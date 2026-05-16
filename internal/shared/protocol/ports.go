package protocol

// Network ports used by services managed by the conductor.
//
// These are the canonical values that the conductor (Go code) interacts with
// over HTTP/TCP. Note: YAML templates in internal/server/configgen and shell
// snippets in nodes.go also embed these numbers — when changing a value here,
// search the repo for the literal port number to keep templates in sync.
const (
	// PatroniRESTPort is Patroni's HTTP REST API port. Used for switchover,
	// failover, /config PATCH, /restart, /reinitialize, etc.
	PatroniRESTPort = 8008

	// PatroniRaftPort is the Patroni Raft (DCS) consensus port. Witness and
	// data nodes communicate on this port for leader election.
	PatroniRaftPort = 5433

	// RedisSentinelPort is the Redis Sentinel control port.
	RedisSentinelPort = 26379
)

// Stringified versions for use in "host:port" concatenation. Computed once so
// callers don't repeat strconv.Itoa(PatroniRaftPort) at every concat site.
const (
	PatroniRESTPortStr   = "8008"
	PatroniRaftPortStr   = "5433"
	RedisSentinelPortStr = "26379"
)
