package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	agentconfig "github.com/averyhabbott/netbox-conductor/internal/agent/config"
	"github.com/averyhabbott/netbox-conductor/internal/agent/executor"
	"github.com/averyhabbott/netbox-conductor/internal/agent/statusserver"
	"github.com/averyhabbott/netbox-conductor/internal/agent/syncperm"
	"github.com/averyhabbott/netbox-conductor/internal/agent/ws"
	"github.com/averyhabbott/netbox-conductor/internal/server/logging"
	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
	"github.com/google/uuid"
)

const defaultEnvFile = "/etc/netbox-agent/netbox-agent.env"

// chunkSendTimeout is how long a sync push waits for outbound buffer space before
// failing the task; the buffer stays full while the server is slow or disconnected.
const chunkSendTimeout = 30 * time.Second

func main() {
	// The repo helper runs as postgres (via sudo), which can't read the agent's
	// .env, so it runs before the config is loaded.
	if len(os.Args) > 1 && os.Args[1] == "repo" {
		if err := executor.RepoHelper(os.Args[2:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	envFile := os.Getenv("AGENT_ENV_FILE")
	if envFile == "" {
		envFile = defaultEnvFile
	}

	cfg, err := agentconfig.Load(envFile)
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	// Subcommands — dispatch before the daemon starts.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "check-sync-permissions":
			if err := syncperm.Run(cfg); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\nUsage: netbox-agent [check-sync-permissions]\n", os.Args[1])
			os.Exit(1)
		}
	}

	if !cfg.IsRegistered() {
		log.Fatalf("agent is not registered: AGENT_NODE_ID and AGENT_TOKEN must be set in %s", envFile)
	}

	// Cert-learning: one-time download of the conductor's CA cert.
	if agentconfig.NeedsCert(cfg) {
		if err := agentconfig.LearnCert(cfg); err != nil {
			log.Printf("WARNING: cert-learning failed (%v) — continuing without pinned cert", err)
		} else {
			log.Printf("cert-learning succeeded; TLS CA cert saved to %s", cfg.TLSCACert)
		}
	}

	// Set up structured logging.
	// Writes to stderr (captured by journald) and also to the local syslog socket
	// so events appear in /var/log/messages (or equivalent) on the managed host.
	// Heartbeats are at Debug level and are suppressed at the default Info level.
	setupLogging(logging.ParseLevel(cfg.LogLevel))

	slog.Info("netbox-agent starting", "node", cfg.NodeID, "server", cfg.ServerURL)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Status server state — updated when the server delivers cluster config on connect.
	statusState := statusserver.NewState(cfg.PatroniRESTURL)

	// Status server — HTTP health endpoint for VIP / reverse-proxy health checks.
	if cfg.StatusAddr != "" {
		go statusserver.Serve(ctx, cfg.StatusAddr, cfg.NodeID, statusState)
	}

	executor.SetNetboxPaths(cfg.NetboxConfigPath, cfg.NetboxMediaRoot)

	// Metrics collector
	metrics := executor.NewMetricsCollector(cfg.NetboxConfigPath, cfg.NetboxMediaRoot, cfg.PatroniRESTURL)

	// Patroni role watcher (fires when role changes; sends a proactive patroni.state message)
	var wsClient *ws.Client // set after creation below
	roleWatcher := executor.NewPatroniRoleWatcher(metrics, func(newRole, prevRole string, stateJSON []byte) {
		slog.Info("patroni role change", "prev_role", prevRole, "role", newRole)
		if wsClient == nil {
			return
		}
		payload, _ := json.Marshal(protocol.PatroniStatePayload{
			NodeID:    cfg.NodeID,
			Role:      newRole,
			PrevRole:  prevRole,
			StateJSON: json.RawMessage(stateJSON),
		})
		wsClient.Send(protocol.Envelope{
			ID:      uuid.New().String(),
			Type:    protocol.TypePatroniState,
			Payload: json.RawMessage(payload),
		})
	})

	// Serial task queue — tasks execute one at a time, in the order received.
	// This is critical for Configure Failover where patroni.install must complete
	// before patroni.write_config and service.restart.patroni run.
	taskQueue := make(chan func(), 64)
	go func() {
		for fn := range taskQueue {
			fn()
		}
	}()

	// Incoming file-sync chunks are written one at a time, in arrival order: a
	// file is built by appending chunks, so concurrent writes would scramble it.
	// When the queue is full the read loop blocks, which slows the sender instead
	// of dropping chunks. A destination's sync tasks run here too, so they stay
	// in order with the chunks around them.
	chunkQueue := make(chan func(), 64)
	syncRecv := executor.NewSyncReceiver()
	go func() {
		for fn := range chunkQueue {
			fn()
		}
	}()

	// Message handler for inbound server commands
	onMessage := func(ctx context.Context, env protocol.Envelope) error {
		switch env.Type {
		case protocol.TypeTaskDispatch:
			handleTaskDispatch(ctx, cfg, wsClient, env, taskQueue, chunkQueue, syncRecv)
		case protocol.TypeSyncChunk:
			chunkQueue <- func() {
				var c protocol.SyncChunkPayload
				if err := json.Unmarshal(env.Payload, &c); err != nil {
					slog.Warn("malformed sync.chunk", "error", err)
					return
				}
				if err := syncRecv.Chunk(c); err != nil {
					slog.Warn("sync chunk", "sync_id", c.SyncID, "path", c.Path, "error", err)
				}
			}

		default:
			slog.Warn("unhandled server message type", "type", env.Type)
		}
		return nil
	}

	// WebSocket client
	client, err := ws.New(cfg, onMessage)
	if err != nil {
		slog.Error("creating WS client", "error", err)
		os.Exit(1)
	}
	wsClient = client

	// A receive the conductor can no longer finish is stopped, so no partial
	// file or repo helper is left behind.
	client.OnDisconnect = func() { chunkQueue <- syncRecv.StopAll }

	// Per-heartbeat service state tracking for fast TypeServiceStateChange emission.
	type svcState struct {
		netbox, rq, redis, sentinel, patroni, postgres *bool
	}
	var prevSvc svcState

	// Wire heartbeat function
	client.HeartbeatFn = func() (protocol.HeartbeatPayload, error) {
		hb, err := metrics.Collect()
		if err == nil {
			sendIfChanged := func(svc string, old *bool, cur bool) {
				if old == nil || *old == cur {
					return
				}
				p, _ := json.Marshal(protocol.ServiceStateChangePayload{
					NodeID:  cfg.NodeID,
					Service: svc,
					Running: cur,
				})
				wsClient.Send(protocol.Envelope{
					ID:      uuid.New().String(),
					Type:    protocol.TypeServiceStateChange,
					Payload: json.RawMessage(p),
				})
			}
			sendIfChanged("netbox", prevSvc.netbox, hb.NetboxRunning)
			sendIfChanged("rq", prevSvc.rq, hb.RQRunning)
			sendIfChanged("redis", prevSvc.redis, hb.RedisRunning)
			sendIfChanged("sentinel", prevSvc.sentinel, hb.SentinelRunning)
			sendIfChanged("patroni", prevSvc.patroni, hb.PatroniRunning)
			sendIfChanged("postgres", prevSvc.postgres, hb.PostgresRunning)

			nb, rq, rd, sn, pa, pg := hb.NetboxRunning, hb.RQRunning, hb.RedisRunning, hb.SentinelRunning, hb.PatroniRunning, hb.PostgresRunning
			prevSvc = svcState{netbox: &nb, rq: &rq, redis: &rd, sentinel: &sn, patroni: &pa, postgres: &pg}

			if cfg.PatroniRESTURL != "" && hb.PatroniRunning && hb.PatroniRole == "" {
				slog.Warn("patroni service is running but not connected to cluster")
			}
		}
		return hb, err
	}

	// Update status server state whenever the server delivers cluster config.
	client.OnServerHello = func(hello protocol.ServerHelloPayload) {
		slog.Info("cluster config received from server",
			"cluster_id", hello.ClusterID,
			"patroni_configured", hello.PatroniConfigured,
			"app_tier_always_available", hello.AppTierAlwaysAvailable,
		)
		statusState.Update(hello.PatroniConfigured, hello.AppTierAlwaysAvailable)
	}

	// Poll Patroni role every 10s (independent of heartbeat cadence)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				roleWatcher.Poll()
			}
		}
	}()

	// Tail NetBox application logs and forward to server.
	// Discovers log files from LOGGING section in configuration.py; falls back to
	// NETBOX_LOG_PATH if no file-based handlers are found.
	go func() {
		executor.TailNetboxLogs(ctx, cfg.NetboxConfigPath, cfg.NetboxLogPath, func(logName string, lines []string) {
			if wsClient == nil {
				return
			}
			payload, _ := json.Marshal(protocol.NetboxLogPayload{
				NodeID:  cfg.NodeID,
				LogName: logName,
				Lines:   lines,
			})
			wsClient.Send(protocol.Envelope{
				ID:      uuid.New().String(),
				Type:    protocol.TypeNetboxLog,
				Payload: json.RawMessage(payload),
			})
		})
	}()

	// Run WebSocket client (blocks until ctx cancelled)
	client.Run(ctx)
	slog.Info("agent stopped")
}

// setupLogging configures the default slog logger and routes the stdlib log
// package through it. Writes to stderr (journald) and the local syslog socket.
func setupLogging(level slog.Level) {
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(h))
	log.SetOutput(io.Discard)
}

// handleTaskDispatch acks an inbound task and queues it for execution.
func handleTaskDispatch(ctx context.Context, cfg *agentconfig.Config, client *ws.Client, env protocol.Envelope, taskQueue, chunkQueue chan<- func(), syncRecv *executor.SyncReceiver) {
	var task protocol.TaskDispatchPayload
	if err := json.Unmarshal(env.Payload, &task); err != nil {
		slog.Error("malformed task dispatch", "error", err)
		return
	}
	slog.Info("task received", "task_id", task.TaskID, "type", task.TaskType)

	// Send ack immediately
	ackPayload, _ := json.Marshal(protocol.TaskAckPayload{
		TaskID: task.TaskID,
		Status: "accepted",
	})
	client.StartTask(task.TaskID, protocol.Envelope{
		ID:      uuid.New().String(),
		Type:    protocol.TypeTaskAck,
		Payload: json.RawMessage(ackPayload),
	})

	switch task.TaskType {
	case protocol.TaskSyncReceive, protocol.TaskSyncFinish, protocol.TaskSyncDelete:
		chunkQueue <- func() { executeTask(ctx, cfg, client, task, syncRecv) }
	default:
		// Serial execution — tasks run one at a time in receive order.
		taskQueue <- func() { executeTask(ctx, cfg, client, task, syncRecv) }
	}
}

// executeTask runs one task and reports its result. The task's TimeoutSecs
// bounds the whole run: commands are stopped and waits cut short when it ends.
func executeTask(ctx context.Context, cfg *agentconfig.Config, client *ws.Client, task protocol.TaskDispatchPayload, syncRecv *executor.SyncReceiver) {
	start := time.Now()
	if task.TimeoutSecs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(task.TimeoutSecs)*time.Second)
		defer cancel()
	}

	// Sync messages wait for buffer space: a dropped one would corrupt the sync.
	sendWait := func(t protocol.MessageType, payload any) error {
		raw, _ := json.Marshal(payload)
		return client.SendWait(protocol.Envelope{ID: uuid.New().String(), Type: t, Payload: raw}, chunkSendTimeout)
	}
	// syncRoot maps a sync folder to a local path: "" is the NetBox media folder.
	syncRoot := func(root string) (string, error) {
		if root != "" {
			return root, nil
		}
		if cfg.NetboxMediaRoot == "" {
			return "", executor.ErrNoNetbox
		}
		return cfg.NetboxMediaRoot, nil
	}

	var output string
	var err error

	switch task.TaskType {
	case protocol.TaskWriteConfig:
		output, err = run(ctx, task.Params, func(ctx context.Context, p protocol.ConfigWriteParams) (string, error) {
			return executor.WriteConfig(ctx, p, cfg.NetboxConfigPath)
		})

	case protocol.TaskRestartNetbox:
		output, err = runCmd(ctx, "sudo", "systemctl", "restart", "netbox", "netbox-rq")

	case protocol.TaskStartNetbox:
		output, err = runCmd(ctx, "sudo", "systemctl", "start", "netbox", "netbox-rq")

	case protocol.TaskStopNetbox:
		output, err = runCmd(ctx, "sudo", "systemctl", "stop", "netbox", "netbox-rq")

	case protocol.TaskRestartRQ:
		output, err = runCmd(ctx, "sudo", "systemctl", "restart", "netbox-rq")

	case protocol.TaskWritePatroniConf:
		output, err = run(ctx, task.Params, executor.WritePatroniConfig)

	case protocol.TaskStopSentinel:
		// Best effort: sentinel may already be stopped or not installed.
		for _, args := range [][]string{
			{"systemctl", "stop", "redis-sentinel"},
			{"systemctl", "disable", "redis-sentinel"},
		} {
			out, _ := runCmd(ctx, "sudo", args...)
			output += out
		}

	case protocol.TaskStopPostgresql:
		output, err = runCmd(ctx, "sudo", "systemctl", "stop", "postgresql")

	case protocol.TaskRestartPatroni:
		// patroni is enabled at agent install time (install.sh), not here.
		output, err = runCmd(ctx, "sudo", "systemctl", "restart", "patroni")

	case protocol.TaskStopPostgres:
		output, err = runCmd(ctx, "sudo", "-u", "postgres",
			"pg_ctl", "stop", "-D", executor.PostgresDataDir(ctx), "-m", "fast", "-w")

	case protocol.TaskPatroniSwitchover:
		output, err = run(ctx, task.Params, func(ctx context.Context, p protocol.PatroniSwitchoverParams) (string, error) {
			if p.Candidate == "" {
				return "", errors.New("candidate hostname is required")
			}
			clusterName := readPatroniClusterName()
			if clusterName == "" {
				return "", errors.New("could not read Patroni cluster name from /etc/patroni/patroni.yml")
			}
			return runCmd(ctx, "patronictl", "-c", "/etc/patroni/patroni.yml",
				"switchover", "--force", clusterName, "--candidate", p.Candidate)
		})

	case protocol.TaskRestartRedis:
		output, err = runCmd(ctx, "sudo", "systemctl", "restart", "redis")

	case protocol.TaskRestartSentinel:
		output, err = runCmd(ctx, "sudo", "systemctl", "restart", "redis-sentinel")

	case protocol.TaskWriteSentinelConf:
		output, err = run(ctx, task.Params, executor.WriteSentinelConfig)

	case protocol.TaskDBRestore:
		output, err = run(ctx, task.Params, executor.RunDBRestore)

	case protocol.TaskDBBackup:
		output, err = run(ctx, task.Params, executor.RunDBBackup)

	case protocol.TaskUpdateDBHost:
		output, err = run(ctx, task.Params, func(ctx context.Context, p protocol.DBHostUpdateParams) (string, error) {
			return executor.UpdateDBHost(ctx, p, cfg.NetboxConfigPath)
		})

	case protocol.TaskUpdateRedisHost:
		output, err = run(ctx, task.Params, func(ctx context.Context, p protocol.RedisHostUpdateParams) (string, error) {
			return executor.UpdateRedisHost(ctx, p, cfg.NetboxConfigPath)
		})

	case protocol.TaskRedisSetRequirepass:
		output, err = run(ctx, task.Params, executor.SetRedisRequirepass)

	case protocol.TaskRedisBindAll:
		output, err = executor.SetRedisBindAll(ctx)

	case protocol.TaskCreatePgRole:
		output, err = run(ctx, task.Params, executor.CreatePgRole)

	case protocol.TaskInstallPatroni:
		output, err = run(ctx, task.Params, executor.InstallPatroni)

	case protocol.TaskRunCommand:
		output, err = run(ctx, task.Params, executor.RunCommand)

	case protocol.TaskReadNetboxConfig:
		var content []byte
		content, err = os.ReadFile(cfg.NetboxConfigPath)
		output = string(content)

	case protocol.TaskPGBackRestConfigure:
		output, err = run(ctx, task.Params, func(_ context.Context, p protocol.PGBackRestConfigParams) (string, error) {
			return executor.WritePGBackRestConfig(p)
		})

	case protocol.TaskPGBackRestStanzaCreate:
		output, err = run(ctx, task.Params, executor.RunPGBackRestStanzaCreate)

	case protocol.TaskPGBackRestBackup:
		output, err = run(ctx, task.Params, func(ctx context.Context, p protocol.PGBackRestBackupParams) (string, error) {
			return executor.RunPGBackRestBackups(ctx, p, func(e protocol.BackupEventPayload) {
				raw, _ := json.Marshal(e)
				client.Send(protocol.Envelope{ID: uuid.New().String(), Type: protocol.TypeBackupEvent, Payload: raw})
			})
		})

	case protocol.TaskPGBackRestCatalog:
		output, err = run(ctx, task.Params, executor.RunPGBackRestCatalog)

	case protocol.TaskPGBackRestRestore:
		output, err = run(ctx, task.Params, executor.RunPGBackRestRestore)

	case protocol.TaskPGBackRestTestPath:
		output, err = run(ctx, task.Params, executor.RunPGBackRestTestPath)

	case protocol.TaskSyncList:
		output, err = run(ctx, task.Params, func(ctx context.Context, p protocol.SyncListParams) (string, error) {
			root, err := syncRoot(p.Root)
			if err != nil {
				return "", err
			}
			return executor.ListSyncFolder(ctx, p.SyncID, root, func(l protocol.SyncListingPayload) error {
				return sendWait(protocol.TypeSyncListing, l)
			})
		})

	case protocol.TaskSyncSend:
		output, err = run(ctx, task.Params, func(ctx context.Context, p protocol.SyncSendParams) (string, error) {
			root, err := syncRoot(p.Root)
			if err != nil {
				return "", err
			}
			return executor.SendSyncFiles(ctx, p, root, func(c protocol.SyncChunkPayload) error {
				return sendWait(protocol.TypeSyncChunk, c)
			})
		})

	case protocol.TaskSyncReceive:
		output, err = run(ctx, task.Params, func(_ context.Context, p protocol.SyncReceiveParams) (string, error) {
			root, err := syncRoot(p.Root)
			if err != nil {
				return "", err
			}
			return syncRecv.Start(p, root)
		})

	case protocol.TaskSyncFinish:
		output, err = run(ctx, task.Params, func(_ context.Context, p protocol.SyncFinishParams) (string, error) {
			return syncRecv.Finish(p)
		})

	case protocol.TaskSyncDelete:
		output, err = run(ctx, task.Params, func(ctx context.Context, p protocol.SyncDeleteParams) (string, error) {
			root, err := syncRoot(p.Root)
			if err != nil {
				return "", err
			}
			return executor.DeleteSyncFiles(ctx, p, root)
		})

	default:
		err = fmt.Errorf("unknown task type: %s", task.TaskType)
	}

	var errMsg string
	if err != nil {
		errMsg = err.Error()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			errMsg = fmt.Sprintf("timed out after %ds: %s", task.TimeoutSecs, errMsg)
		}
		slog.Warn("task failed",
			"task_id", task.TaskID,
			"type", task.TaskType,
			"error", errMsg,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	} else {
		slog.Info("task done",
			"task_id", task.TaskID,
			"type", task.TaskType,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}

	sendResult(client, task.TaskID, err == nil, output, errMsg, time.Since(start).Milliseconds())
}

// run decodes a task's params into P and runs fn with them.
func run[P any](ctx context.Context, raw json.RawMessage, fn func(context.Context, P) (string, error)) (string, error) {
	var params P
	if err := json.Unmarshal(raw, &params); err != nil {
		return "", fmt.Errorf("bad params: %w", err)
	}
	return fn(ctx, params)
}

// runCmd runs a command under the task's deadline and returns its combined output.
func runCmd(ctx context.Context, name string, args ...string) (string, error) {
	out, err := executor.Command(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// readPatroniClusterName parses the 'name:' field from /etc/patroni/patroni.yml.
func readPatroniClusterName() string {
	data, err := os.ReadFile("/etc/patroni/patroni.yml")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "name:") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "name:")), `"'`)
		}
	}
	return ""
}

func sendResult(client *ws.Client, taskID string, success bool, output, errMsg string, durationMs int64) {
	payload, _ := json.Marshal(protocol.TaskResultPayload{
		TaskID:     taskID,
		Success:    success,
		Output:     output,
		ErrorMsg:   errMsg,
		DurationMs: durationMs,
		Repaired:   executor.TakeRepaired(),
	})
	// Wait for buffer space: a sync task can leave the buffer full of chunks.
	// A result that still can't be sent is read by the conductor as lost.
	if err := client.EndTask(taskID, protocol.Envelope{
		ID:      uuid.New().String(),
		Type:    protocol.TypeTaskResult,
		Payload: json.RawMessage(payload),
	}, chunkSendTimeout); err != nil {
		slog.Error("task result not sent", "task_id", taskID, "error", err)
	}
}
