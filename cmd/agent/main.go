package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	agentconfig "github.com/abottVU/netbox-failover/internal/agent/config"
	"github.com/abottVU/netbox-failover/internal/agent/executor"
	"github.com/abottVU/netbox-failover/internal/agent/ws"
	"github.com/abottVU/netbox-failover/internal/shared/protocol"
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

	if !cfg.IsRegistered() {
		log.Fatalf("agent is not registered: AGENT_NODE_ID and AGENT_TOKEN must be set in %s", envFile)
	}

	log.Printf("netbox-agent starting | node=%s server=%s", cfg.NodeID, cfg.ServerURL)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Metrics collector
	metrics := executor.NewMetricsCollector(cfg.NetboxConfigPath, cfg.NetboxMediaRoot, cfg.PatroniRESTURL)

	// Patroni role watcher (fires when role changes; sends a proactive patroni.state message)
	var wsClient *ws.Client // set after creation below
	roleWatcher := executor.NewPatroniRoleWatcher(metrics, func(newRole, prevRole string, stateJSON []byte) {
		log.Printf("patroni role change: %s -> %s", prevRole, newRole)
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

	// Message handler for inbound server commands
	onMessage := func(ctx context.Context, env protocol.Envelope) error {
		switch env.Type {
		case protocol.TypeTaskDispatch:
			handleTaskDispatch(ctx, cfg, wsClient, env)
		default:
			log.Printf("unhandled server message type: %s", env.Type)
		}
		return nil
	}

	// WebSocket client
	client, err := ws.New(cfg, onMessage)
	if err != nil {
		log.Fatalf("creating WS client: %v", err)
	}
	wsClient = client

	// Wire heartbeat function
	client.HeartbeatFn = func() (protocol.HeartbeatPayload, error) {
		return metrics.Collect()
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

	// Run WebSocket client (blocks until ctx cancelled)
	client.Run(ctx)
	log.Println("agent stopped")
}

// handleTaskDispatch routes an inbound task to the appropriate executor.
// Full task execution is implemented in Phase 3+; this logs and acks for now.
func handleTaskDispatch(ctx context.Context, cfg *agentconfig.Config, client *ws.Client, env protocol.Envelope) {
	var task protocol.TaskDispatchPayload
	if err := json.Unmarshal(env.Payload, &task); err != nil {
		log.Printf("malformed task dispatch: %v", err)
		return
	}
	log.Printf("received task: id=%s type=%s", task.TaskID, task.TaskType)

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

	// Execute task in background so we don't block the read loop
	go executeTask(ctx, cfg, client, task)
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
		cmd := exec.Command("systemctl", "restart", "netbox", "netbox-rq")
		out, err := cmd.CombinedOutput()
		output = string(out)
		if err != nil {
			errMsg = err.Error()
		} else {
			success = true
		}

	case protocol.TaskStartNetbox:
		cmd := exec.Command("systemctl", "start", "netbox", "netbox-rq")
		out, err := cmd.CombinedOutput()
		output = string(out)
		if err != nil {
			errMsg = err.Error()
		} else {
			success = true
		}

	case protocol.TaskStopNetbox:
		cmd := exec.Command("systemctl", "stop", "netbox", "netbox-rq")
		out, err := cmd.CombinedOutput()
		output = string(out)
		if err != nil {
			errMsg = err.Error()
		} else {
			success = true
		}

	case protocol.TaskRestartRQ:
		cmd := exec.Command("systemctl", "restart", "netbox-rq")
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
		cmd := exec.Command("systemctl", "restart", "patroni")
		out, err := cmd.CombinedOutput()
		output = string(out)
		if err != nil {
			errMsg = err.Error()
		} else {
			success = true
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

	default:
		errMsg = "unknown task type: " + string(task.TaskType)
	}

	log.Printf("task done: id=%s type=%s success=%v duration=%dms",
		task.TaskID, task.TaskType, success, time.Since(start).Milliseconds())

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
