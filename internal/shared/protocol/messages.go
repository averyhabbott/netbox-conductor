package protocol

import "encoding/json"

// MessageType identifies the kind of WebSocket message.
type MessageType string

const (
	// Agent → Server
	TypeAgentHello         MessageType = "agent.hello"
	TypeAgentHeartbeat     MessageType = "agent.heartbeat"
	TypePatroniState       MessageType = "patroni.state"
	TypeServiceStateChange MessageType = "agent.service_state_change"
	TypeTaskAck            MessageType = "task.ack"
	TypeTaskResult         MessageType = "task.result"
	TypeSyncListing        MessageType = "sync.listing" // one piece of a folder listing (sync.list)
	TypeSyncChunk          MessageType = "sync.chunk"   // source → conductor → destinations (sync.send)
	TypeNetboxLog          MessageType = "netbox.log"
	TypeBackupEvent        MessageType = "backup.event" // a backup round's warning or error, sent as it happens

	// Server → Agent
	TypeServerHello  MessageType = "server.hello"
	TypeTaskDispatch MessageType = "task.dispatch"
)

// TaskType identifies the operation an agent should perform.
type TaskType string

const (
	TaskWriteConfig         TaskType = "config.write"
	TaskUpdateDBHost        TaskType = "config.update_db_host"
	TaskUpdateRedisHost     TaskType = "config.update_redis_host"
	TaskRedisSetRequirepass TaskType = "config.redis_requirepass"
	TaskRedisBindAll        TaskType = "config.redis_bind_all"
	TaskStartNetbox         TaskType = "service.start.netbox"
	TaskStopNetbox          TaskType = "service.stop.netbox"
	TaskRestartNetbox       TaskType = "service.restart.netbox"
	TaskRestartRQ           TaskType = "service.restart.rq"
	TaskStopPostgresql      TaskType = "service.stop.postgresql"
	TaskInstallPatroni      TaskType = "patroni.install"
	TaskCreatePgRole        TaskType = "postgres.create_role"
	TaskWritePatroniConf    TaskType = "patroni.write_config"
	TaskRestartPatroni      TaskType = "service.restart.patroni"
	TaskStopPostgres        TaskType = "postgres.stop"
	TaskPatroniSwitchover   TaskType = "patroni.switchover"
	TaskRestartRedis        TaskType = "service.restart.redis"
	TaskRestartSentinel     TaskType = "service.restart.redis-sentinel"
	TaskStopSentinel        TaskType = "service.stop.sentinel"
	TaskWriteSentinelConf   TaskType = "sentinel.write_config"
	TaskDBRestore           TaskType = "db.restore"  // reinitialize a replica or restore from backup
	TaskDBBackup            TaskType = "db.backup"   // pg_dump the primary database before destructive ops
	TaskRunCommand          TaskType = "exec.run"    // admin-only ad-hoc
	TaskReadNetboxConfig    TaskType = "config.read" // read /opt/netbox/.../configuration.py from agent

	// pgBackRest backup management
	TaskPGBackRestConfigure    TaskType = "pgbackrest.configure"     // write /etc/pgbackrest/pgbackrest.conf
	TaskPGBackRestStanzaCreate TaskType = "pgbackrest.stanza-create" // stanza-create + stanza-check (one-time bootstrap)
	TaskPGBackRestBackup       TaskType = "pgbackrest.backup"        // check + back up to each storage location in turn (full|diff|incr)
	TaskPGBackRestCatalog      TaskType = "pgbackrest.catalog"       // pgbackrest info --output=json
	TaskPGBackRestRestore      TaskType = "pgbackrest.restore"       // full cluster restore (stops Patroni, runs pgBackRest restore)
	TaskPGBackRestTestPath     TaskType = "pgbackrest.test_path"     // verify a posix backup path is writable by the postgres user

	// Folder sync (backups, media, extra folders); see internal/server/filesync.
	TaskSyncList    TaskType = "sync.list"    // list path/size/SHA-256 of every file, plus free space
	TaskSyncSend    TaskType = "sync.send"    // stream the named files as sync.chunk messages
	TaskSyncReceive TaskType = "sync.receive" // start accepting sync.chunk messages for a sync
	TaskSyncFinish  TaskType = "sync.finish"  // stop accepting chunks; report what arrived
	TaskSyncDelete  TaskType = "sync.delete"  // delete the named files
)

// SafeToRetryOnReconnect reports whether re-dispatching a task to an agent that
// previously had it "sent" but never returned a result is safe — i.e. whether
// running the operation a second time produces the same end state as running it
// once. Used by the reconnect handler: tasks that are NOT safe to retry are
// marked timeout instead of being re-sent, so the operator is alerted and the
// underlying work isn't accidentally duplicated.
//
// Idempotent: state-converging (start/stop/restart, write-config-file,
// host-update), read-only, or designed for repeat invocation (stanza-create
// is bootstrap-only but the agent guards against double-init).
//
// NOT idempotent: anything that does work twice if run twice (backups,
// pg_dump, full restore, exec.run admin commands).
func SafeToRetryOnReconnect(t TaskType) bool {
	switch t {
	case TaskPGBackRestBackup,
		TaskPGBackRestRestore,
		TaskDBBackup,
		TaskDBRestore,
		TaskRunCommand,
		// Sync tasks belong to one run, which the conductor fails when the agent disconnects.
		TaskSyncList,
		TaskSyncSend,
		TaskSyncReceive,
		TaskSyncFinish,
		TaskSyncDelete:
		return false
	}
	return true
}

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
	NodeID        string           `json:"node_id"`
	LoadAvg1      float64          `json:"load_avg_1"`
	LoadAvg5      float64          `json:"load_avg_5"`
	MemUsedPct    float64          `json:"mem_used_pct"`
	DiskUsedPct   float64          `json:"disk_used_pct"`
	NetboxRunning bool             `json:"netbox_running"`
	RQRunning     bool             `json:"rq_running"`
	NetboxVersion string           `json:"netbox_version,omitempty"` // e.g. "4.1.0"
	PatroniRole   string           `json:"patroni_role"`             // "primary", "replica", "standby_leader", ""
	PatroniLagB   *int64           `json:"patroni_lag_bytes,omitempty"`
	PatroniState  *json.RawMessage `json:"patroni_state,omitempty"` // full Patroni /patroni response

	// Service-level health indicators added in v0.1.1+.
	RedisRunning    bool   `json:"redis_running"`
	RedisRole       string `json:"redis_role,omitempty"` // "master" | "slave" | ""
	SentinelRunning bool   `json:"sentinel_running"`
	PatroniRunning  bool   `json:"patroni_running"`
	PostgresRunning bool   `json:"postgres_running"`

	// Tasks lists the IDs of the tasks the agent has received and not yet
	// reported, running or queued. A task gone from it without a result was lost.
	Tasks []string `json:"tasks"`
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
	// Repaired lists folders whose access the agent restored (e.g. after a
	// NetBox upgrade) since its previous task result, not only in this task.
	Repaired []string `json:"repaired,omitempty"`
}

// SyncListingPayload carries one piece of a sync.list result. A listing is
// split across messages because agent↔conductor messages are capped at 4 MB.
type SyncListingPayload struct {
	SyncID string     `json:"sync_id"`
	Files  []SyncFile `json:"files"`
}

// SyncFile is one regular file in a sync folder.
type SyncFile struct {
	Path   string `json:"path"` // relative to the sync folder, slash-separated
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	MTime  int64  `json:"mtime"` // modification time, Unix nanoseconds; picks the newest of two edits
}

// SyncChunkPayload carries one chunk of a file being synced. A file's chunks
// arrive in order; the last one has EOF set. Gone or Err end a file early.
type SyncChunkPayload struct {
	SyncID string `json:"sync_id"`
	Path   string `json:"path"`
	Index  int    `json:"index"`
	Data   []byte `json:"data,omitempty"` // JSON marshals as base64
	EOF    bool   `json:"eof,omitempty"`
	SHA256 string `json:"sha256,omitempty"` // with EOF: hash of all the data sent
	Gone   bool   `json:"gone,omitempty"`   // file vanished on the source since the listing
	Err    string `json:"err,omitempty"`    // source couldn't read the file
}

// SyncPartialSuffix marks a file still being received; it is renamed into
// place only once its hash matches.
const SyncPartialSuffix = ".nbc-partial"

// BackupEventPayload is a backup round's warning (a location's check failed;
// it is checked again) or error (a location's backup failed).
type BackupEventPayload struct {
	Repo    int    `json:"repo"`
	Error   bool   `json:"error"`
	Message string `json:"message"`
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

// RedisHostUpdateParams are the params for TaskUpdateRedisHost.
// The agent patches the Redis HOST lines in the REDIS dict in configuration.py.
// Used for active/standby clusters when the Patroni primary changes and
// app_tier_always_available=true, so all app-tier nodes point to the new primary's Redis.
type RedisHostUpdateParams struct {
	Host         string `json:"host"`          // new Redis HOST value (bare IP, no CIDR)
	RestartAfter bool   `json:"restart_after"` // restart netbox+netbox-rq after patching
}

// RedisRequirepassParams are the params for TaskRedisSetRequirepass.
// The agent sets requirepass on the local Redis instance and persists it via CONFIG REWRITE.
type RedisRequirepassParams struct {
	Password string `json:"password"` // value for requirepass
}

// ConfigWriteParams are the params for TaskWriteConfig.
type ConfigWriteParams struct {
	Content        string `json:"content"`
	Sha256         string `json:"sha256"`
	BackupExisting bool   `json:"backup_existing"`
	RestartAfter   bool   `json:"restart_after"`
}

// PatroniSwitchoverParams are the params for TaskPatroniSwitchover.
type PatroniSwitchoverParams struct {
	Candidate string `json:"candidate"` // hostname of the node that should become primary
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
	Method       string `json:"method"`          // "reinitialize" | "pitr"
	TargetTime   string `json:"target_time"`     // ISO8601 — used for pitr
	RestoreCmd   string `json:"restore_command"` // optional: override default restore command
	PatroniScope string `json:"patroni_scope"`   // cluster scope for patronictl
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

// ────────────────────────────────────────────────────────────────
// pgBackRest backup params
// ────────────────────────────────────────────────────────────────

// PGBackRestConfigParams are the params for TaskPGBackRestConfigure.
// The conductor renders pgbackrest.conf from the cluster's backup targets and
// pushes it to each node; the agent writes the file atomically.
type PGBackRestConfigParams struct {
	Config string `json:"config"` // rendered pgbackrest.conf content
	Sha256 string `json:"sha256"` // hex SHA-256 of Config for integrity check
}

// PGBackRestStanzaCreateParams are the params for TaskPGBackRestStanzaCreate.
// Run once after the first config push. Runs stanza-create then stanza-check.
type PGBackRestStanzaCreateParams struct {
	Stanza string `json:"stanza"` // pgBackRest stanza name (= Patroni scope)
}

// PGBackRestBackupParams are the params for TaskPGBackRestBackup: one round
// that backs up to each storage location once, after checking it can be read.
// A location whose check fails goes to the back of the queue, not to be checked
// again for CheckRetrySecs; after CheckTries failed checks its backup has failed.
// The output is a []PGBackRestBackupResult as JSON.
type PGBackRestBackupParams struct {
	Stanza            string `json:"stanza"`
	Type              string `json:"type"`  // "full" | "diff" | "incr"
	Repos             []int  `json:"repos"` // pgBackRest repos (storage locations), 1–4
	CheckTries        int    `json:"check_tries"`
	CheckTimeoutSecs  int    `json:"check_timeout_secs"`
	CheckRetrySecs    int    `json:"check_retry_secs"`
	BackupTimeoutSecs int    `json:"backup_timeout_secs"`
}

// PGBackRestRepoReadable reports whether a repo status code from `pgbackrest
// info` means the repo was read: 0 ok, or 2 "no valid backups" (a new
// location before its first backup).
func PGBackRestRepoReadable(code int) bool { return code == 0 || code == 2 }

// PGBackRestBackupResult is one location's outcome in a backup round.
type PGBackRestBackupResult struct {
	Repo  int    `json:"repo"`
	Error string `json:"error,omitempty"` // "" = backed up
}

// PGBackRestCatalogParams are the params for TaskPGBackRestCatalog.
// The agent runs `pgbackrest info --output=json` and returns the raw JSON output.
type PGBackRestCatalogParams struct {
	Stanza string `json:"stanza"`
}

// PGBackRestRestoreParams are the params for TaskPGBackRestRestore.
// The conductor stops all cluster nodes before dispatching this task.
// The agent orchestrates the full PostgreSQL recovery lifecycle.
type PGBackRestRestoreParams struct {
	Stanza     string `json:"stanza"`
	TargetTime string `json:"target_time"` // RFC3339 UTC
	DataDir    string `json:"data_dir"`    // PostgreSQL data directory (from Patroni config)
	RestoreCmd string `json:"restore_cmd"` // optional: override the default pgbackrest restore command
}

// PGBackRestTestPathParams are the params for TaskPGBackRestTestPath.
// The agent writes, reads back and removes a test file in Path: written and
// removed as postgres, read as the agent.
type PGBackRestTestPathParams struct {
	Path string `json:"path"` // absolute path of a local-disk storage location
}

// SyncListParams are the params for TaskSyncList. Root "" means the node's
// NetBox media folder. The task's output is a SyncListResult as JSON.
type SyncListParams struct {
	SyncID string `json:"sync_id"`
	Root   string `json:"root"`
}

// SyncListResult is the output of TaskSyncList.
type SyncListResult struct {
	Files     int    `json:"files"`      // entries sent in sync.listing messages
	FreeBytes uint64 `json:"free_bytes"` // space available to the agent in Root
}

// SyncSendParams are the params for TaskSyncSend.
type SyncSendParams struct {
	SyncID string   `json:"sync_id"`
	Root   string   `json:"root"`
	Paths  []string `json:"paths"` // sent in this order
}

// SyncReceiveParams are the params for TaskSyncReceive.
type SyncReceiveParams struct {
	SyncID   string `json:"sync_id"`
	Root     string `json:"root"`
	FileMode uint32 `json:"file_mode"` // os.FileMode for written files
	DirMode  uint32 `json:"dir_mode"`  // os.FileMode for created dirs
	// AsPostgres: Root is a pgBackRest repo; files are written as postgres
	// (through the agent's repo helper) and FileMode/DirMode are ignored.
	AsPostgres bool `json:"as_postgres,omitempty"`
}

// SyncFinishParams are the params for TaskSyncFinish. Output is a SyncFinishResult as JSON.
type SyncFinishParams struct {
	SyncID string `json:"sync_id"`
}

// SyncFinishResult is the output of TaskSyncFinish.
type SyncFinishResult struct {
	Copied  int      `json:"copied"`
	Skipped int      `json:"skipped"` // vanished on the source mid-sync
	Failed  []string `json:"failed"`  // "path: reason", capped
	Errors  int      `json:"errors"`  // total failures (Failed may be capped)
}

// SyncDeleteParams are the params for TaskSyncDelete.
type SyncDeleteParams struct {
	Root       string   `json:"root"`
	Paths      []string `json:"paths"`
	AsPostgres bool     `json:"as_postgres,omitempty"` // as in SyncReceiveParams
}
