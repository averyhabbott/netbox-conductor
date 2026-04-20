package protocol

import "encoding/json"

// MessageType identifies the kind of WebSocket message.
type MessageType string

const (
	// Agent → Server
	TypeAgentHello          MessageType = "agent.hello"
	TypeAgentHeartbeat      MessageType = "agent.heartbeat"
	TypePatroniState        MessageType = "patroni.state"
	TypeServiceStateChange  MessageType = "agent.service_state_change"
	TypeTaskAck             MessageType = "task.ack"
	TypeTaskResult          MessageType = "task.result"
	TypeMediaChunk          MessageType = "media.chunk"
	TypeMediaChunkAck       MessageType = "media.chunk.ack"
	TypeNetboxLog           MessageType = "netbox.log"

	// Server → Agent
	TypeServerHello  MessageType = "server.hello"
	TypeTaskDispatch MessageType = "task.dispatch"
	TypeMediaRequest MessageType = "media.request"
)

// TaskType identifies the operation an agent should perform.
type TaskType string

const (
	TaskWriteConfig       TaskType = "config.write"
	TaskUpdateDBHost      TaskType = "config.update_db_host"
	TaskStartNetbox       TaskType = "service.start.netbox"
	TaskStopNetbox        TaskType = "service.stop.netbox"
	TaskRestartNetbox     TaskType = "service.restart.netbox"
	TaskRestartRQ         TaskType = "service.restart.rq"
	TaskInstallPatroni    TaskType = "patroni.install"
	TaskCreatePgRole      TaskType = "postgres.create_role"
	TaskWritePatroniConf  TaskType = "patroni.write_config"
	TaskRestartPatroni    TaskType = "service.restart.patroni"
	TaskRestartRedis      TaskType = "service.restart.redis"
	TaskRestartSentinel   TaskType = "service.restart.redis-sentinel"
	TaskWriteSentinelConf TaskType = "sentinel.write_config"
	TaskMediaSync         TaskType = "media.sync"
	TaskDBRestore         TaskType = "db.restore"     // reinitialize a replica or restore from backup
	TaskDBBackup          TaskType = "db.backup"      // pg_dump the primary database before destructive ops
	TaskRunCommand        TaskType = "exec.run"        // admin-only ad-hoc
	TaskEnforceRetention  TaskType = "backup.expire"   // run pgbackrest expire / retention enforcement
	TaskAgentUpgrade      TaskType = "agent.upgrade"   // self-upgrade the agent binary
	TaskReadNetboxConfig  TaskType = "config.read"     // read /opt/netbox/.../configuration.py from agent
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
	NetboxVersion string   `json:"netbox_version,omitempty"` // e.g. "4.1.0"
	PatroniRole   string   `json:"patroni_role"`   // "primary", "replica", "standby_leader", ""
	PatroniLagB   *int64   `json:"patroni_lag_bytes,omitempty"`
	PatroniState  *json.RawMessage `json:"patroni_state,omitempty"` // full Patroni /patroni response

	// Service-level health indicators added in v0.1.1+.
	RedisRunning    bool   `json:"redis_running"`
	RedisRole       string `json:"redis_role,omitempty"`    // "master" | "slave" | ""
	SentinelRunning bool   `json:"sentinel_running"`
	PatroniRunning  bool   `json:"patroni_running"`
	PostgresRunning bool   `json:"postgres_running"`
}

// PatroniStatePayload is sent proactively when the agent detects a role change.
type PatroniStatePayload struct {
	NodeID    string          `json:"node_id"`
	Role      string          `json:"role"`
	PrevRole  string          `json:"prev_role"`
	StateJSON json.RawMessage `json:"state"`
}

// ServiceStateChangePayload is sent immediately when the agent detects a service
// start or stop, providing faster alerting than the 15-second heartbeat cycle.
type ServiceStateChangePayload struct {
	NodeID  string `json:"node_id"`
	Service string `json:"service"` // "netbox"|"rq"|"patroni"|"postgres"|"redis"|"sentinel"
	Running bool   `json:"running"`
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

// NetboxLogPayload carries a batch of NetBox application log lines from the agent.
type NetboxLogPayload struct {
	NodeID  string   `json:"node_id"`
	LogName string   `json:"log_name"` // base filename, e.g. "netbox.log"; empty → treated as "netbox.log"
	Lines   []string `json:"lines"`
}

// ────────────────────────────────────────────────────────────────
// Server → Agent payloads
// ────────────────────────────────────────────────────────────────

// ServerHelloPayload is the server's response to AgentHelloPayload.
// It includes cluster configuration so the agent can update the status server
// and behave correctly without an additional round-trip.
type ServerHelloPayload struct {
	Accepted      bool   `json:"accepted"`
	RejectReason  string `json:"reject_reason,omitempty"`
	ServerVersion string `json:"server_version"`

	// Cluster configuration delivered on connect.
	// Zero values when the agent is in staging or the cluster lookup fails.
	ClusterID              string `json:"cluster_id,omitempty"`
	AppTierAlwaysAvailable bool   `json:"app_tier_always_available"`
	PatroniScope           string `json:"patroni_scope,omitempty"`
	PatroniConfigured      bool   `json:"patroni_configured"`
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

// DBHostUpdateParams are the params for TaskUpdateDBHost.
// The agent patches only the DATABASE.HOST line in configuration.py, preserving
// all other settings. Used when the Patroni primary changes and all app-tier
// nodes running in app_tier_always_available mode need to reconnect to the new primary.
type DBHostUpdateParams struct {
	Host         string `json:"host"`          // new DATABASE.HOST value (bare IP, no CIDR)
	RestartAfter bool   `json:"restart_after"` // restart netbox+netbox-rq after patching
}

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

// SentinelConfigWriteParams are the params for TaskWriteSentinelConf.
type SentinelConfigWriteParams struct {
	Content      string `json:"content"`
	Sha256       string `json:"sha256"`
	RestartAfter bool   `json:"restart_after"`
}

// DBRestoreParams are the params for TaskDBRestore.
// Method selects the restore strategy:
//   - "reinitialize": run `patronictl reinitialize` (re-clones replica from primary)
//   - "pitr": run a custom restore command with a target recovery time (requires pgBackRest/WAL-E)
type DBRestoreParams struct {
	Method        string `json:"method"`          // "reinitialize" | "pitr"
	TargetTime    string `json:"target_time"`     // ISO8601 — used for pitr
	RestoreCmd    string `json:"restore_command"` // optional: override default restore command
	PatroniScope  string `json:"patroni_scope"`   // cluster scope for patronictl
}

// DBBackupParams are the params for TaskDBBackup.
// The agent runs pg_dump on the local Postgres instance and writes the backup
// file to OutputDir. The resulting path is returned in the task output so the
// operator can retrieve it later (the restore-from-backup UI is a future feature).
type DBBackupParams struct {
	DBName    string `json:"db_name"`    // database to dump; default "netbox"
	DBUser    string `json:"db_user"`    // Postgres role to connect as; default "postgres"
	OutputDir string `json:"output_dir"` // directory for the dump file; default "/var/lib/postgresql/backups"
}

// MediaSyncParams are the params for TaskMediaSync.
type MediaSyncParams struct {
	Direction    string `json:"direction"`      // "push_to_server" | "pull_from_server"
	RelativePath string `json:"relative_path"`  // "" = full sync within SourcePath
	SourcePath   string `json:"source_path"`    // absolute path override; "" = use MEDIA_ROOT
	ChunkSizeB   int    `json:"chunk_size"`
	TransferID   string `json:"transfer_id"`
}

// PatroniInstallParams are the params for TaskInstallPatroni.
type PatroniInstallParams struct {
	PackageManager string `json:"package_manager"` // "apt-get" | "yum" | "dnf" — auto-detected if empty
	InstallCmd     string `json:"install_cmd"`     // optional full override command
}

// CreatePgRoleParams are the params for TaskCreatePgRole.
// The conductor constructs the role details; the agent executes the SQL as the
// postgres OS user via peer authentication — no pg_hba.conf remote-access rules
// or database passwords required.
type CreatePgRoleParams struct {
	RoleName string   `json:"role_name"`
	Password string   `json:"password"`
	Options  []string `json:"options"` // e.g. ["LOGIN", "REPLICATION"]
}

// EnforceRetentionParams are the params for TaskEnforceRetention.
type EnforceRetentionParams struct {
	PatroniScope string `json:"patroni_scope"` // pgBackRest stanza name (defaults to "main")
	ExpireCmd    string `json:"expire_cmd"`    // optional override for the expire command
}

// AgentUpgradeParams are the params for TaskAgentUpgrade.
type AgentUpgradeParams struct {
	DownloadURL string `json:"download_url"` // full URL of the tarball (e.g. https://conductor:8443/api/v1/downloads/agent-linux-amd64)
	Arch        string `json:"arch"`         // "amd64" | "arm64"
}
