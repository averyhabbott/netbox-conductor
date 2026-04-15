package protocol

import "encoding/json"

// MessageType identifies the kind of WebSocket message.
type MessageType string

const (
	// Agent → Server
	TypeAgentHello     MessageType = "agent.hello"
	TypeAgentHeartbeat MessageType = "agent.heartbeat"
	TypePatroniState   MessageType = "patroni.state"
	TypeTaskAck        MessageType = "task.ack"
	TypeTaskResult     MessageType = "task.result"
	TypeMediaChunk     MessageType = "media.chunk"
	TypeMediaChunkAck  MessageType = "media.chunk.ack"

	// Server → Agent
	TypeServerHello  MessageType = "server.hello"
	TypeTaskDispatch MessageType = "task.dispatch"
	TypeMediaRequest MessageType = "media.request"
)

// TaskType identifies the operation an agent should perform.
type TaskType string

const (
	TaskWriteConfig       TaskType = "config.write"
	TaskStartNetbox       TaskType = "service.start.netbox"
	TaskStopNetbox        TaskType = "service.stop.netbox"
	TaskRestartNetbox     TaskType = "service.restart.netbox"
	TaskRestartRQ         TaskType = "service.restart.rq"
	TaskInstallPatroni    TaskType = "patroni.install"
	TaskWritePatroniConf  TaskType = "patroni.write_config"
	TaskRestartPatroni    TaskType = "service.restart.patroni"
	TaskRestartRedis      TaskType = "service.restart.redis"
	TaskRestartSentinel   TaskType = "service.restart.redis-sentinel"
	TaskMediaSync         TaskType = "media.sync"
	TaskRunCommand        TaskType = "exec.run" // admin-only ad-hoc
)

// Envelope wraps every WebSocket message.
type Envelope struct {
	// ID is a UUIDv4 used to correlate requests and responses.
	ID      string          `json:"id"`
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// ────────────────────────────────────────────────────────────────
// Agent → Server payloads
// ────────────────────────────────────────────────────────────────

// AgentHelloPayload is the first message sent by the agent after connecting.
// The server authenticates the agent using NodeID + Token.
type AgentHelloPayload struct {
	NodeID       string `json:"node_id"`
	Token        string `json:"token"`
	AgentVersion string `json:"agent_version"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
}

// HeartbeatPayload is sent by the agent every 15 seconds.
type HeartbeatPayload struct {
	NodeID        string   `json:"node_id"`
	LoadAvg1      float64  `json:"load_avg_1"`
	LoadAvg5      float64  `json:"load_avg_5"`
	MemUsedPct    float64  `json:"mem_used_pct"`
	DiskUsedPct   float64  `json:"disk_used_pct"`
	NetboxRunning bool     `json:"netbox_running"`
	RQRunning     bool     `json:"rq_running"`
	PatroniRole   string   `json:"patroni_role"`   // "primary", "replica", "standby_leader", ""
	PatroniLagB   *int64   `json:"patroni_lag_bytes,omitempty"`
	PatroniState  *json.RawMessage `json:"patroni_state,omitempty"` // full Patroni /patroni response
}

// PatroniStatePayload is sent proactively when the agent detects a role change.
type PatroniStatePayload struct {
	NodeID      string          `json:"node_id"`
	Role        string          `json:"role"`
	PrevRole    string          `json:"prev_role"`
	StateJSON   json.RawMessage `json:"state"`
}

// TaskAckPayload confirms that the agent received a task dispatch.
type TaskAckPayload struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"` // "accepted" | "rejected"
	Reason string `json:"reason,omitempty"`
}

// TaskResultPayload reports the outcome of a completed task.
type TaskResultPayload struct {
	TaskID     string `json:"task_id"`
	Success    bool   `json:"success"`
	Output     string `json:"output,omitempty"`
	ErrorMsg   string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// MediaChunkPayload carries a chunk of a file being relayed through the server.
type MediaChunkPayload struct {
	TransferID   string `json:"transfer_id"`
	RelativePath string `json:"relative_path"`
	ChunkIndex   int    `json:"chunk_index"`
	TotalChunks  int    `json:"total_chunks"`
	Data         []byte `json:"data"` // raw bytes; JSON marshals as base64
	EOF          bool   `json:"eof"`
}

// MediaChunkAckPayload acknowledges receipt of a chunk (backpressure).
type MediaChunkAckPayload struct {
	TransferID string `json:"transfer_id"`
	ChunkIndex int    `json:"chunk_index"`
}

// ────────────────────────────────────────────────────────────────
// Server → Agent payloads
// ────────────────────────────────────────────────────────────────

// ServerHelloPayload is the server's response to AgentHelloPayload.
type ServerHelloPayload struct {
	Accepted      bool   `json:"accepted"`
	RejectReason  string `json:"reject_reason,omitempty"`
	ServerVersion string `json:"server_version"`
}

// TaskDispatchPayload instructs the agent to execute a task.
type TaskDispatchPayload struct {
	TaskID      string          `json:"task_id"`
	TaskType    TaskType        `json:"task_type"`
	Params      json.RawMessage `json:"params"`
	TimeoutSecs int             `json:"timeout_secs"`
}

// MediaRequestPayload instructs the agent to begin streaming a file to the server.
type MediaRequestPayload struct {
	TransferID   string `json:"transfer_id"`
	RelativePath string `json:"relative_path"` // empty = full MEDIA_ROOT
	ChunkSize    int    `json:"chunk_size"`    // bytes per chunk
}

// ────────────────────────────────────────────────────────────────
// Task param structs (embedded in TaskDispatchPayload.Params)
// ────────────────────────────────────────────────────────────────

// ConfigWriteParams are the params for TaskWriteConfig.
type ConfigWriteParams struct {
	Content        string `json:"content"`
	Sha256         string `json:"sha256"`
	BackupExisting bool   `json:"backup_existing"`
	RestartAfter   bool   `json:"restart_after"`
}

// PatroniConfigWriteParams are the params for TaskWritePatroniConf.
type PatroniConfigWriteParams struct {
	Content      string `json:"content"`
	Sha256       string `json:"sha256"`
	RestartAfter bool   `json:"restart_after"`
}

// RunCommandParams are the params for TaskRunCommand (admin-only).
type RunCommandParams struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// MediaSyncParams are the params for TaskMediaSync.
type MediaSyncParams struct {
	Direction    string `json:"direction"`      // "push_to_server" | "pull_from_server"
	RelativePath string `json:"relative_path"`  // "" = full sync
	ChunkSizeB   int    `json:"chunk_size"`
	TransferID   string `json:"transfer_id"`
}
