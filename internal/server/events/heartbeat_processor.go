package events

import (
	"fmt"

	"github.com/google/uuid"
)

// HeartbeatState tracks the last known service state for a node.
// The AgentHandler keeps one per active WebSocket session.
type HeartbeatState struct {
	NetboxRunning   *bool
	RQRunning       *bool
	PatroniRunning  *bool
	PostgresRunning *bool
	RedisRunning    *bool
	SentinelRunning *bool
	PatroniRole     string
}

// HeartbeatInput is the subset of a heartbeat payload needed to derive events.
type HeartbeatInput struct {
	NodeID    uuid.UUID
	ClusterID uuid.UUID
	Hostname  string

	NetboxRunning   bool
	RQRunning       bool
	PatroniRunning  bool
	PostgresRunning bool
	RedisRunning    bool
	SentinelRunning bool
	PatroniRole     string
}

// Process compares a new heartbeat against the stored previous state for
// that node and returns any discrete state-change events that occurred.
// The caller must update prev with the new values after calling Process.
func Process(prev *HeartbeatState, hb HeartbeatInput) []Event {
	var evts []Event

	boolTransition := func(prev *bool, cur bool, startCode, stopCode, startMsg, stopMsg string) {
		if prev == nil {
			return // first heartbeat — no transition to report
		}
		if *prev == cur {
			return
		}
		if cur {
			evts = append(evts, New(CategoryService, SeverityInfo, startCode,
				fmt.Sprintf(startMsg, hb.Hostname), ActorSystem).
				Cluster(hb.ClusterID).Node(hb.NodeID).Build())
		} else {
			evts = append(evts, New(CategoryService, SeverityError, stopCode,
				fmt.Sprintf(stopMsg, hb.Hostname), ActorSystem).
				Cluster(hb.ClusterID).Node(hb.NodeID).Build())
		}
	}

	boolTransition(prev.NetboxRunning, hb.NetboxRunning,
		CodeNetboxStarted, CodeNetboxStopped,
		"NetBox started on %s", "NetBox stopped on %s")

	boolTransition(prev.RQRunning, hb.RQRunning,
		CodeRQStarted, CodeRQStopped,
		"RQ worker started on %s", "RQ worker stopped on %s")

	boolTransition(prev.PatroniRunning, hb.PatroniRunning,
		CodePatroniStarted, CodePatroniStopped,
		"Patroni started on %s", "Patroni stopped on %s")

	boolTransition(prev.PostgresRunning, hb.PostgresRunning,
		CodePostgresReady, CodePostgresDown,
		"PostgreSQL became ready on %s", "PostgreSQL became unavailable on %s")

	boolTransition(prev.RedisRunning, hb.RedisRunning,
		CodeRedisStarted, CodeRedisStopped,
		"Redis started on %s", "Redis stopped on %s")

	boolTransition(prev.SentinelRunning, hb.SentinelRunning,
		CodeSentinelStarted, CodeSentinelStopped,
		"Sentinel started on %s", "Sentinel stopped on %s")

	// Patroni role change (non-empty previous role required to avoid false positives on first beat).
	if prev.PatroniRole != "" && hb.PatroniRole != "" && prev.PatroniRole != hb.PatroniRole {
		evts = append(evts, New(CategoryHA, SeverityInfo, CodePatroniRoleChanged,
			fmt.Sprintf("Patroni role changed on %s: %s → %s", hb.Hostname, prev.PatroniRole, hb.PatroniRole),
			ActorSystem).
			Cluster(hb.ClusterID).Node(hb.NodeID).
			Meta("prev_role", prev.PatroniRole).
			Meta("new_role", hb.PatroniRole).
			Build())
	}

	return evts
}

// Update returns a HeartbeatState reflecting the new heartbeat values.
func (s *HeartbeatState) Update(hb HeartbeatInput) {
	nb := hb.NetboxRunning
	rq := hb.RQRunning
	pa := hb.PatroniRunning
	pg := hb.PostgresRunning
	rd := hb.RedisRunning
	sn := hb.SentinelRunning

	s.NetboxRunning = &nb
	s.RQRunning = &rq
	s.PatroniRunning = &pa
	s.PostgresRunning = &pg
	s.RedisRunning = &rd
	s.SentinelRunning = &sn
	s.PatroniRole = hb.PatroniRole
}
