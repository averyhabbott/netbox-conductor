// Package nodestate provides helpers for computing node health and state
// from live agent data. These helpers are shared between the cluster status
// API handler and the failover manager.
package nodestate

import "encoding/json"

// ExtractPatroniRole parses a Patroni state JSON blob and returns the node's
// Patroni role string (e.g. "primary", "replica"). Returns "" if state is nil
// or unparseable.
func ExtractPatroniRole(state json.RawMessage) string {
	if state == nil {
		return ""
	}
	var ps map[string]any
	if err := json.Unmarshal(state, &ps); err != nil {
		return ""
	}
	role, _ := ps["role"].(string)
	return role
}

// ComputeNodeHealth returns "healthy", "unhealthy", or "offline" for a node
// given its role, agent connectivity, service running flags, and Patroni role.
//
// Health rules:
//   - Agent disconnected → "offline"
//   - HC active (NetBox running): needs RQ running and, if Patroni configured,
//     Patroni must be primary/master → "unhealthy" otherwise
//   - HC standby (NetBox stopped): if Patroni configured and node is primary
//     with no app → "unhealthy"
//   - app active: needs RQ running → "unhealthy" if not
//   - app standby: always "healthy" (connected, waiting)
//   - db_only: needs a valid Patroni role when Patroni is configured
func ComputeNodeHealth(role, agentStatus string, netboxRunning, rqRunning *bool, patroniRole string, patroniConfigured bool) string {
	if agentStatus != "connected" {
		return "offline"
	}
	nb := netboxRunning != nil && *netboxRunning
	rq := rqRunning != nil && *rqRunning
	isPrimary := patroniRole == "primary" || patroniRole == "master"
	isReplica := patroniRole == "replica" || patroniRole == "standby"

	switch role {
	case "hyperconverged":
		if nb {
			if !rq {
				return "unhealthy"
			}
			if patroniConfigured && patroniRole != "" && !isPrimary {
				return "unhealthy"
			}
			return "healthy"
		}
		// standby: NetBox not running
		if patroniConfigured && patroniRole != "" && isPrimary {
			return "unhealthy"
		}
		return "healthy"
	case "app":
		if nb && !rq {
			return "unhealthy"
		}
		return "healthy"
	case "db_only":
		if patroniConfigured {
			if patroniRole == "" {
				return "unhealthy"
			}
			if !isPrimary && !isReplica {
				return "unhealthy"
			}
		}
		return "healthy"
	}
	return "healthy"
}

// ComputeNodeState returns "active", "standby", or "" for a node.
//
// For hyperconverged/app nodes: active = NetBox running, standby = not running.
// For db_only nodes: active = Patroni primary/master, standby = replica.
// Returns "" for db_only when Patroni is not configured.
func ComputeNodeState(role string, netboxRunning *bool, patroniRole string, patroniConfigured bool) string {
	switch role {
	case "hyperconverged", "app":
		if netboxRunning != nil && *netboxRunning {
			return "active"
		}
		return "standby"
	case "db_only":
		if !patroniConfigured {
			return ""
		}
		if patroniRole == "primary" || patroniRole == "master" {
			return "active"
		}
		return "standby"
	}
	return ""
}

// AggregateClusterHealth derives cluster-level health from per-node health/state/role slices.
//
// Only app-tier nodes (hyperconverged and app) contribute — db_only nodes are excluded.
//
// Results:
//   - "healthy":  at least one healthy active node AND all standbys are healthy
//   - "degraded": a healthy active node exists but at least one standby is unhealthy/offline
//   - "down":     no healthy active node exists
func AggregateClusterHealth(nodeHealths, nodeStates, nodeRoles []string) string {
	hasHealthyActive := false
	allStandbysHealthy := true
	standbyCount := 0
	for i, role := range nodeRoles {
		if role == "db_only" {
			continue
		}
		if nodeStates[i] == "active" && nodeHealths[i] == "healthy" {
			hasHealthyActive = true
		} else if nodeStates[i] == "standby" {
			standbyCount++
			if nodeHealths[i] != "healthy" {
				allStandbysHealthy = false
			}
		}
	}
	if !hasHealthyActive {
		return "down"
	}
	if standbyCount > 0 && !allStandbysHealthy {
		return "degraded"
	}
	return "healthy"
}
