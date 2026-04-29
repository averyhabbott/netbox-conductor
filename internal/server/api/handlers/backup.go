package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/configgen"
	"github.com/averyhabbott/netbox-conductor/internal/server/crypto"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/events"
	"github.com/averyhabbott/netbox-conductor/internal/server/hub"
	"github.com/averyhabbott/netbox-conductor/internal/server/patroni"
	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// BackupHandler handles backup configuration, scheduling, catalog, and restore.
type BackupHandler struct {
	clusters    *queries.ClusterQuerier
	nodes       *queries.NodeQuerier
	targets     *queries.BackupTargetQuerier
	schedules   *queries.BackupScheduleQuerier
	runs        *queries.BackupRunQuerier
	catalog     *queries.BackupCatalogQuerier
	taskResults *queries.TaskResultQuerier
	enc         *crypto.Encryptor
	dispatcher  *hub.Dispatcher
	hub         *hub.Hub
	creds       *queries.CredentialQuerier
	witness     *patroni.WitnessManager
	emitter     events.Emitter
}

func (h *BackupHandler) SetEmitter(e events.Emitter) { h.emitter = e }

func NewBackupHandler(
	clusters *queries.ClusterQuerier,
	nodes *queries.NodeQuerier,
	targets *queries.BackupTargetQuerier,
	schedules *queries.BackupScheduleQuerier,
	runs *queries.BackupRunQuerier,
	catalog *queries.BackupCatalogQuerier,
	taskResults *queries.TaskResultQuerier,
	enc *crypto.Encryptor,
	dispatcher *hub.Dispatcher,
	h *hub.Hub,
	creds *queries.CredentialQuerier,
	witness *patroni.WitnessManager,
) *BackupHandler {
	return &BackupHandler{
		clusters:    clusters,
		nodes:       nodes,
		targets:     targets,
		schedules:   schedules,
		runs:        runs,
		catalog:     catalog,
		taskResults: taskResults,
		enc:         enc,
		dispatcher:  dispatcher,
		hub:         h,
		creds:       creds,
		witness:     witness,
	}
}

// GetBackupConfig returns the backup targets and schedule for a cluster.
// GET /api/v1/clusters/:id/backup-config
func (h *BackupHandler) GetBackupConfig(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	ctx := c.Request().Context()

	targetList, err := h.targets.ListByCluster(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list backup targets")
	}

	schedule, _ := h.schedules.Get(ctx, clusterID) // nil = not yet configured
	cached, _ := h.catalog.GetLatest(ctx, clusterID)

	return c.JSON(http.StatusOK, map[string]any{
		"cluster_id":     clusterID.String(),
		"targets":        serializeTargets(targetList),
		"schedule":       serializeSchedule(schedule),
		"cached_catalog": serializeCatalog(cached),
	})
}

// PutBackupSchedule saves the backup schedule for a cluster.
// PUT /api/v1/clusters/:id/backup-config
func (h *BackupHandler) PutBackupSchedule(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	var req struct {
		Enabled               bool   `json:"enabled"`
		FullBackupCron        string `json:"full_backup_cron"`
		DiffBackupCron        string `json:"diff_backup_cron"`
		IncrBackupIntervalHrs int    `json:"incr_backup_interval_hrs"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.FullBackupCron == "" {
		req.FullBackupCron = "0 1 * * 0"
	}
	if req.DiffBackupCron == "" {
		req.DiffBackupCron = "0 1 * * 1-6"
	}
	if req.IncrBackupIntervalHrs <= 0 {
		req.IncrBackupIntervalHrs = 1
	}

	schedule, err := h.schedules.Upsert(c.Request().Context(), queries.UpsertBackupScheduleParams{
		ClusterID:             clusterID,
		Enabled:               req.Enabled,
		FullBackupCron:        req.FullBackupCron,
		DiffBackupCron:        req.DiffBackupCron,
		IncrBackupIntervalHrs: req.IncrBackupIntervalHrs,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save backup schedule")
	}

	return c.JSON(http.StatusOK, map[string]any{
		"cluster_id": clusterID.String(),
		"schedule":   serializeSchedule(schedule),
	})
}

// CreateBackupTarget adds a new backup storage location for a cluster.
// POST /api/v1/clusters/:id/backup-targets
func (h *BackupHandler) CreateBackupTarget(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	ctx := c.Request().Context()

	p, err := h.bindCreateTarget(ctx, c, clusterID)
	if err != nil {
		return err
	}

	nextIdx, err := h.targets.NextRepoIndex(ctx, clusterID)
	if err != nil || nextIdx == 0 {
		return echo.NewHTTPError(http.StatusConflict, "maximum of 4 storage locations already configured")
	}
	p.RepoIndex = nextIdx

	target, err := h.targets.Create(ctx, *p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create backup target: "+err.Error())
	}

	if h.emitter != nil {
		h.emitter.Emit(events.New(events.CategoryConfig, events.SeverityInfo, events.CodeNodeConfigUpdated,
			fmt.Sprintf("Backup storage location added: %s", target.Label),
			actorFromCtx(c)).Cluster(clusterID).Build())
	}

	return c.JSON(http.StatusCreated, serializeTarget(target))
}

// UpdateBackupTarget updates a backup storage location.
// PUT /api/v1/clusters/:id/backup-targets/:tid
func (h *BackupHandler) UpdateBackupTarget(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	tid, err := uuid.Parse(c.Param("tid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid target id")
	}
	ctx := c.Request().Context()

	existing, err := h.targets.Get(ctx, tid)
	if err != nil || existing.ClusterID != clusterID {
		return echo.NewHTTPError(http.StatusNotFound, "backup target not found")
	}

	p, httpErr := h.bindUpdateTarget(ctx, c, existing)
	if httpErr != nil {
		return httpErr
	}

	target, err := h.targets.Update(ctx, *p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update backup target: "+err.Error())
	}

	return c.JSON(http.StatusOK, serializeTarget(target))
}

// DeleteBackupTarget removes a backup storage location.
// DELETE /api/v1/clusters/:id/backup-targets/:tid
func (h *BackupHandler) DeleteBackupTarget(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	tid, err := uuid.Parse(c.Param("tid"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid target id")
	}
	ctx := c.Request().Context()

	existing, err := h.targets.Get(ctx, tid)
	if err != nil || existing.ClusterID != clusterID {
		return echo.NewHTTPError(http.StatusNotFound, "backup target not found")
	}

	if err := h.targets.Delete(ctx, tid); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete backup target")
	}

	return c.JSON(http.StatusOK, map[string]any{"deleted": tid.String()})
}

// EnableBackups runs the full backup bootstrap sequence for a cluster:
//  1. Push pgbackrest.conf to all DB nodes.
//  2. Push Patroni config with archive_mode/archive_command to all DB nodes (requires restart).
//  3. Background: wait for Patroni config tasks → dispatch stanza-create to primary →
//     wait for success → set stanza_initialized=true.
//
// POST /api/v1/clusters/:id/backup-config/enable
func (h *BackupHandler) EnableBackups(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	ctx := c.Request().Context()

	cluster, err := h.clusters.GetByID(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	targetList, err := h.targets.ListByCluster(ctx, clusterID)
	if err != nil || len(targetList) == 0 {
		return echo.NewHTTPError(http.StatusConflict, "add at least one backup storage location before enabling backups")
	}

	nodes, err := h.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

	dbNodes := dbRoleNodes(nodes)
	if len(dbNodes) == 0 {
		return echo.NewHTTPError(http.StatusConflict, "no database nodes found in cluster")
	}

	stanzaName := cluster.PatroniScope

	// Ensure a schedule row exists so SetStanzaInitialized has a row to UPDATE.
	// If the user hasn't saved a schedule yet, create one with defaults.
	// If one already exists, leave its values untouched.
	if existing, _ := h.schedules.Get(ctx, clusterID); existing == nil {
		if _, err := h.schedules.Upsert(ctx, queries.UpsertBackupScheduleParams{
			ClusterID:             clusterID,
			Enabled:               true,
			FullBackupCron:        "0 1 * * 0",
			DiffBackupCron:        "0 1 * * 1-6",
			IncrBackupIntervalHrs: 1,
		}); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to initialize backup schedule: "+err.Error())
		}
	}

	// Fetch cluster credentials for Patroni config rendering.
	superUser, superPass := "postgres", ""
	replicaUser, replicaPass := "replicator", ""
	restUser, restPass := "patroni", ""
	if cred, err := h.creds.GetByKind(ctx, clusterID, "postgres_superuser"); err == nil {
		superUser = cred.Username
		if pw, e := h.enc.Decrypt(cred.PasswordEnc); e == nil {
			superPass = string(pw)
		}
	}
	if cred, err := h.creds.GetByKind(ctx, clusterID, "postgres_replication"); err == nil {
		replicaUser = cred.Username
		if pw, e := h.enc.Decrypt(cred.PasswordEnc); e == nil {
			replicaPass = string(pw)
		}
	}
	if cred, err := h.creds.GetByKind(ctx, clusterID, "patroni_rest_password"); err == nil {
		restUser = cred.Username
		if pw, e := h.enc.Decrypt(cred.PasswordEnc); e == nil {
			restPass = string(pw)
		}
	}

	// Witness address (empty for HA 3+ node clusters).
	witnessAddr := ""
	if h.witness != nil {
		witnessAddr = h.witness.Addr(clusterID)
	}

	// Build Raft peer list.
	raftPeers := make([]string, 0, len(dbNodes))
	for _, n := range dbNodes {
		nodeIP, _, _ := strings.Cut(n.IPAddress, "/")
		raftPeers = append(raftPeers, nodeIP+":5433")
	}

	// Render pgbackrest.conf.
	pgbConf, err := renderPGBackRestConf(stanzaName, targetList, h.enc)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to render backup configuration: "+err.Error())
	}

	type taskRef struct {
		NodeID   string `json:"node_id"`
		Hostname string `json:"hostname"`
		TaskID   string `json:"task_id,omitempty"`
		Status   string `json:"status"`
		Error    string `json:"error,omitempty"`
	}

	var primaryNode *queries.Node
	pgbTasks := make([]taskRef, 0, len(dbNodes))
	patroniTaskIDs := make([]uuid.UUID, 0, len(dbNodes))
	patroniTasks := make([]taskRef, 0, len(dbNodes))

	for i := range dbNodes {
		n := &dbNodes[i]
		nodeIP, _, _ := strings.Cut(n.IPAddress, "/")

		// Identify primary from Patroni state.
		if n.PatroniState != nil {
			var ps map[string]any
			if json.Unmarshal(n.PatroniState, &ps) == nil && ps["role"] == "primary" {
				primaryNode = n
			}
		}

		// 1. Dispatch pgbackrest.configure.
		pgbParams, _ := json.Marshal(protocol.PGBackRestConfigParams{Config: pgbConf})
		pgbTaskID := uuid.New()
		_ = h.taskResults.Create(ctx, n.ID, pgbTaskID, string(protocol.TaskPGBackRestConfigure), pgbParams)
		if dispErr := h.dispatcher.Dispatch(n.ID, protocol.TaskDispatchPayload{
			TaskID:      pgbTaskID.String(),
			TaskType:    protocol.TaskPGBackRestConfigure,
			Params:      json.RawMessage(pgbParams),
			TimeoutSecs: 30,
		}); dispErr != nil {
			pgbTasks = append(pgbTasks, taskRef{NodeID: n.ID.String(), Hostname: n.Hostname, Status: "offline", Error: dispErr.Error()})
		} else {
			_ = h.taskResults.SetSent(ctx, pgbTaskID)
			pgbTasks = append(pgbTasks, taskRef{NodeID: n.ID.String(), Hostname: n.Hostname, TaskID: pgbTaskID.String(), Status: "dispatched"})
		}

		// 2a. Dispatch patroni.write_config with archive settings.
		partners := make([]string, 0, len(raftPeers)-1)
		for _, p := range raftPeers {
			if !strings.HasPrefix(p, nodeIP+":") {
				partners = append(partners, p)
			}
		}
		patroniContent, renderErr := configgen.RenderPatroni(configgen.PatroniInput{
			Scope:         stanzaName,
			NodeName:      n.Hostname,
			NodeAddr:      nodeIP,
			RaftSelfAddr:  nodeIP + ":5433",
			RaftPartners:  partners,
			WitnessAddr:   witnessAddr,
			RESTUsername:  restUser,
			RESTPassword:  restPass,
			DBSuperUser:   superUser,
			DBSuperPass:   superPass,
			DBReplicaUser: replicaUser,
			DBReplicaPass: replicaPass,
			ArchiveEnabled: true,
			ArchiveStanza:  stanzaName,
		})
		if renderErr != nil {
			patroniTasks = append(patroniTasks, taskRef{NodeID: n.ID.String(), Hostname: n.Hostname, Status: "error", Error: renderErr.Error()})
			continue
		}
		writeParams, _ := json.Marshal(protocol.PatroniConfigWriteParams{Content: patroniContent})
		writeTaskID := uuid.New()
		_ = h.taskResults.Create(ctx, n.ID, writeTaskID, string(protocol.TaskWritePatroniConf), writeParams)
		if dispErr := h.dispatcher.Dispatch(n.ID, protocol.TaskDispatchPayload{
			TaskID:      writeTaskID.String(),
			TaskType:    protocol.TaskWritePatroniConf,
			Params:      json.RawMessage(writeParams),
			TimeoutSecs: 30,
		}); dispErr != nil {
			patroniTasks = append(patroniTasks, taskRef{NodeID: n.ID.String(), Hostname: n.Hostname, Status: "offline", Error: dispErr.Error()})
			continue
		}
		_ = h.taskResults.SetSent(ctx, writeTaskID)
		patroniTasks = append(patroniTasks, taskRef{NodeID: n.ID.String(), Hostname: n.Hostname, TaskID: writeTaskID.String(), Status: "dispatched"})

		// 2b. Dispatch service.restart.patroni — agent processes tasks in order
		// so this runs after the config write completes on that node.
		restartParams, _ := json.Marshal(struct{}{})
		restartTaskID := uuid.New()
		_ = h.taskResults.Create(ctx, n.ID, restartTaskID, string(protocol.TaskRestartPatroni), restartParams)
		if dispErr := h.dispatcher.Dispatch(n.ID, protocol.TaskDispatchPayload{
			TaskID:      restartTaskID.String(),
			TaskType:    protocol.TaskRestartPatroni,
			Params:      json.RawMessage(restartParams),
			TimeoutSecs: 60,
		}); dispErr != nil {
			patroniTasks = append(patroniTasks, taskRef{NodeID: n.ID.String(), Hostname: n.Hostname, Status: "offline", Error: "restart: " + dispErr.Error()})
		} else {
			_ = h.taskResults.SetSent(ctx, restartTaskID)
			patroniTaskIDs = append(patroniTaskIDs, restartTaskID) // wait on restart, not write
			patroniTasks = append(patroniTasks, taskRef{NodeID: n.ID.String(), Hostname: n.Hostname, TaskID: restartTaskID.String(), Status: "dispatched"})
		}
	}

	// Fall back to any connected node if primary is unknown.
	if primaryNode == nil {
		for i := range dbNodes {
			if h.hub.IsConnected(dbNodes[i].ID) {
				primaryNode = &dbNodes[i]
				break
			}
		}
	}

	var stanzaTask *taskRef
	var stanzaTaskID uuid.UUID
	if primaryNode != nil {
		stanzaTaskID = uuid.New()
		stanzaParams, _ := json.Marshal(protocol.PGBackRestStanzaCreateParams{Stanza: stanzaName})
		// Register the waiter before dispatching to avoid a race.
		_ = h.taskResults.Create(ctx, primaryNode.ID, stanzaTaskID, string(protocol.TaskPGBackRestStanzaCreate), stanzaParams)
		ref := taskRef{NodeID: primaryNode.ID.String(), Hostname: primaryNode.Hostname, TaskID: stanzaTaskID.String(), Status: "pending"}
		// Stanza-create is dispatched in the background goroutine after Patroni
		// config tasks finish — only pre-register the task here.
		stanzaTask = &ref
	}

	if h.emitter != nil {
		h.emitter.Emit(events.New(events.CategoryConfig, events.SeverityInfo, events.CodePatroniConfigured,
			"Backup configuration pushed to cluster nodes — activating WAL archiving",
			actorFromCtx(c)).Cluster(clusterID).Build())
	}

	// Background goroutine: poll DB for Patroni restart tasks to complete, then
	// dispatch stanza-create and poll for its success before marking stanza_initialized.
	// DB polling avoids the WaitForTask race condition where tasks complete before
	// the waiter is registered.
	if primaryNode != nil {
		capturedPrimary := primaryNode
		capturedPatroniIDs := patroniTaskIDs
		capturedStanzaID := stanzaTaskID
		go func() {
			bgCtx := context.Background()

			// 1. Wait for all Patroni restart tasks to reach a terminal state.
			for _, tid := range capturedPatroniIDs {
				if ok := pollTaskSuccess(bgCtx, h.taskResults, tid, 5*time.Minute); !ok {
					slog.Warn("enable-backups: Patroni restart task did not succeed", "task_id", tid)
					return
				}
			}

			// 2. Poll heartbeat data until the cluster reports a stable primary.
			//    Heartbeat interval is 3s; poll every 5s to allow Patroni to
			//    finish its election before we read the result.
			var currentPrimary *queries.Node
			deadline := time.Now().Add(2 * time.Minute)
			for time.Now().Before(deadline) {
				if fresh, err := h.nodes.ListByCluster(bgCtx, clusterID); err == nil {
					if p := primaryDBNode(fresh); p != nil {
						currentPrimary = p
						break
					}
				}
				time.Sleep(5 * time.Second)
			}
			if currentPrimary == nil {
				slog.Warn("enable-backups: no Patroni primary found after restart, falling back to original",
					"original", capturedPrimary.Hostname)
				currentPrimary = capturedPrimary
			}

			// 3. Switchover to the intended primary if leadership drifted.
			//    Intended = highest-priority connected DB node.
			if fresh, err := h.nodes.ListByCluster(bgCtx, clusterID); err == nil {
				if intended := intendedPrimaryNode(fresh); intended != nil && intended.ID != currentPrimary.ID {
					slog.Info("enable-backups: primary drifted after restart, initiating switchover",
						"current", currentPrimary.Hostname, "intended", intended.Hostname)
					switchParams, _ := json.Marshal(protocol.PatroniSwitchoverParams{Candidate: intended.Hostname})
					switchTaskID := uuid.New()
					_ = h.taskResults.Create(bgCtx, currentPrimary.ID, switchTaskID,
						string(protocol.TaskPatroniSwitchover), switchParams)
					if dispErr := h.dispatcher.Dispatch(currentPrimary.ID, protocol.TaskDispatchPayload{
						TaskID:      switchTaskID.String(),
						TaskType:    protocol.TaskPatroniSwitchover,
						Params:      json.RawMessage(switchParams),
						TimeoutSecs: 60,
					}); dispErr == nil {
						_ = h.taskResults.SetSent(bgCtx, switchTaskID)
						if ok := pollTaskSuccess(bgCtx, h.taskResults, switchTaskID, 2*time.Minute); ok {
							currentPrimary = intended
							slog.Info("enable-backups: switchover complete", "primary", currentPrimary.Hostname)
						} else {
							slog.Warn("enable-backups: switchover did not complete, proceeding with current primary",
								"current", currentPrimary.Hostname)
						}
					}
				}
			}

			// 4. Dispatch stanza-create to the confirmed primary.
			stanzaParams, _ := json.Marshal(protocol.PGBackRestStanzaCreateParams{Stanza: stanzaName})
			if dispErr := h.dispatcher.Dispatch(currentPrimary.ID, protocol.TaskDispatchPayload{
				TaskID:      capturedStanzaID.String(),
				TaskType:    protocol.TaskPGBackRestStanzaCreate,
				Params:      json.RawMessage(stanzaParams),
				TimeoutSecs: 120,
			}); dispErr != nil {
				slog.Warn("enable-backups: stanza-create dispatch failed",
					"node", currentPrimary.Hostname, "error", dispErr)
				return
			}
			_ = h.taskResults.SetSent(bgCtx, capturedStanzaID)

			if ok := pollTaskSuccess(bgCtx, h.taskResults, capturedStanzaID, 5*time.Minute); !ok {
				return
			}

			_ = h.schedules.SetStanzaInitialized(bgCtx, clusterID, stanzaName)
		}()
	}

	return c.JSON(http.StatusAccepted, map[string]any{
		"cluster_id":  clusterID.String(),
		"pgbackrest":  pgbTasks,
		"patroni":     patroniTasks,
		"stanza_task": stanzaTask,
	})
}

// PushBackupConfig re-renders and pushes pgbackrest.conf to all nodes.
// POST /api/v1/clusters/:id/backup-config/push
func (h *BackupHandler) PushBackupConfig(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	ctx := c.Request().Context()

	cluster, err := h.clusters.GetByID(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	targetList, err := h.targets.ListByCluster(ctx, clusterID)
	if err != nil || len(targetList) == 0 {
		return echo.NewHTTPError(http.StatusConflict, "no backup storage locations configured")
	}

	nodes, err := h.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

	pgbConf, err := renderPGBackRestConf(cluster.PatroniScope, targetList, h.enc)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to render backup configuration: "+err.Error())
	}

	type taskRef struct {
		NodeID   string `json:"node_id"`
		Hostname string `json:"hostname"`
		TaskID   string `json:"task_id,omitempty"`
		Status   string `json:"status"`
		Error    string `json:"error,omitempty"`
	}

	results := make([]taskRef, 0)
	for _, n := range dbRoleNodes(nodes) {
		params, _ := json.Marshal(protocol.PGBackRestConfigParams{Config: pgbConf})
		taskID := uuid.New()
		_ = h.taskResults.Create(ctx, n.ID, taskID, string(protocol.TaskPGBackRestConfigure), params)
		if dispErr := h.dispatcher.Dispatch(n.ID, protocol.TaskDispatchPayload{
			TaskID:      taskID.String(),
			TaskType:    protocol.TaskPGBackRestConfigure,
			Params:      json.RawMessage(params),
			TimeoutSecs: 30,
		}); dispErr != nil {
			results = append(results, taskRef{NodeID: n.ID.String(), Hostname: n.Hostname, Status: "offline", Error: dispErr.Error()})
			continue
		}
		_ = h.taskResults.SetSent(ctx, taskID)
		results = append(results, taskRef{NodeID: n.ID.String(), Hostname: n.Hostname, TaskID: taskID.String(), Status: "dispatched"})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"cluster_id": clusterID.String(),
		"nodes":      results,
	})
}

// GetBackupCatalog fetches the latest backup catalog from the primary node.
// GET /api/v1/clusters/:id/backup-catalog
func (h *BackupHandler) GetBackupCatalog(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	ctx := c.Request().Context()

	schedule, err := h.schedules.Get(ctx, clusterID)
	if err != nil || schedule.StanzaName == nil {
		return echo.NewHTTPError(http.StatusConflict, "backups not yet configured for this cluster")
	}

	nodes, err := h.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

	primary := primaryDBNode(nodes)
	if primary == nil {
		return echo.NewHTTPError(http.StatusConflict, "no connected database node found")
	}

	params, _ := json.Marshal(protocol.PGBackRestCatalogParams{Stanza: *schedule.StanzaName})
	taskID := uuid.New()
	_ = h.taskResults.Create(ctx, primary.ID, taskID, string(protocol.TaskPGBackRestCatalog), params)
	if dispErr := h.dispatcher.Dispatch(primary.ID, protocol.TaskDispatchPayload{
		TaskID:      taskID.String(),
		TaskType:    protocol.TaskPGBackRestCatalog,
		Params:      json.RawMessage(params),
		TimeoutSecs: 60,
	}); dispErr != nil {
		return echo.NewHTTPError(http.StatusConflict, "primary node is not connected: "+dispErr.Error())
	}
	_ = h.taskResults.SetSent(ctx, taskID)

	// Also return the cached catalog if available.
	cached, _ := h.catalog.GetLatest(ctx, clusterID)

	return c.JSON(http.StatusAccepted, map[string]any{
		"cluster_id":     clusterID.String(),
		"task_id":        taskID.String(),
		"cached_catalog": serializeCatalog(cached),
	})
}

// RunBackup triggers a manual backup.
// POST /api/v1/clusters/:id/backup/run
func (h *BackupHandler) RunBackup(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	var req struct {
		Type string `json:"type"` // "full" | "diff" | "incr"
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.Type == "" {
		req.Type = "full"
	}
	if req.Type != "full" && req.Type != "diff" && req.Type != "incr" {
		return echo.NewHTTPError(http.StatusBadRequest, "type must be full, diff, or incr")
	}

	ctx := c.Request().Context()

	schedule, err := h.schedules.Get(ctx, clusterID)
	if err != nil || schedule.StanzaName == nil {
		return echo.NewHTTPError(http.StatusConflict, "backups not yet configured for this cluster")
	}

	nodes, err := h.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

	primary := primaryDBNode(nodes)
	if primary == nil {
		return echo.NewHTTPError(http.StatusConflict, "no connected database node found")
	}

	params, _ := json.Marshal(protocol.PGBackRestBackupParams{
		Stanza: *schedule.StanzaName,
		Type:   req.Type,
	})
	taskID := uuid.New()
	_ = h.taskResults.Create(ctx, primary.ID, taskID, string(protocol.TaskPGBackRestBackup), params)
	if dispErr := h.dispatcher.Dispatch(primary.ID, protocol.TaskDispatchPayload{
		TaskID:      taskID.String(),
		TaskType:    protocol.TaskPGBackRestBackup,
		Params:      json.RawMessage(params),
		TimeoutSecs: 3600,
	}); dispErr != nil {
		return echo.NewHTTPError(http.StatusConflict, "primary node is not connected: "+dispErr.Error())
	}
	_ = h.taskResults.SetSent(ctx, taskID)

	// Create a backup_run record for tracking.
	run, _ := h.runs.Create(ctx, clusterID, req.Type, time.Now().UTC(), 1)
	if run != nil {
		_ = h.runs.SetDispatched(ctx, run.ID, taskID)
	}

	return c.JSON(http.StatusAccepted, map[string]any{
		"cluster_id": clusterID.String(),
		"task_id":    taskID.String(),
		"node_id":    primary.ID.String(),
		"hostname":   primary.Hostname,
		"type":       req.Type,
		"status":     "dispatched",
	})
}

// ClusterRestore performs a whole-cluster point-in-time restore.
// Stops Patroni on all nodes → restores primary → reinitializes replicas.
// POST /api/v1/clusters/:id/db-restore
func (h *BackupHandler) ClusterRestore(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	var req struct {
		TargetTime     string `json:"target_time"`      // RFC3339 UTC
		RestoreNodeID  string `json:"restore_node_id"`  // optional: override which node restores
		RestoreCmd     string `json:"restore_cmd"`      // optional override for pgbackrest command
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.TargetTime == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "target_time is required (RFC3339 UTC)")
	}
	if _, err := time.Parse(time.RFC3339, req.TargetTime); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "target_time must be RFC3339 format (e.g. 2025-04-15T18:30:00Z)")
	}

	ctx := c.Request().Context()

	schedule, err := h.schedules.Get(ctx, clusterID)
	if err != nil || schedule.StanzaName == nil {
		return echo.NewHTTPError(http.StatusConflict, "backups not yet configured for this cluster")
	}
	if schedule.RestoreInProgress {
		return echo.NewHTTPError(http.StatusConflict, "a restore is already in progress for this cluster")
	}

	nodes, err := h.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

	dbNodes := dbRoleNodes(nodes)
	if len(dbNodes) == 0 {
		return echo.NewHTTPError(http.StatusConflict, "no database nodes found in cluster")
	}

	// Choose restore target node.
	var restoreNode *queries.Node
	if req.RestoreNodeID != "" {
		rid, parseErr := uuid.Parse(req.RestoreNodeID)
		if parseErr != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid restore_node_id")
		}
		for i := range dbNodes {
			if dbNodes[i].ID == rid {
				restoreNode = &dbNodes[i]
				break
			}
		}
		if restoreNode == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "restore_node_id not found in cluster db nodes")
		}
	} else {
		restoreNode = primaryDBNode(nodes)
		if restoreNode == nil {
			return echo.NewHTTPError(http.StatusConflict, "no connected primary node found; specify restore_node_id to pick an online node")
		}
	}

	// Mark restore in progress.
	_ = h.schedules.SetRestoreInProgress(ctx, clusterID, true)

	type taskRef struct {
		NodeID   string `json:"node_id"`
		Hostname string `json:"hostname"`
		TaskID   string `json:"task_id,omitempty"`
		Status   string `json:"status"`
		Error    string `json:"error,omitempty"`
	}

	// Stop Patroni on all db nodes before restoring.
	stopTasks := make([]taskRef, 0, len(dbNodes))
	for _, n := range dbNodes {
		stopParams, _ := json.Marshal(protocol.RunCommandParams{Command: "systemctl stop patroni"})
		taskID := uuid.New()
		_ = h.taskResults.Create(ctx, n.ID, taskID, string(protocol.TaskRunCommand), stopParams)
		if dispErr := h.dispatcher.Dispatch(n.ID, protocol.TaskDispatchPayload{
			TaskID:      taskID.String(),
			TaskType:    protocol.TaskRunCommand,
			Params:      json.RawMessage(stopParams),
			TimeoutSecs: 30,
		}); dispErr != nil {
			stopTasks = append(stopTasks, taskRef{NodeID: n.ID.String(), Hostname: n.Hostname, Status: "offline", Error: dispErr.Error()})
			continue
		}
		_ = h.taskResults.SetSent(ctx, taskID)
		stopTasks = append(stopTasks, taskRef{NodeID: n.ID.String(), Hostname: n.Hostname, TaskID: taskID.String(), Status: "dispatched"})
	}

	// Dispatch restore to primary node.
	restoreParams, _ := json.Marshal(protocol.PGBackRestRestoreParams{
		Stanza:     *schedule.StanzaName,
		TargetTime: req.TargetTime,
		RestoreCmd: req.RestoreCmd,
	})
	restoreTaskID := uuid.New()
	_ = h.taskResults.Create(ctx, restoreNode.ID, restoreTaskID, string(protocol.TaskPGBackRestRestore), restoreParams)
	var restoreTask taskRef
	if dispErr := h.dispatcher.Dispatch(restoreNode.ID, protocol.TaskDispatchPayload{
		TaskID:      restoreTaskID.String(),
		TaskType:    protocol.TaskPGBackRestRestore,
		Params:      json.RawMessage(restoreParams),
		TimeoutSecs: 3600,
	}); dispErr != nil {
		restoreTask = taskRef{NodeID: restoreNode.ID.String(), Hostname: restoreNode.Hostname, Status: "offline", Error: dispErr.Error()}
		_ = h.schedules.SetRestoreInProgress(ctx, clusterID, false)
	} else {
		_ = h.taskResults.SetSent(ctx, restoreTaskID)
		restoreTask = taskRef{NodeID: restoreNode.ID.String(), Hostname: restoreNode.Hostname, TaskID: restoreTaskID.String(), Status: "dispatched"}
	}

	// Dispatch db.restore (reinitialize) to each replica.
	replicaTasks := make([]taskRef, 0)
	for _, n := range dbNodes {
		if n.ID == restoreNode.ID {
			continue
		}
		stanzaStr := ""
		if schedule.StanzaName != nil {
			stanzaStr = *schedule.StanzaName
		}
		replicaParams, _ := json.Marshal(protocol.DBRestoreParams{
			Method:       "reinitialize",
			PatroniScope: stanzaStr,
		})
		taskID := uuid.New()
		_ = h.taskResults.Create(ctx, n.ID, taskID, string(protocol.TaskDBRestore), replicaParams)
		if dispErr := h.dispatcher.Dispatch(n.ID, protocol.TaskDispatchPayload{
			TaskID:      taskID.String(),
			TaskType:    protocol.TaskDBRestore,
			Params:      json.RawMessage(replicaParams),
			TimeoutSecs: 3600,
		}); dispErr != nil {
			replicaTasks = append(replicaTasks, taskRef{NodeID: n.ID.String(), Hostname: n.Hostname, Status: "offline", Error: dispErr.Error()})
			continue
		}
		_ = h.taskResults.SetSent(ctx, taskID)
		replicaTasks = append(replicaTasks, taskRef{NodeID: n.ID.String(), Hostname: n.Hostname, TaskID: taskID.String(), Status: "dispatched"})
	}

	if h.emitter != nil {
		h.emitter.Emit(events.New(events.CategoryHA, events.SeverityWarn, events.CodeFailoverInitiated,
			fmt.Sprintf("Database restore initiated to %s", req.TargetTime),
			actorFromCtx(c)).Cluster(clusterID).Build())
	}

	return c.JSON(http.StatusAccepted, map[string]any{
		"cluster_id":    clusterID.String(),
		"target_time":   req.TargetTime,
		"restore_node":  restoreTask,
		"stop_tasks":    stopTasks,
		"replica_tasks": replicaTasks,
	})
}

type nodeTestResult struct {
	NodeID     string `json:"node_id"`
	Hostname   string `json:"hostname"`
	TaskID     string `json:"task_id"`     // "" when skipped
	SkipReason string `json:"skip_reason"` // "" | "offline" | "maintenance"
}

// TestBackupPath dispatches a write test to all DB/HC nodes in the cluster.
// Disconnected and maintenance-mode nodes are returned as skipped immediately.
// POST /api/v1/clusters/:id/backup-path/test
func (h *BackupHandler) TestBackupPath(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := c.Bind(&req); err != nil || req.Path == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "path is required")
	}
	ctx := c.Request().Context()

	nodes, err := h.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}

	params, _ := json.Marshal(protocol.PGBackRestTestPathParams{Path: req.Path})
	results := make([]nodeTestResult, 0)

	for _, node := range nodes {
		if node.Role != "db_only" && node.Role != "hyperconverged" {
			continue
		}
		if node.MaintenanceMode {
			results = append(results, nodeTestResult{
				NodeID:     node.ID.String(),
				Hostname:   node.Hostname,
				SkipReason: "maintenance",
			})
			continue
		}
		if node.AgentStatus != "connected" {
			results = append(results, nodeTestResult{
				NodeID:     node.ID.String(),
				Hostname:   node.Hostname,
				SkipReason: "offline",
			})
			continue
		}
		taskID := uuid.New()
		_ = h.taskResults.Create(ctx, node.ID, taskID, string(protocol.TaskPGBackRestTestPath), params)
		if dispErr := h.dispatcher.Dispatch(node.ID, protocol.TaskDispatchPayload{
			TaskID:      taskID.String(),
			TaskType:    protocol.TaskPGBackRestTestPath,
			Params:      json.RawMessage(params),
			TimeoutSecs: 15,
		}); dispErr != nil {
			results = append(results, nodeTestResult{
				NodeID:     node.ID.String(),
				Hostname:   node.Hostname,
				SkipReason: "offline",
			})
			continue
		}
		_ = h.taskResults.SetSent(ctx, taskID)
		results = append(results, nodeTestResult{
			NodeID:   node.ID.String(),
			Hostname: node.Hostname,
			TaskID:   taskID.String(),
		})
	}

	return c.JSON(http.StatusAccepted, results)
}

// GetBackupRuns returns backup run history for a cluster.
// GET /api/v1/clusters/:id/backup-runs
func (h *BackupHandler) GetBackupRuns(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	runs, err := h.runs.ListByCluster(c.Request().Context(), clusterID, 50)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list backup runs")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"cluster_id": clusterID.String(),
		"runs":       serializeRuns(runs),
	})
}

// ─────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────

func dbRoleNodes(nodes []queries.Node) []queries.Node {
	out := make([]queries.Node, 0)
	for _, n := range nodes {
		if n.Role == "hyperconverged" || n.Role == "db_only" {
			out = append(out, n)
		}
	}
	return out
}

// intendedPrimaryNode returns the connected DB node with the highest failover
// priority — the node that should be Patroni leader after a stable restart.
func intendedPrimaryNode(nodes []queries.Node) *queries.Node {
	var best *queries.Node
	for i := range nodes {
		n := &nodes[i]
		if n.Role != "hyperconverged" && n.Role != "db_only" {
			continue
		}
		if n.AgentStatus != "connected" {
			continue
		}
		if best == nil || n.FailoverPriority > best.FailoverPriority {
			best = n
		}
	}
	return best
}

func primaryDBNode(nodes []queries.Node) *queries.Node {
	var best *queries.Node
	for i := range nodes {
		n := &nodes[i]
		if n.Role != "hyperconverged" && n.Role != "db_only" {
			continue
		}
		if n.AgentStatus != "connected" {
			continue
		}
		if n.PatroniState != nil {
			var ps map[string]any
			if json.Unmarshal(n.PatroniState, &ps) == nil && ps["role"] == "primary" {
				return n
			}
		}
		if best == nil {
			best = n
		}
	}
	return best
}

func renderPGBackRestConf(stanza string, targets []queries.BackupTarget, enc *crypto.Encryptor) (string, error) {
	decryptField := func(encB64 *string) string {
		if encB64 == nil {
			return ""
		}
		raw, err := base64.StdEncoding.DecodeString(*encB64)
		if err != nil {
			return ""
		}
		plain, err := enc.Decrypt(raw)
		if err != nil {
			return ""
		}
		return string(plain)
	}

	repos := make([]configgen.PGBackRestRepo, 0, len(targets))
	for _, t := range targets {
		days := t.RecoveryDays
		if days == 0 {
			days = 14
		}
		repo := configgen.PGBackRestRepo{
			Index:            t.RepoIndex,
			RepoType:         t.TargetType,
			Label:            t.Label,
			FullRetention:    int(math.Ceil(float64(days)/7)) + 1,
			DiffRetention:    days,
			WALRetentionDays: days + 7,
		}

		switch t.TargetType {
		case "posix":
			if t.PosixPath != nil {
				repo.PosixPath = *t.PosixPath
			}
		case "s3":
			if t.S3Bucket != nil {
				repo.S3Bucket = *t.S3Bucket
			}
			if t.S3Region != nil {
				repo.S3Region = *t.S3Region
			}
			if t.S3Endpoint != nil {
				repo.S3Endpoint = *t.S3Endpoint
			}
			repo.S3KeyID = decryptField(t.S3KeyIDEnc)
			repo.S3Secret = decryptField(t.S3SecretEnc)
		case "gcs":
			if t.GCSBucket != nil {
				repo.GCSBucket = *t.GCSBucket
			}
			repo.GCSKey = decryptField(t.GCSKeyEnc)
		case "azure":
			if t.AzureAccount != nil {
				repo.AzureAccount = *t.AzureAccount
			}
			if t.AzureContainer != nil {
				repo.AzureContainer = *t.AzureContainer
			}
			repo.AzureKey = decryptField(t.AzureKeyEnc)
		case "sftp":
			if t.SFTPHost != nil {
				repo.SFTPHost = *t.SFTPHost
			}
			if t.SFTPPort != nil {
				repo.SFTPPort = *t.SFTPPort
			}
			if t.SFTPUser != nil {
				repo.SFTPUser = *t.SFTPUser
			}
			if t.SFTPPath != nil {
				repo.SFTPPath = *t.SFTPPath
			}
			repo.SFTPPrivateKey = decryptField(t.SFTPPrivateKeyEnc)
		}
		repos = append(repos, repo)
	}

	return configgen.RenderPGBackRest(configgen.PGBackRestInput{
		Stanza: stanza,
		Repos:  repos,
	})
}

// bindCreateTarget parses a create-target request body and encrypts credentials.
func (h *BackupHandler) bindCreateTarget(_ context.Context, c echo.Context, clusterID uuid.UUID) (*queries.CreateBackupTargetParams, error) {
	var req backupTargetRequest
	if err := c.Bind(&req); err != nil || req.Label == "" || req.TargetType == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "label and target_type are required")
	}
	recoveryDays := req.RecoveryDays
	if recoveryDays == 0 {
		recoveryDays = 14
	}
	p := &queries.CreateBackupTargetParams{
		ClusterID:    clusterID,
		Label:        req.Label,
		TargetType:   req.TargetType,
		RecoveryDays: recoveryDays,
		SyncToNodes:  req.SyncToNodes,
	}

	if err := encryptTargetCredentials(req, p, h.enc); err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "failed to encrypt credentials: "+err.Error())
	}
	return p, nil
}

// bindUpdateTarget parses an update-target request body and encrypts credentials.
func (h *BackupHandler) bindUpdateTarget(_ context.Context, c echo.Context, existing *queries.BackupTarget) (*queries.UpdateBackupTargetParams, error) {
	var req backupTargetRequest
	if err := c.Bind(&req); err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.Label == "" {
		req.Label = existing.Label
	}

	recoveryDays := req.RecoveryDays
	if recoveryDays == 0 {
		recoveryDays = existing.RecoveryDays
	}
	p := &queries.UpdateBackupTargetParams{
		ID:           existing.ID,
		Label:        req.Label,
		RecoveryDays: recoveryDays,
		SyncToNodes:  req.SyncToNodes,

		// Start with existing encrypted values — only overwrite if new plaintext provided.
		PosixPath:      existing.PosixPath,
		S3Bucket:       existing.S3Bucket,
		S3Region:       existing.S3Region,
		S3Endpoint:     existing.S3Endpoint,
		S3KeyIDEnc:     existing.S3KeyIDEnc,
		S3SecretEnc:    existing.S3SecretEnc,
		GCSBucket:      existing.GCSBucket,
		GCSKeyEnc:      existing.GCSKeyEnc,
		AzureAccount:   existing.AzureAccount,
		AzureContainer: existing.AzureContainer,
		AzureKeyEnc:    existing.AzureKeyEnc,
		SFTPHost:       existing.SFTPHost,
		SFTPPort:       existing.SFTPPort,
		SFTPUser:       existing.SFTPUser,
		SFTPPrivateKeyEnc: existing.SFTPPrivateKeyEnc,
		SFTPPath:       existing.SFTPPath,
	}

	createProxy := &queries.CreateBackupTargetParams{}
	if err := encryptTargetCredentials(req, createProxy, h.enc); err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "failed to encrypt credentials: "+err.Error())
	}
	// Overlay only non-nil new values.
	if createProxy.PosixPath != nil {
		p.PosixPath = createProxy.PosixPath
	}
	if createProxy.S3Bucket != nil {
		p.S3Bucket = createProxy.S3Bucket
	}
	if createProxy.S3Region != nil {
		p.S3Region = createProxy.S3Region
	}
	if createProxy.S3Endpoint != nil {
		p.S3Endpoint = createProxy.S3Endpoint
	}
	if createProxy.S3KeyIDEnc != nil {
		p.S3KeyIDEnc = createProxy.S3KeyIDEnc
	}
	if createProxy.S3SecretEnc != nil {
		p.S3SecretEnc = createProxy.S3SecretEnc
	}
	if createProxy.GCSBucket != nil {
		p.GCSBucket = createProxy.GCSBucket
	}
	if createProxy.GCSKeyEnc != nil {
		p.GCSKeyEnc = createProxy.GCSKeyEnc
	}
	if createProxy.AzureAccount != nil {
		p.AzureAccount = createProxy.AzureAccount
	}
	if createProxy.AzureContainer != nil {
		p.AzureContainer = createProxy.AzureContainer
	}
	if createProxy.AzureKeyEnc != nil {
		p.AzureKeyEnc = createProxy.AzureKeyEnc
	}
	if createProxy.SFTPHost != nil {
		p.SFTPHost = createProxy.SFTPHost
	}
	if createProxy.SFTPPort != nil {
		p.SFTPPort = createProxy.SFTPPort
	}
	if createProxy.SFTPUser != nil {
		p.SFTPUser = createProxy.SFTPUser
	}
	if createProxy.SFTPPrivateKeyEnc != nil {
		p.SFTPPrivateKeyEnc = createProxy.SFTPPrivateKeyEnc
	}
	if createProxy.SFTPPath != nil {
		p.SFTPPath = createProxy.SFTPPath
	}

	return p, nil
}

// backupTargetRequest is the API input shape for create/update.
type backupTargetRequest struct {
	Label      string      `json:"label"`
	TargetType string      `json:"target_type"`
	SyncToNodes []uuid.UUID `json:"sync_to_nodes"`

	RecoveryDays int `json:"recovery_days"`

	// posix
	PosixPath string `json:"posix_path"`

	// s3
	S3Bucket   string `json:"s3_bucket"`
	S3Region   string `json:"s3_region"`
	S3Endpoint string `json:"s3_endpoint"`
	S3KeyID    string `json:"s3_key_id"`
	S3Secret   string `json:"s3_secret"`

	// gcs
	GCSBucket string `json:"gcs_bucket"`
	GCSKey    string `json:"gcs_key"`

	// azure
	AzureAccount   string `json:"azure_account"`
	AzureContainer string `json:"azure_container"`
	AzureKey       string `json:"azure_key"`

	// sftp
	SFTPHost       string `json:"sftp_host"`
	SFTPPort       int    `json:"sftp_port"`
	SFTPUser       string `json:"sftp_user"`
	SFTPPrivateKey string `json:"sftp_private_key"`
	SFTPPath       string `json:"sftp_path"`
}

func encryptTargetCredentials(req backupTargetRequest, p *queries.CreateBackupTargetParams, enc *crypto.Encryptor) error {
	strPtr := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	intPtr := func(n int) *int {
		if n == 0 {
			return nil
		}
		return &n
	}
	encPtr := func(s string) (*string, error) {
		if s == "" {
			return nil, nil
		}
		raw, err := enc.Encrypt([]byte(s))
		if err != nil {
			return nil, err
		}
		encoded := base64.StdEncoding.EncodeToString(raw)
		return &encoded, nil
	}

	p.PosixPath = strPtr(req.PosixPath)
	p.S3Bucket = strPtr(req.S3Bucket)
	p.S3Region = strPtr(req.S3Region)
	p.S3Endpoint = strPtr(req.S3Endpoint)
	p.GCSBucket = strPtr(req.GCSBucket)
	p.AzureAccount = strPtr(req.AzureAccount)
	p.AzureContainer = strPtr(req.AzureContainer)
	p.SFTPHost = strPtr(req.SFTPHost)
	p.SFTPPort = intPtr(req.SFTPPort)
	p.SFTPUser = strPtr(req.SFTPUser)
	p.SFTPPath = strPtr(req.SFTPPath)

	var err error
	if p.S3KeyIDEnc, err = encPtr(req.S3KeyID); err != nil {
		return err
	}
	if p.S3SecretEnc, err = encPtr(req.S3Secret); err != nil {
		return err
	}
	if p.GCSKeyEnc, err = encPtr(req.GCSKey); err != nil {
		return err
	}
	if p.AzureKeyEnc, err = encPtr(req.AzureKey); err != nil {
		return err
	}
	if p.SFTPPrivateKeyEnc, err = encPtr(req.SFTPPrivateKey); err != nil {
		return err
	}
	return nil
}

// ─────────────────────────────────────────────────────────────
// serializers — strip encrypted fields from API responses
// ─────────────────────────────────────────────────────────────

func serializeTarget(t *queries.BackupTarget) map[string]any {
	m := map[string]any{
		"id":                 t.ID.String(),
		"cluster_id":         t.ClusterID.String(),
		"repo_index":         t.RepoIndex,
		"label":              t.Label,
		"target_type":        t.TargetType,
		"recovery_days": t.RecoveryDays,
		"sync_to_nodes":      uuidsToStrings(t.SyncToNodes),
		"created_at":         t.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":         t.UpdatedAt.UTC().Format(time.RFC3339),
	}
	switch t.TargetType {
	case "posix":
		m["posix_path"] = strVal(t.PosixPath)
	case "s3":
		m["s3_bucket"] = strVal(t.S3Bucket)
		m["s3_region"] = strVal(t.S3Region)
		m["s3_endpoint"] = strVal(t.S3Endpoint)
		m["s3_key_id_set"] = t.S3KeyIDEnc != nil
		m["s3_secret_set"] = t.S3SecretEnc != nil
	case "gcs":
		m["gcs_bucket"] = strVal(t.GCSBucket)
		m["gcs_key_set"] = t.GCSKeyEnc != nil
	case "azure":
		m["azure_account"] = strVal(t.AzureAccount)
		m["azure_container"] = strVal(t.AzureContainer)
		m["azure_key_set"] = t.AzureKeyEnc != nil
	case "sftp":
		m["sftp_host"] = strVal(t.SFTPHost)
		m["sftp_port"] = intVal(t.SFTPPort)
		m["sftp_user"] = strVal(t.SFTPUser)
		m["sftp_path"] = strVal(t.SFTPPath)
		m["sftp_private_key_set"] = t.SFTPPrivateKeyEnc != nil
	}
	return m
}

func serializeTargets(targets []queries.BackupTarget) []map[string]any {
	out := make([]map[string]any, 0, len(targets))
	for i := range targets {
		out = append(out, serializeTarget(&targets[i]))
	}
	return out
}

func serializeSchedule(s *queries.BackupSchedule) any {
	if s == nil {
		return nil
	}
	m := map[string]any{
		"cluster_id":               s.ClusterID.String(),
		"enabled":                  s.Enabled,
		"full_backup_cron":         s.FullBackupCron,
		"diff_backup_cron":         s.DiffBackupCron,
		"incr_backup_interval_hrs": s.IncrBackupIntervalHrs,
		"stanza_initialized":       s.StanzaInitialized,
		"first_backup_run":         s.FirstBackupRun,
		"restore_in_progress":      s.RestoreInProgress,
		"updated_at":               s.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if s.StanzaName != nil {
		m["stanza_name"] = *s.StanzaName
	}
	return m
}

func serializeCatalog(c *queries.BackupCatalogCache) any {
	if c == nil {
		return nil
	}
	m := map[string]any{
		"id":         c.ID.String(),
		"cluster_id": c.ClusterID.String(),
		"fetched_at": c.FetchedAt.UTC().Format(time.RFC3339),
	}
	if c.OldestRestorePoint != nil {
		m["oldest_restore_point"] = c.OldestRestorePoint.UTC().Format(time.RFC3339)
	}
	if c.NewestRestorePoint != nil {
		m["newest_restore_point"] = c.NewestRestorePoint.UTC().Format(time.RFC3339)
	}
	_, _, backups := parsePGBackRestInfo(string(c.CatalogJSON))
	type backupEntry struct {
		Type       string `json:"type"`
		Label      string `json:"label"`
		StartedAt  string `json:"started_at"`
		FinishedAt string `json:"finished_at"`
	}
	out := make([]backupEntry, 0, len(backups))
	for _, b := range backups {
		out = append(out, backupEntry{
			Type:       b.Type,
			Label:      b.Label,
			StartedAt:  time.Unix(b.Timestamp.Start, 0).UTC().Format(time.RFC3339),
			FinishedAt: time.Unix(b.Timestamp.Stop, 0).UTC().Format(time.RFC3339),
		})
	}
	m["backups"] = out
	return m
}

func serializeRuns(runs []queries.BackupRun) []map[string]any {
	out := make([]map[string]any, 0, len(runs))
	for _, r := range runs {
		m := map[string]any{
			"id":           r.ID.String(),
			"cluster_id":   r.ClusterID.String(),
			"backup_type":  r.BackupType,
			"attempt":      r.Attempt,
			"status":       r.Status,
			"scheduled_at": r.ScheduledAt.UTC().Format(time.RFC3339),
		}
		if r.TaskID != nil {
			m["task_id"] = r.TaskID.String()
		}
		if r.DispatchedAt != nil {
			m["dispatched_at"] = r.DispatchedAt.UTC().Format(time.RFC3339)
		}
		if r.CompletedAt != nil {
			m["completed_at"] = r.CompletedAt.UTC().Format(time.RFC3339)
		}
		if r.RetryAfter != nil {
			m["retry_after"] = r.RetryAfter.UTC().Format(time.RFC3339)
		}
		if r.ErrorMessage != nil {
			m["error_message"] = *r.ErrorMessage
		}
		out = append(out, m)
	}
	return out
}

// pollTaskSuccess polls the DB every 3 seconds until the task reaches a terminal
// state (success/failure/timeout), returning true only on success. This avoids
// the WaitForTask race condition where the task completes before the waiter registers.
func pollTaskSuccess(ctx context.Context, q *queries.TaskResultQuerier, taskID uuid.UUID, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		t, err := q.GetByTaskID(ctx, taskID)
		if err == nil {
			switch t.Status {
			case "success":
				return true
			case "failure", "timeout":
				return false
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(3 * time.Second):
		}
	}
	return false
}

func strVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func intVal(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}

func uuidsToStrings(ids []uuid.UUID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}
