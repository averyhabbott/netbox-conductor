package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
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

// Default backup schedules. Sunday 1am for the full backup; the rest of the
// week 1am for differentials. Centralized here so the "create defaults" and
// "fill in missing field" paths can't drift apart.
const (
	DefaultFullBackupCron = "0 1 * * 0"
	DefaultDiffBackupCron = "0 1 * * 1-6"
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
	snapshots   *queries.PatroniSnapshotQuerier
	designs     *queries.PatroniDesignQuerier
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
	snapshots *queries.PatroniSnapshotQuerier,
	designs *queries.PatroniDesignQuerier,
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
		snapshots:   snapshots,
		designs:     designs,
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
		req.FullBackupCron = DefaultFullBackupCron
	}
	if req.DiffBackupCron == "" {
		req.DiffBackupCron = DefaultDiffBackupCron
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
// If the deleted target was the last one, archiving is automatically disabled in the DCS.
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

	// If no targets remain, disable archiving in the background.
	remaining, _ := h.targets.ListByCluster(ctx, clusterID)
	if len(remaining) == 0 {
		nodes, _ := h.nodes.ListByCluster(ctx, clusterID)
		if primary := primaryDBNode(nodes); primary != nil {
			capturedPrimary := *primary
			capturedSnapshots := h.snapshots
			go func() {
				bgCtx := context.Background()
				h.disableArchiving(bgCtx, clusterID, capturedPrimary, capturedSnapshots)
			}()
		}
	}

	return c.JSON(http.StatusOK, map[string]any{"deleted": tid.String()})
}

// DisableBackups removes archive settings from the Patroni DCS config and clears
// the stanza_initialized flag. Does not restart PostgreSQL — archive_mode=off takes
// effect on the next Patroni-managed restart.
// POST /api/v1/clusters/:id/backup-config/disable
func (h *BackupHandler) DisableBackups(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}
	ctx := c.Request().Context()

	nodes, err := h.nodes.ListByCluster(ctx, clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list nodes")
	}
	primary := primaryDBNode(nodes)
	if primary == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "no connected primary node found")
	}

	if err := h.disableArchiving(ctx, clusterID, *primary, h.snapshots); err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "failed to disable archiving: "+err.Error())
	}

	return c.JSON(http.StatusOK, map[string]any{"cluster_id": clusterID.String(), "status": "archiving disabled"})
}

// disableArchiving snapshots the current Patroni config, then PATCHes archive_mode and
// archive_command to null (removes them from DCS). Also clears stanza_initialized.
func (h *BackupHandler) disableArchiving(ctx context.Context, clusterID uuid.UUID, primary queries.Node, snaps *queries.PatroniSnapshotQuerier) error {
	restUser, restPass := CredDefaultUserPatroniREST, ""
	if cred, err := h.creds.GetByKind(ctx, clusterID, CredKindPatroniREST); err == nil {
		restUser = cred.Username
		if pw, e := h.enc.Decrypt(cred.PasswordEnc); e == nil {
			restPass = string(pw)
		}
	}
	primaryIP := stripCIDR(primary.IPAddress)

	disablePatch, _ := json.Marshal(map[string]any{
		"postgresql": map[string]any{
			"parameters": map[string]any{
				"archive_mode":    nil,
				"archive_command": nil,
			},
		},
	})
	patchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, status, err := patroniDCSChange(patchCtx, patroniDCSChangeOptions{
		snapshots: snaps,
		tasks:     h.taskResults,
		clusterID: clusterID,
		nodeID:    primary.ID,
		primaryIP: primaryIP,
		restUser:  restUser,
		restPass:  restPass,
		patchBody: disablePatch,
		source:    "disable-archiving",
	})
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("PATCH /config returned HTTP %d", status)
	}

	slog.Info("disable-archiving: archive settings removed from DCS", "node", primary.Hostname)
	_ = h.schedules.ClearStanzaInitialized(ctx, clusterID)
	return nil
}

// EnableBackups runs the full backup bootstrap sequence for a cluster:
//  1. Push pgbackrest.conf to all DB nodes.
//  2. Background: PATCH Patroni DCS config with archive settings → restart PostgreSQL
//     if archive_mode not already active → dispatch stanza-create if not yet initialized.
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
	if existing, _ := h.schedules.Get(ctx, clusterID); existing == nil {
		if _, err := h.schedules.Upsert(ctx, queries.UpsertBackupScheduleParams{
			ClusterID:             clusterID,
			Enabled:               true,
			FullBackupCron:        DefaultFullBackupCron,
			DiffBackupCron:        DefaultDiffBackupCron,
			IncrBackupIntervalHrs: 1,
		}); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to initialize backup schedule: "+err.Error())
		}
	}

	// Fetch Patroni REST credentials.
	restUser, restPass := "patroni", ""
	if cred, err := h.creds.GetByKind(ctx, clusterID, "patroni_rest_password"); err == nil {
		restUser = cred.Username
		if pw, e := h.enc.Decrypt(cred.PasswordEnc); e == nil {
			restPass = string(pw)
		}
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

	// Identify primary for background work.
	primaryNode := primaryDBNode(nodes)

	// Dispatch pgbackrest.configure to all DB nodes.
	pgbTasks := make([]taskRef, 0, len(dbNodes))
	for i := range dbNodes {
		n := &dbNodes[i]
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
	}

	if h.emitter != nil {
		h.emitter.Emit(events.New(events.CategoryConfig, events.SeverityInfo, events.CodePatroniConfigured,
			"Backup configuration pushed to cluster nodes — activating WAL archiving",
			actorFromCtx(c)).Cluster(clusterID).Build())
	}

	// Background: pause Patroni, confirm/enable archive_mode on all nodes, resume, run stanza-create.
	if primaryNode != nil {
		capturedPrimary := *primaryNode
		capturedSnapshots := h.snapshots
		capturedDesigns := h.designs
		capturedEmitter := h.emitter

		// Collect replica nodes (DB nodes that are not the primary).
		capturedReplicas := make([]queries.Node, 0)
		for _, n := range dbNodes {
			if n.ID != capturedPrimary.ID {
				capturedReplicas = append(capturedReplicas, n)
			}
		}

		go func() {
			bgCtx := context.Background()
			primaryIP := stripCIDR(capturedPrimary.IPAddress)

			emit := func(severity, code, msg string) {
				if capturedEmitter != nil {
					capturedEmitter.Emit(events.New(events.CategoryConfig, severity, code, msg,
						events.ActorSystem).Cluster(clusterID).Build())
				}
			}

			// 1. Snapshot current config before any changes.
			if capturedSnapshots != nil {
				snapshotPatroniConfig(bgCtx, capturedSnapshots, clusterID, primaryIP, restUser, restPass, "configure-backups")
			}

			archivePatch, _ := json.Marshal(map[string]any{
				"postgresql": map[string]any{
					"parameters": map[string]any{
						"archive_mode":    "on",
						"archive_command": "pgbackrest --stanza=" + stanzaName + " archive-push %p",
					},
				},
			})

			// 2. Pause Patroni on the cluster to suppress automatic failover during restarts.
			pauseCtx, pauseCancel := context.WithTimeout(bgCtx, 10*time.Second)
			if _, pauseStatus, pauseErr := patroniREST(pauseCtx, http.MethodPost, primaryIP,
				"/pause", restUser, restPass, nil); pauseErr != nil || pauseStatus >= 300 {
				slog.Warn("enable-backups: Patroni pause failed — continuing anyway",
					"status", pauseStatus, "err", pauseErr)
			} else {
				slog.Info("enable-backups: Patroni paused")
			}
			pauseCancel()

			// Always resume Patroni when the goroutine exits.
			defer func() {
				resumeCtx, resumeCancel := context.WithTimeout(bgCtx, 10*time.Second)
				if _, resumeStatus, resumeErr := patroniREST(resumeCtx, http.MethodPost, primaryIP,
					"/resume", restUser, restPass, nil); resumeErr != nil || resumeStatus >= 300 {
					slog.Warn("enable-backups: Patroni resume failed",
						"status", resumeStatus, "err", resumeErr)
				} else {
					slog.Info("enable-backups: Patroni resumed")
				}
				resumeCancel()
			}()

			// archiveOK checks whether a node has archive_mode active in the DCS and no
			// pending PostgreSQL restart. For replicas, pass checkDCS=false (DCS propagates
			// automatically; only pending_restart matters).
			expectedArchiveCommand := "pgbackrest --stanza=" + stanzaName + " archive-push %p"
			archiveOK := func(nodeIP string, checkDCS bool) bool {
				if checkDCS {
					cfgCtx, cfgCancel := context.WithTimeout(bgCtx, 10*time.Second)
					body, status, err := patroniREST(cfgCtx, http.MethodGet, nodeIP, "/config", restUser, restPass, nil)
					cfgCancel()
					if err != nil || status != http.StatusOK {
						return false
					}
					var cfg struct {
						Postgresql struct {
							Parameters map[string]any `json:"parameters"`
						} `json:"postgresql"`
					}
					if err := json.Unmarshal(body, &cfg); err != nil {
						slog.Warn("enable-backups: unparseable /config response from primary — treating as not-ready",
							"node", nodeIP, "err", err)
						return false
					}
					if v, ok := cfg.Postgresql.Parameters["archive_mode"]; !ok || v != "on" {
						return false
					}
					// archive_command must also match what we PATCHed in — if archive_mode
					// is "on" but the command is stale or missing, WAL files won't reach
					// the pgBackRest stanza and the next backup will fail with no warning.
					if v, ok := cfg.Postgresql.Parameters["archive_command"]; !ok || v != expectedArchiveCommand {
						slog.Warn("enable-backups: archive_command in DCS does not match expected value",
							"node", nodeIP, "got", v, "want", expectedArchiveCommand)
						return false
					}
				}
				healthCtx, healthCancel := context.WithTimeout(bgCtx, 10*time.Second)
				body, status, err := patroniREST(healthCtx, http.MethodGet, nodeIP, "/", restUser, restPass, nil)
				healthCancel()
				if err != nil || status != http.StatusOK {
					return false
				}
				var health struct {
					PendingRestart bool `json:"pending_restart"`
				}
				if err := json.Unmarshal(body, &health); err != nil {
					// If we can't parse the health response, we don't actually know
					// whether a restart is pending — refuse to declare success.
					slog.Warn("enable-backups: unparseable health response — treating as not-ready",
						"node", nodeIP, "err", err)
					return false
				}
				return !health.PendingRestart
			}

			// 3. Primary: confirm archive_mode in DCS and no pending restart.
			//    If not satisfied, PATCH DCS config and restart. Max 3 attempts.
			const maxAttempts = 3
			primaryActive := false
			for attempt := 0; attempt < maxAttempts; attempt++ {
				if archiveOK(primaryIP, true) {
					slog.Info("enable-backups: archive_mode confirmed active on primary",
						"node", capturedPrimary.Hostname, "attempt", attempt+1)
					primaryActive = true
					break
				}
				slog.Info("enable-backups: archive_mode not yet active on primary — patching and restarting",
					"node", capturedPrimary.Hostname, "attempt", attempt+1)

				patchCtx, patchCancel := context.WithTimeout(bgCtx, 30*time.Second)
				_, patchStatus, patchErr := patroniPATCHConfigAudited(patchCtx, h.taskResults, capturedPrimary.ID, primaryIP,
					restUser, restPass, archivePatch, "configure-backups")
				patchCancel()
				if patchErr != nil || patchStatus >= 300 {
					slog.Warn("enable-backups: PATCH /config failed",
						"node", capturedPrimary.Hostname, "attempt", attempt+1, "status", patchStatus, "err", patchErr)
					emit(events.SeverityWarn, events.CodeBackupsEnabled,
						"Failed to write archive settings to Patroni DCS on "+capturedPrimary.Hostname)
					return
				}

				restartCtx, restartCancel := context.WithTimeout(bgCtx, 3*time.Minute)
				_, restartStatus, restartErr := patroniREST(restartCtx, http.MethodPost, primaryIP,
					"/restart", restUser, restPass, []byte(`{}`))
				restartCancel()
				if restartErr != nil || restartStatus >= 300 {
					slog.Warn("enable-backups: postgres restart failed on primary",
						"node", capturedPrimary.Hostname, "attempt", attempt+1, "status", restartStatus, "err", restartErr)
					emit(events.SeverityWarn, events.CodeBackupsEnabled,
						"PostgreSQL restart failed on primary "+capturedPrimary.Hostname+" while enabling WAL archiving")
					return
				}
				slog.Info("enable-backups: postgres restarted on primary",
					"node", capturedPrimary.Hostname, "attempt", attempt+1)
			}
			if !primaryActive {
				slog.Warn("enable-backups: archive_mode not confirmed on primary after max attempts — aborting",
					"node", capturedPrimary.Hostname)
				emit(events.SeverityWarn, events.CodeBackupsEnabled,
					"WAL archiving could not be confirmed on primary "+capturedPrimary.Hostname+" after "+fmt.Sprintf("%d", maxAttempts)+" attempts")
				return
			}
			emit(events.SeverityInfo, events.CodeBackupsEnabled,
				"WAL archiving active on primary "+capturedPrimary.Hostname)

			// 4. Replicas: check pending_restart and restart if needed.
			for _, replica := range capturedReplicas {
				replicaIP := stripCIDR(replica.IPAddress)
				replicaActive := false
				for attempt := 0; attempt < maxAttempts; attempt++ {
					if archiveOK(replicaIP, false) {
						slog.Info("enable-backups: no pending restart on replica",
							"node", replica.Hostname, "attempt", attempt+1)
						replicaActive = true
						break
					}
					slog.Info("enable-backups: restarting replica to apply archive_mode",
						"node", replica.Hostname, "attempt", attempt+1)

					restartCtx, restartCancel := context.WithTimeout(bgCtx, 3*time.Minute)
					_, restartStatus, restartErr := patroniREST(restartCtx, http.MethodPost, replicaIP,
						"/restart", restUser, restPass, []byte(`{}`))
					restartCancel()
					if restartErr != nil || restartStatus >= 300 {
						slog.Warn("enable-backups: postgres restart failed on replica",
							"node", replica.Hostname, "attempt", attempt+1, "status", restartStatus, "err", restartErr)
						emit(events.SeverityWarn, events.CodeBackupsEnabled,
							"PostgreSQL restart failed on replica "+replica.Hostname+" while applying archive_mode")
						break
					}
					slog.Info("enable-backups: postgres restarted on replica",
						"node", replica.Hostname, "attempt", attempt+1)
				}
				if replicaActive {
					emit(events.SeverityInfo, events.CodeBackupsEnabled,
						"WAL archiving applied to replica "+replica.Hostname)
				} else {
					slog.Warn("enable-backups: archive_mode not confirmed on replica after max attempts",
						"node", replica.Hostname)
					emit(events.SeverityWarn, events.CodeBackupsEnabled,
						"WAL archiving could not be confirmed on replica "+replica.Hostname+" after "+fmt.Sprintf("%d", maxAttempts)+" attempts — backups may be incomplete from this node")
				}
			}

			// Record design intent and post-change snapshot after all nodes confirm archive_mode.
			if capturedDesigns != nil {
				if err := capturedDesigns.Upsert(bgCtx, clusterID, archivePatch); err != nil {
					slog.Warn("enable-backups: failed to store design", "cluster", clusterID, "err", err)
				}
			}
			if capturedSnapshots != nil {
				recordPostChangeSnapshot(bgCtx, capturedSnapshots, clusterID, primaryIP, restUser, restPass, "configure-backups")
			}

			// 5. Skip stanza-create if already initialized.
			if sched, err := h.schedules.Get(bgCtx, clusterID); err == nil && sched.StanzaInitialized {
				slog.Info("enable-backups: stanza already initialized, skipping stanza-create",
					"node", capturedPrimary.Hostname)
				return
			}

			// 6. Dispatch stanza-create to the confirmed primary.
			stanzaParams, _ := json.Marshal(protocol.PGBackRestStanzaCreateParams{Stanza: stanzaName})
			stanzaTaskID := uuid.New()
			if createErr := h.taskResults.Create(bgCtx, capturedPrimary.ID, stanzaTaskID, string(protocol.TaskPGBackRestStanzaCreate), stanzaParams); createErr != nil {
				slog.Error("enable-backups: stanza-create task record create failed — aborting",
					"node", capturedPrimary.Hostname, "error", createErr)
				emit(events.SeverityWarn, events.CodeBackupsEnabled,
					"pgBackRest stanza-create could not be recorded for "+capturedPrimary.Hostname+" — aborting")
				return
			}
			if dispErr := h.dispatcher.Dispatch(capturedPrimary.ID, protocol.TaskDispatchPayload{
				TaskID:      stanzaTaskID.String(),
				TaskType:    protocol.TaskPGBackRestStanzaCreate,
				Params:      json.RawMessage(stanzaParams),
				TimeoutSecs: 120,
			}); dispErr != nil {
				slog.Warn("enable-backups: stanza-create dispatch failed",
					"node", capturedPrimary.Hostname, "error", dispErr)
				emit(events.SeverityWarn, events.CodeBackupsEnabled,
					"pgBackRest stanza-create dispatch failed on "+capturedPrimary.Hostname)
				return
			}
			if sentErr := h.taskResults.SetSent(bgCtx, stanzaTaskID); sentErr != nil {
				slog.Warn("enable-backups: stanza-create SetSent failed",
					"node", capturedPrimary.Hostname, "task", stanzaTaskID, "error", sentErr)
			}

			if ok := pollTaskSuccess(bgCtx, h.taskResults, stanzaTaskID, 5*time.Minute); !ok {
				emit(events.SeverityWarn, events.CodeBackupsEnabled,
					"pgBackRest stanza-create failed on "+capturedPrimary.Hostname)
				return
			}
			_ = h.schedules.SetStanzaInitialized(bgCtx, clusterID, stanzaName)
			emit(events.SeverityInfo, events.CodeBackupsEnabled,
				"pgBackRest stanza '"+stanzaName+"' created — backups enabled")
		}()
	}

	return c.JSON(http.StatusAccepted, map[string]any{
		"cluster_id": clusterID.String(),
		"pgbackrest": pgbTasks,
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

	stanzaStr := ""
	if schedule.StanzaName != nil {
		stanzaStr = *schedule.StanzaName
	}

	// Fetch Patroni REST credentials once — used for pause, resume, and reinitialize.
	var restUser, restPass string
	if cred, credErr := h.creds.GetByKind(ctx, clusterID, "patroni_rest_password"); credErr == nil {
		restUser = cred.Username
		if pw, decErr := h.enc.Decrypt(cred.PasswordEnc); decErr == nil {
			restPass = string(pw)
		}
	}

	// Step 1: Pause Patroni automation via REST API.
	// The pause flag propagates via DCS to all cluster members automatically.
	restoreIP := stripCIDR(restoreNode.IPAddress)
	if _, status, err := patroniPATCHConfigAudited(ctx, h.taskResults, restoreNode.ID, restoreIP, restUser, restPass, []byte(`{"pause": true}`), "restore-pause"); err != nil || status >= 300 {
		_ = h.schedules.SetRestoreInProgress(ctx, clusterID, false)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, "patroni pause failed: "+err.Error())
		}
		return echo.NewHTTPError(http.StatusBadGateway, fmt.Sprintf("patroni pause returned HTTP %d", status))
	}

	type taskRef struct {
		NodeID   string `json:"node_id"`
		Hostname string `json:"hostname"`
		TaskID   string `json:"task_id,omitempty"`
		Status   string `json:"status"`
		Error    string `json:"error,omitempty"`
	}

	// Step 2: Stop the PostgreSQL process on each replica.
	// Patroni is paused so it will not restart them. The restore task handles the local stop.
	emptyParams := json.RawMessage(`{}`)
	stopTasks := make([]taskRef, 0)
	stopIDs := make([]uuid.UUID, 0)
	for _, n := range dbNodes {
		if n.ID == restoreNode.ID {
			continue
		}
		taskID := uuid.New()
		_ = h.taskResults.Create(ctx, n.ID, taskID, string(protocol.TaskStopPostgres), emptyParams)
		if dispErr := h.dispatcher.Dispatch(n.ID, protocol.TaskDispatchPayload{
			TaskID:      taskID.String(),
			TaskType:    protocol.TaskStopPostgres,
			Params:      emptyParams,
			TimeoutSecs: 30,
		}); dispErr != nil {
			stopTasks = append(stopTasks, taskRef{NodeID: n.ID.String(), Hostname: n.Hostname, Status: "offline", Error: dispErr.Error()})
			continue
		}
		_ = h.taskResults.SetSent(ctx, taskID)
		stopTasks = append(stopTasks, taskRef{NodeID: n.ID.String(), Hostname: n.Hostname, TaskID: taskID.String(), Status: "dispatched"})
		stopIDs = append(stopIDs, taskID)
	}

	// Step 3: Dispatch restore to the restore node.
	// RunPGBackRestRestore stops local postgres, runs pgbackrest, starts postgres, and waits for promotion.
	restoreParams, _ := json.Marshal(protocol.PGBackRestRestoreParams{
		Stanza:     stanzaStr,
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

		// Capture state for background goroutine before returning 202.
		capturedStopIDs := stopIDs
		capturedRestoreID := restoreTaskID
		capturedRestoreNode := *restoreNode
		capturedRestoreIP := restoreIP
		capturedReplicas := make([]queries.Node, 0, len(dbNodes)-1)
		for _, n := range dbNodes {
			if n.ID != restoreNode.ID {
				capturedReplicas = append(capturedReplicas, n)
			}
		}

		go func() {
			bgCtx := context.Background()

			// Wait for each replica's postgres stop.
			for _, tid := range capturedStopIDs {
				slog.Info("restore goroutine: waiting for postgres stop", "task_id", tid)
				ok := pollTaskSuccess(bgCtx, h.taskResults, tid, 2*time.Minute)
				slog.Info("restore goroutine: postgres stop result", "task_id", tid, "success", ok)
			}

			// Wait for restore + promotion (up to 30 min).
			slog.Info("restore goroutine: waiting for pgbackrest.restore task", "task_id", capturedRestoreID)
			if ok := pollTaskSuccess(bgCtx, h.taskResults, capturedRestoreID, 30*time.Minute); !ok {
				slog.Error("restore goroutine: pgbackrest.restore task did not succeed", "task_id", capturedRestoreID)
				_ = h.schedules.SetRestoreInProgress(bgCtx, clusterID, false)
				return
			}
			slog.Info("restore goroutine: pgbackrest.restore succeeded, waiting for Patroni to acquire DCS lock")

			// Poll primary GET / until Patroni holds the DCS lock.
			// The agent confirms PostgreSQL promoted but not that Patroni reconnected —
			// reinitialize returns "Cluster has no leader" if sent before the lock is held.
			primaryPollCtx, primaryPollCancel := context.WithTimeout(bgCtx, 2*time.Minute)
			for {
				_, primaryStatus, primaryErr := patroniREST(primaryPollCtx, http.MethodGet, capturedRestoreIP, "/", restUser, restPass, nil)
				if primaryErr == nil && primaryStatus == http.StatusOK {
					slog.Info("restore goroutine: primary Patroni ready, DCS lock held", "node", capturedRestoreNode.Hostname)
					break
				}
				select {
				case <-primaryPollCtx.Done():
					primaryPollCancel()
					slog.Error("restore goroutine: timed out waiting for primary Patroni DCS lock", "node", capturedRestoreNode.Hostname)
					_ = h.schedules.SetRestoreInProgress(bgCtx, clusterID, false)
					return
				case <-time.After(5 * time.Second):
				}
			}
			primaryPollCancel()

			// Reinitialize each replica while Patroni is still paused.
			// The paused HA loop cannot race us with automatic pg_rewind — pg_rewind
			// always fails after a restore (no common ancestor) and leaves replicas stuck.
			for _, replica := range capturedReplicas {
				replicaIP := stripCIDR(replica.IPAddress)
				body, status, err := patroniREST(bgCtx, http.MethodPost, replicaIP, "/reinitialize", restUser, restPass, []byte(`{}`))
				slog.Info("restore goroutine: patroni reinitialize", "node", replica.Hostname, "status", status, "body", string(body), "err", err)
				if err != nil {
					slog.Error("patroni reinitialize failed", "node", replica.Hostname, "error", err)
				}
			}

			// Resume Patroni — HA loop takes over, replicas clone from primary via pg_basebackup.
			body, status, err := patroniPATCHConfigAudited(bgCtx, h.taskResults, capturedRestoreNode.ID, capturedRestoreIP, restUser, restPass, []byte(`{"pause": null}`), "restore-resume")
			slog.Info("restore goroutine: patroni resume", "node", capturedRestoreNode.Hostname, "status", status, "body", string(body), "err", err)
			if err != nil {
				slog.Error("patroni resume failed", "node", capturedRestoreNode.Hostname, "error", err)
			}

			// Poll each replica GET /replica until running as replica.
			for _, replica := range capturedReplicas {
				replicaIP := stripCIDR(replica.IPAddress)
				slog.Info("restore goroutine: waiting for replica to rejoin", "node", replica.Hostname)
				replicaPollCtx, replicaPollCancel := context.WithTimeout(bgCtx, 5*time.Minute)
				for {
					_, replicaStatus, replicaErr := patroniREST(replicaPollCtx, http.MethodGet, replicaIP, "/replica", restUser, restPass, nil)
					if replicaErr == nil && replicaStatus == http.StatusOK {
						slog.Info("restore goroutine: replica rejoined", "node", replica.Hostname)
						break
					}
					select {
					case <-replicaPollCtx.Done():
						slog.Warn("restore goroutine: timed out waiting for replica to rejoin", "node", replica.Hostname)
						goto nextReplica
					case <-time.After(5 * time.Second):
					}
				}
			nextReplica:
				replicaPollCancel()
			}

			slog.Info("restore goroutine: complete, clearing restore_in_progress")
			_ = h.schedules.SetRestoreInProgress(bgCtx, clusterID, false)
		}()
	}

	if h.emitter != nil {
		h.emitter.Emit(events.New(events.CategoryHA, events.SeverityWarn, events.CodeFailoverInitiated,
			fmt.Sprintf("Database restore initiated to %s", req.TargetTime),
			actorFromCtx(c)).Cluster(clusterID).Build())
	}

	return c.JSON(http.StatusAccepted, map[string]any{
		"cluster_id":   clusterID.String(),
		"target_time":  req.TargetTime,
		"restore_node": restoreTask,
		"stop_tasks":   stopTasks,
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
