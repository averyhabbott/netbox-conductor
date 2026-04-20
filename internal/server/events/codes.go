// Package events defines the event code constants and types for all
// NetBox Conductor structured events.  Every event emitted by the server
// carries one of these codes so operators can filter, alert, and document
// them precisely.
//
// Code format: NBC-{CAT}-{NNN}
//   NBC  = NetBox Conductor product prefix
//   CAT  = abbreviated category (CLU, AGT, SVC, HA, CFG)
//   NNN  = three-digit zero-padded numeric suffix
//
// Full reference: docs/event-codes.md
package events

// ─── Cluster events (NBC-CLU-*) ───────────────────────────────────────────────

const (
	CodeClusterCreated         = "NBC-CLU-001"
	CodeClusterDeleted         = "NBC-CLU-002"
	CodeClusterFailoverUpdated = "NBC-CLU-003"
	CodeNodeAdded              = "NBC-CLU-004"
	CodeNodeRemoved            = "NBC-CLU-005"
)

// ─── Agent events (NBC-AGT-*) ─────────────────────────────────────────────────

const (
	CodeAgentConnected    = "NBC-AGT-001"
	CodeAgentDisconnected = "NBC-AGT-002"
	CodeAgentRegistered   = "NBC-AGT-003"
	CodeAgentUpgraded     = "NBC-AGT-004"
)

// ─── Service events (NBC-SVC-*) ───────────────────────────────────────────────

const (
	CodeNetboxStarted    = "NBC-SVC-001"
	CodeNetboxStopped    = "NBC-SVC-002"
	CodeRQStarted        = "NBC-SVC-003"
	CodeRQStopped        = "NBC-SVC-004"
	CodePatroniStarted   = "NBC-SVC-005"
	CodePatroniStopped   = "NBC-SVC-006"
	CodePostgresReady    = "NBC-SVC-007"
	CodePostgresDown     = "NBC-SVC-008"
	CodeRedisStarted     = "NBC-SVC-009"
	CodeRedisStopped     = "NBC-SVC-010"
	CodeSentinelStarted  = "NBC-SVC-011"
	CodeSentinelStopped  = "NBC-SVC-012"
)

// ─── HA events (NBC-HA-*) ─────────────────────────────────────────────────────

const (
	CodeFailoverInitiated        = "NBC-HA-001"
	CodeFailoverCompleted        = "NBC-HA-002"
	CodeFailoverFailed           = "NBC-HA-003"
	CodeFailbackInitiated        = "NBC-HA-004"
	CodeFailbackCompleted        = "NBC-HA-005"
	CodePatroniRoleChanged       = "NBC-HA-006"
	CodeMaintenanceFailover      = "NBC-HA-007"
)

// ─── Config events (NBC-CFG-*) ────────────────────────────────────────────────

const (
	CodeCredentialRotated     = "NBC-CFG-001"
	CodeFailoverSettingsUpdated = "NBC-CFG-002"
	CodeNodeConfigUpdated     = "NBC-CFG-003"
	CodeConfigPushed          = "NBC-CFG-004"
	CodePatroniConfigured     = "NBC-CFG-005"
	CodeFailoverConfigured    = "NBC-CFG-006"
)
