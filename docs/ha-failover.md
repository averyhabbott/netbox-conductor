# High Availability & Failover

## Contents

- [Configure Failover](#configure-failover)
- [Patroni Raft Consensus](#patroni-raft-consensus)
- [Automatic Failover & Failback](#automatic-failover--failback)
- [App Tier Always Available](#app-tier-always-available)
- [Active/Standby Mode](#activestandby-mode)

---

## Configure Failover

The **Configure Failover** button on a cluster's Settings tab is the single-step HA setup. It:

1. Saves failover settings to the database
2. Auto-generates any missing credentials (postgres superuser, replication user, Patroni REST password, Redis password)
3. Starts the Conductor's built-in Patroni Raft witness subprocess — the third voter that gives 2-node clusters quorum without a dedicated witness VM
4. Optionally runs `pg_dump` on the primary database before making any changes
5. Stops NetBox on all non-primary nodes to prevent split-brain during reconfiguration
6. Renders and pushes `patroni.yml` to every node (with per-node Raft peer lists and witness address), then restarts Patroni on each node
7. If **App Tier Always Available** is enabled: renders and pushes Redis Sentinel config to every node and pre-seeds the correct `DATABASE.HOST` on all nodes
8. Marks the cluster as configured

### Primary node selection

When you click Configure Failover, the Conductor identifies the primary node using this priority chain:

1. **Explicit override** — `primary_node_id` in the request (selectable in the UI dropdown)
2. **Patroni state** — a node reporting `role=primary` in its last heartbeat
3. **Single running node** — if exactly one node reports `netbox_running=true`
4. **Highest failover priority** — among all connected nodes running NetBox, the one with the highest `failover_priority` value wins

---

## Patroni Raft Consensus

Patroni uses its built-in Raft DCS — no external etcd, Consul, or ZooKeeper is required.

**Witness process:** The Conductor spawns `patroni_raft_controller` (one subprocess per active/standby cluster) that participates in Raft voting but holds no data. This gives a 2-node cluster a 3rd voter on the conductor host, so either node can maintain quorum after a network partition without promoting a split-brain primary. The witness is also automatically recovered when the Conductor restarts — no manual trigger needed.

`patroni_raft_controller` is installed at `/opt/netbox-conductor/venv/bin/patroni_raft_controller` as part of the Patroni pip package. See [Installation](installation.md#3-set-up-the-patroni-witness-python-environment) for setup steps. If the witness process crashes it auto-restarts every 5 seconds; the current address is visible in the cluster's Patroni Topology view.

**`failsafe_mode: true`** is set in all generated configs — the primary continues serving if it loses contact with the standby, preventing a self-inflicted outage in a 2-node cluster with a temporary network hiccup.

---

## Automatic Failover & Failback

The failover manager runs on the Conductor and monitors agent heartbeats. It triggers when:

- An agent disconnects (agent_status → `disconnected`)
- A heartbeat reports `netbox_running=false` on the active node
- A node enters maintenance mode (immediate failover, no grace period)

### Grace period

By default, failover fires after **30 seconds** of the trigger condition persisting. This allows transient issues (agent restart, brief network blip) to self-heal without an unnecessary failover. The delay is configurable per cluster.

### Startup suppression

After the Conductor restarts, failover is suppressed for **90 seconds**. This prevents a mass-trigger when all agents reconnect simultaneously during a Conductor restart.

### Split-brain prevention

When the Conductor detects that NetBox should no longer run on a failed node, it queues a `stop.netbox` task for that node. The task executes the moment the agent reconnects — ensuring NetBox is stopped on the former primary even if the Conductor couldn't reach it during the outage.

### Auto-failback

When a higher-priority node reconnects and its heartbeat shows it is healthy, the Conductor moves NetBox back after one grace period. Failback can be disabled per cluster.

### Event history

Every failover, failback, and maintenance-triggered move is recorded in the cluster's **History** tab with: timestamp, event type, trigger reason, from/to node, and outcome.

---

## App Tier Always Available

When enabled, **all nodes always run NetBox** — there is no single "active" node. The load balancer steers using `GET /status` and Patroni handles the database primary election independently.

- All nodes' `configuration.py` points `DATABASE.HOST` at the current Patroni primary's IP
- When Patroni elects a new primary (reported via `patroni_state` in the agent heartbeat), the Conductor automatically dispatches `config.update_db_host` to all connected cluster nodes to update `DATABASE.HOST` and restart NetBox
- Redis Sentinel provides Redis HA; all nodes connect to the Sentinel master at port 26379

This mode is suitable when you want zero NetBox downtime during a database failover and your load balancer can handle routing to all healthy nodes simultaneously.

---

## Active/Standby Mode

When App Tier Always Available is disabled, exactly one node runs NetBox at a time. `configuration.py` on every node points `DATABASE.HOST` to `localhost` — each node connects to its own local PostgreSQL instance, and Patroni handles replication.

The load balancer steers traffic to the active node using `GET /status`. In active/standby mode the `/status` check is gated on Patroni primary status, so only the node with the writable PostgreSQL instance returns 200.
