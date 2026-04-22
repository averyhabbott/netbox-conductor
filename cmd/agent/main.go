package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"log/syslog"
	"os"
	"os/exec"
	"os/signal"
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

func main() {
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

	// Cert-learning: one-time download of conductor's CA cert.
	// Only runs when UPDATE_CERT=true (default); updates the env file and the
	// in-memory config so the WS connection uses the downloaded cert.
	if cfg.UpdateCert {
		if err := agentconfig.LearnCert(cfg, envFile); err != nil {
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

	// Message handler for inbound server commands
	onMessage := func(ctx context.Context, env protocol.Envelope) error {
		switch env.Type {
		case protocol.TypeTaskDispatch:
			handleTaskDispatch(ctx, cfg, wsClient, env, taskQueue)
		case protocol.TypeMediaChunk:
			// Server is forwarding a media chunk to us (pull_from_server mode).
			go func() {
				var chunk protocol.MediaChunkPayload
				if err := json.Unmarshal(env.Payload, &chunk); err != nil {
					slog.Warn("malformed media.chunk", "error", err)
					return
				}
				if err := executor.WriteMediaChunk(chunk, cfg.NetboxMediaRoot); err != nil {
					slog.Warn("writing media chunk", "path", chunk.RelativePath, "error", err)
					return
				}
				// Send ack for backpressure
				ack, _ := json.Marshal(protocol.MediaChunkAckPayload{
					TransferID: chunk.TransferID,
					ChunkIndex: chunk.ChunkIndex,
				})
				wsClient.Send(protocol.Envelope{
					ID:      uuid.New().String(),
					Type:    protocol.TypeMediaChunkAck,
					Payload: json.RawMessage(ack),
				})
			}()
		case protocol.TypeBackupChunk:
			// Conductor is relaying a pgBackRest repo file chunk to us (backup sync write side).
			go func() {
				var chunk protocol.BackupChunkPayload
				if err := json.Unmarshal(env.Payload, &chunk); err != nil {
					slog.Warn("malformed backup.chunk", "error", err)
					return
				}
				if err := executor.WriteBackupChunk(chunk, ""); err != nil {
					slog.Warn("writing backup chunk", "path", chunk.RelativePath, "error", err)
					return
				}
				ack, _ := json.Marshal(protocol.BackupChunkAckPayload{
					TransferID: chunk.TransferID,
					ChunkIndex: chunk.ChunkIndex,
				})
				wsClient.Send(protocol.Envelope{
					ID:      uuid.New().String(),
					Type:    protocol.TypeBackupChunkAck,
					Payload: json.RawMessage(ack),
				})
			}()

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
	writers := []io.Writer{os.Stderr}

	// Best-effort syslog — skip silently if the socket is unavailable (e.g. in
	// containers or non-Linux environments).
	if sw, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, "netbox-agent"); err == nil {
		writers = append(writers, sw)
	}

	h := slog.NewTextHandler(
		io.MultiWriter(writers...),
		&slog.HandlerOptions{Level: level},
	)
	logger := slog.New(h)
	slog.SetDefault(logger)
	// Route stdlib log.Printf calls through slog at Info level.
	log.SetOutput(io.Discard) // slog.SetDefault already redirects; silence duplicate output
}

// handleTaskDispatch routes an inbound task to the appropriate executor.
func handleTaskDispatch(ctx context.Context, cfg *agentconfig.Config, client *ws.Client, env protocol.Envelope, taskQueue chan<- func()) {
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
	client.Send(protocol.Envelope{
		ID:      uuid.New().String(),
		Type:    protocol.TypeTaskAck,
		Payload: json.RawMessage(ackPayload),
	})

	// Enqueue task for serial execution — tasks run one at a time in receive order.
	taskQueue <- func() { executeTask(ctx, cfg, client, task) }
}

func executeTask(ctx context.Context, cfg *agentconfig.Config, client *ws.Client, task protocol.TaskDispatchPayload) {
	start := time.Now()
	var output, errMsg string
	success := false

	switch task.TaskType {
	case protocol.TaskWriteConfig:
		var params protocol.ConfigWriteParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.WriteConfig(params, cfg.NetboxConfigPath)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskRestartNetbox:
		cmd := exec.Command("sudo", "systemctl", "restart", "netbox", "netbox-rq")
		out, err := cmd.CombinedOutput()
		output = string(out)
		if err != nil {
			errMsg = err.Error()
		} else {
			success = true
		}

	case protocol.TaskStartNetbox:
		cmd := exec.Command("sudo", "systemctl", "start", "netbox", "netbox-rq")
		out, err := cmd.CombinedOutput()
		output = string(out)
		if err != nil {
			errMsg = err.Error()
		} else {
			success = true
		}

	case protocol.TaskStopNetbox:
		cmd := exec.Command("sudo", "systemctl", "stop", "netbox", "netbox-rq")
		out, err := cmd.CombinedOutput()
		output = string(out)
		if err != nil {
			errMsg = err.Error()
		} else {
			success = true
		}

	case protocol.TaskRestartRQ:
		cmd := exec.Command("sudo", "systemctl", "restart", "netbox-rq")
		out, err := cmd.CombinedOutput()
		output = string(out)
		if err != nil {
			errMsg = err.Error()
		} else {
			success = true
		}

	case protocol.TaskWritePatroniConf:
		var params protocol.PatroniConfigWriteParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.WritePatroniConfig(params)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskRestartPatroni:
		// patroni is enabled at agent install time (install.sh), not here.
		cmd := exec.Command("sudo", "systemctl", "restart", "patroni")
		out, err := cmd.CombinedOutput()
		output = string(out)
		if err != nil {
			errMsg = err.Error()
		} else {
			success = true
		}

	case protocol.TaskRestartRedis:
		cmd := exec.Command("sudo", "systemctl", "restart", "redis")
		out, err := cmd.CombinedOutput()
		output = string(out)
		if err != nil {
			errMsg = err.Error()
		} else {
			success = true
		}

	case protocol.TaskRestartSentinel:
		cmd := exec.Command("sudo", "systemctl", "restart", "redis-sentinel")
		out, err := cmd.CombinedOutput()
		output = string(out)
		if err != nil {
			errMsg = err.Error()
		} else {
			success = true
		}

	case protocol.TaskWriteSentinelConf:
		var params protocol.SentinelConfigWriteParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.WriteSentinelConfig(params)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskDBRestore:
		var params protocol.DBRestoreParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.RunDBRestore(params)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskDBBackup:
		var params protocol.DBBackupParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.RunDBBackup(params)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskUpdateDBHost:
		var params protocol.DBHostUpdateParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.UpdateDBHost(params, cfg.NetboxConfigPath)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskUpdateRedisHost:
		var params protocol.RedisHostUpdateParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.UpdateRedisHost(params, cfg.NetboxConfigPath)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskCreatePgRole:
		var params protocol.CreatePgRoleParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.CreatePgRole(params)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskInstallPatroni:
		var params protocol.PatroniInstallParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.InstallPatroni(params)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskEnforceRetention:
		var params protocol.EnforceRetentionParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.EnforceRetention(params)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskMediaSync:
		var params protocol.MediaSyncParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else if params.Direction == "push_to_server" {
			err := executor.PushMediaRoot(params, cfg.NetboxMediaRoot, func(env protocol.Envelope) {
				client.Send(env)
			})
			if err != nil {
				errMsg = err.Error()
			} else {
				output = "media push complete"
				success = true
			}
		} else {
			// pull_from_server: chunks arrive via TypeMediaChunk messages; this task
			// just signals readiness — actual writing happens in the message handler.
			output = "pull mode: ready to receive chunks"
			success = true
		}

	case protocol.TaskAgentUpgrade:
		var params protocol.AgentUpgradeParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.UpgradeAgent(params, cfg.TLSSkipVerify, cfg.TLSCACert)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskRunCommand:
		var params protocol.RunCommandParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.RunCommand(params)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskReadNetboxConfig:
		content, err := os.ReadFile(cfg.NetboxConfigPath)
		if err != nil {
			errMsg = err.Error()
		} else {
			output = string(content)
			success = true
		}

	case protocol.TaskPGBackRestConfigure:
		var params protocol.PGBackRestConfigParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.WritePGBackRestConfig(params)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskPGBackRestStanzaCreate:
		var params protocol.PGBackRestStanzaCreateParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.RunPGBackRestStanzaCreate(params)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskPGBackRestBackup:
		var params protocol.PGBackRestBackupParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.RunPGBackRestBackup(params)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskPGBackRestCatalog:
		var params protocol.PGBackRestCatalogParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.RunPGBackRestCatalog(params)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskPGBackRestRestore:
		var params protocol.PGBackRestRestoreParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.RunPGBackRestRestore(params)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskPGBackRestTestPath:
		var params protocol.PGBackRestTestPathParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			out, err := executor.RunPGBackRestTestPath(params)
			output = out
			if err != nil {
				errMsg = err.Error()
			} else {
				success = true
			}
		}

	case protocol.TaskBackupSyncRead:
		var params protocol.BackupSyncReadParams
		if err := json.Unmarshal(task.Params, &params); err != nil {
			errMsg = "bad params: " + err.Error()
		} else {
			err := executor.PushBackupRepo(params, func(env protocol.Envelope) {
				client.Send(env)
			})
			if err != nil {
				errMsg = err.Error()
			} else {
				output = "backup repo sync push complete"
				success = true
			}
		}

	case protocol.TaskBackupSyncWrite:
		// Chunks arrive via TypeBackupChunk messages in the message handler.
		// This task just signals readiness to receive.
		output = "ready to receive backup chunks"
		success = true

	default:
		errMsg = "unknown task type: " + string(task.TaskType)
	}

	if success {
		slog.Info("task done",
			"task_id", task.TaskID,
			"type", task.TaskType,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	} else {
		slog.Warn("task failed",
			"task_id", task.TaskID,
			"type", task.TaskType,
			"error", errMsg,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}

	sendResult(client, task.TaskID, success, output, errMsg, time.Since(start).Milliseconds())
}

func sendResult(client *ws.Client, taskID string, success bool, output, errMsg string, durationMs int64) {
	payload, _ := json.Marshal(protocol.TaskResultPayload{
		TaskID:     taskID,
		Success:    success,
		Output:     output,
		ErrorMsg:   errMsg,
		DurationMs: durationMs,
	})
	client.Send(protocol.Envelope{
		ID:      uuid.New().String(),
		Type:    protocol.TypeTaskResult,
		Payload: json.RawMessage(payload),
	})
}
