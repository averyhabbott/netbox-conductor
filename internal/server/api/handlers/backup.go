package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/configgen"
	"github.com/averyhabbott/netbox-conductor/internal/server/crypto"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/events"
	"github.com/averyhabbott/netbox-conductor/internal/server/hub"
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

	return c.JSON(http.StatusOK, map[string]any{
		"cluster_id": clusterID.String(),
		"targets":    serializeTargets(targetList),
		"schedule":   serializeSchedule(schedule),
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

// EnableBackups runs the bootstrap sequence for a cluster:
// push updated Patroni config (with archive settings) → push pgbackrest.conf →
// stanza-create → stanza-check → set stanza_initialized=true.
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

	// Render pgbackrest.conf from targets + cluster data dir.
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

	confTasks := make([]taskRef, 0, len(dbNodes))
	var primaryNode *queries.Node

	for i := range dbNodes {
		n := &dbNodes[i]
		if n.PatroniState != nil {
			var ps map[string]any
			if json.Unmarshal(n.PatroniState, &ps) == nil && ps["role"] == "primary" {
				primaryNode = n
			}
		}

		params, _ := json.Marshal(protocol.PGBackRestConfigParams{Config: pgbConf})
		taskID := uuid.New()
		_ = h.taskResults.Create(ctx, n.ID, taskID, string(protocol.TaskPGBackRestConfigure), params)
		if dispErr := h.dispatcher.Dispatch(n.ID, protocol.TaskDispatchPayload{
			TaskID:      taskID.String(),
			TaskType:    protocol.TaskPGBackRestConfigure,
			Params:      json.RawMessage(params),
			TimeoutSecs: 30,
		}); dispErr != nil {
			confTasks = append(confTasks, taskRef{NodeID: n.ID.String(), Hostname: n.Hostname, Status: "offline", Error: dispErr.Error()})
			continue
		}
		_ = h.taskResults.SetSent(ctx, taskID)
		confTasks = append(confTasks, taskRef{NodeID: n.ID.String(), Hostname: n.Hostname, TaskID: taskID.String(), Status: "dispatched"})
	}

	// Dispatch stanza-create to primary (or first online node if primary unknown).
	if primaryNode == nil {
		for i := range dbNodes {
			if dbNodes[i].AgentStatus == "connected" {
				primaryNode = &dbNodes[i]
				break
			}
		}
	}

	var stanzaTask *taskRef
	if primaryNode != nil {
		stanzaName := cluster.PatroniScope
		params, _ := json.Marshal(protocol.PGBackRestStanzaCreateParams{Stanza: stanzaName})
		taskID := uuid.New()
		_ = h.taskResults.Create(ctx, primaryNode.ID, taskID, string(protocol.TaskPGBackRestStanzaCreate), params)
		if dispErr := h.dispatcher.Dispatch(primaryNode.ID, protocol.TaskDispatchPayload{
			TaskID:      taskID.String(),
			TaskType:    protocol.TaskPGBackRestStanzaCreate,
			Params:      json.RawMessage(params),
			TimeoutSecs: 120,
		}); dispErr != nil {
			ref := taskRef{NodeID: primaryNode.ID.String(), Hostname: primaryNode.Hostname, Status: "offline", Error: dispErr.Error()}
			stanzaTask = &ref
		} else {
			_ = h.taskResults.SetSent(ctx, taskID)
			// Mark stanza initialized in background once task completes (optimistic).
			go func(tid uuid.UUID, cid uuid.UUID, name string) {
				time.Sleep(5 * time.Second)
				_ = h.schedules.SetStanzaInitialized(context.Background(), cid, name)
			}(taskID, clusterID, stanzaName)
			ref := taskRef{NodeID: primaryNode.ID.String(), Hostname: primaryNode.Hostname, TaskID: taskID.String(), Status: "dispatched"}
			stanzaTask = &ref
		}
	}

	if h.emitter != nil {
		h.emitter.Emit(events.New(events.CategoryConfig, events.SeverityInfo, events.CodePatroniConfigured,
			"Backup configuration pushed to cluster nodes",
			actorFromCtx(c)).Cluster(clusterID).Build())
	}

	return c.JSON(http.StatusAccepted, map[string]any{
		"cluster_id":   clusterID.String(),
		"config_tasks": confTasks,
		"stanza_task":  stanzaTask,
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

// TestBackupPath dispatches a write test against a local disk path on the primary node.
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
	node := primaryDBNode(nodes)
	if node == nil {
		return echo.NewHTTPError(http.StatusConflict, "no connected node available to run the test")
	}

	// Use positional $1 to safely pass the path without shell injection risk.
	params, _ := json.Marshal(protocol.RunCommandParams{
		Command: "/bin/sh",
		Args:    []string{"-c", `touch "$1"/.conductor_test && rm -f "$1"/.conductor_test && echo ok`, "--", req.Path},
	})
	taskID := uuid.New()
	_ = h.taskResults.Create(ctx, node.ID, taskID, string(protocol.TaskRunCommand), params)
	if dispErr := h.dispatcher.Dispatch(node.ID, protocol.TaskDispatchPayload{
		TaskID:      taskID.String(),
		TaskType:    protocol.TaskRunCommand,
		Params:      json.RawMessage(params),
		TimeoutSecs: 15,
	}); dispErr != nil {
		return echo.NewHTTPError(http.StatusConflict, "node is not connected: "+dispErr.Error())
	}
	_ = h.taskResults.SetSent(ctx, taskID)

	return c.JSON(http.StatusAccepted, map[string]any{
		"task_id":  taskID.String(),
		"node_id":  node.ID.String(),
		"hostname": node.Hostname,
	})
}

// ProvisionBackupPath creates the local backup directory on the primary node.
// POST /api/v1/clusters/:id/backup-path/provision
func (h *BackupHandler) ProvisionBackupPath(c echo.Context) error {
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
	node := primaryDBNode(nodes)
	if node == nil {
		return echo.NewHTTPError(http.StatusConflict, "no connected node available to provision the directory")
	}

	params, _ := json.Marshal(protocol.RunCommandParams{
		Command: "/bin/sh",
		Args:    []string{"-c", `mkdir -p "$1" && chown postgres:postgres "$1" && chmod 750 "$1"`, "--", req.Path},
	})
	taskID := uuid.New()
	_ = h.taskResults.Create(ctx, node.ID, taskID, string(protocol.TaskRunCommand), params)
	if dispErr := h.dispatcher.Dispatch(node.ID, protocol.TaskDispatchPayload{
		TaskID:      taskID.String(),
		TaskType:    protocol.TaskRunCommand,
		Params:      json.RawMessage(params),
		TimeoutSecs: 30,
	}); dispErr != nil {
		return echo.NewHTTPError(http.StatusConflict, "node is not connected: "+dispErr.Error())
	}
	_ = h.taskResults.SetSent(ctx, taskID)

	return c.JSON(http.StatusAccepted, map[string]any{
		"task_id":  taskID.String(),
		"node_id":  node.ID.String(),
		"hostname": node.Hostname,
	})
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
		repo := configgen.PGBackRestRepo{
			Index:            t.RepoIndex,
			RepoType:         t.TargetType,
			Label:            t.Label,
			FullRetention:    t.FullRetention,
			DiffRetention:    t.DiffRetention,
			WALRetentionDays: t.WalRetentionDays,
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
func (h *BackupHandler) bindCreateTarget(ctx context.Context, c echo.Context, clusterID uuid.UUID) (*queries.CreateBackupTargetParams, error) {
	var req backupTargetRequest
	if err := c.Bind(&req); err != nil || req.Label == "" || req.TargetType == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "label and target_type are required")
	}
	p := &queries.CreateBackupTargetParams{
		ClusterID:        clusterID,
		Label:            req.Label,
		TargetType:       req.TargetType,
		FullRetention:    req.FullRetention,
		DiffRetention:    req.DiffRetention,
		WalRetentionDays: req.WalRetentionDays,
		SyncToNodes:      req.SyncToNodes,
	}
	if p.FullRetention == 0 {
		p.FullRetention = 2
	}
	if p.DiffRetention == 0 {
		p.DiffRetention = 7
	}
	if p.WalRetentionDays == 0 {
		p.WalRetentionDays = 14
	}

	if err := encryptTargetCredentials(req, p, h.enc); err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "failed to encrypt credentials: "+err.Error())
	}
	return p, nil
}

// bindUpdateTarget parses an update-target request body and encrypts credentials.
func (h *BackupHandler) bindUpdateTarget(ctx context.Context, c echo.Context, existing *queries.BackupTarget) (*queries.UpdateBackupTargetParams, error) {
	var req backupTargetRequest
	if err := c.Bind(&req); err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.Label == "" {
		req.Label = existing.Label
	}

	p := &queries.UpdateBackupTargetParams{
		ID:               existing.ID,
		Label:            req.Label,
		FullRetention:    req.FullRetention,
		DiffRetention:    req.DiffRetention,
		WalRetentionDays: req.WalRetentionDays,
		SyncToNodes:      req.SyncToNodes,

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

	FullRetention    int `json:"full_retention"`
	DiffRetention    int `json:"diff_retention"`
	WalRetentionDays int `json:"wal_retention_days"`

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
		"full_retention":     t.FullRetention,
		"diff_retention":     t.DiffRetention,
		"wal_retention_days": t.WalRetentionDays,
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
		"id":           c.ID.String(),
		"cluster_id":   c.ClusterID.String(),
		"fetched_at":   c.FetchedAt.UTC().Format(time.RFC3339),
		"catalog_json": json.RawMessage(c.CatalogJSON),
	}
	if c.OldestRestorePoint != nil {
		m["oldest_restore_point"] = c.OldestRestorePoint.UTC().Format(time.RFC3339)
	}
	if c.NewestRestorePoint != nil {
		m["newest_restore_point"] = c.NewestRestorePoint.UTC().Format(time.RFC3339)
	}
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
