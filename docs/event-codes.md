# NetBox Conductor Event Codes

Every structured event emitted by the conductor carries a code in the format **NBC-{CAT}-{NNN}**.

- **NBC** — NetBox Conductor product prefix
- **CAT** — abbreviated category (CLU, AGT, SVC, HA, CFG)
- **NNN** — three-digit zero-padded numeric suffix

Events are stored in the `events` table and can be queried via `GET /api/v1/events?code=NBC-HA-001`.

---

## Cluster Events (NBC-CLU-*)

| Code | Severity | Description | Remediation |
|---|---|---|---|
| NBC-CLU-001 | info | Cluster created | — |
| NBC-CLU-002 | warn | Cluster deleted | Verify all agents have been cleanly deregistered. |
| NBC-CLU-003 | info | Cluster failover settings updated | Review new settings in the cluster detail page. |
| NBC-CLU-004 | info | Node added to cluster | Complete cluster setup via Configure Failover if applicable. |
| NBC-CLU-005 | warn | Node removed from cluster | Ensure remaining nodes have quorum if Patroni is active. |

---

## Agent Events (NBC-AGT-*)

| Code | Severity | Description | Remediation |
|---|---|---|---|
| NBC-AGT-001 | info | Agent connected | — |
| NBC-AGT-002 | warn | Agent disconnected | Check the node is reachable and the agent service is running. |
| NBC-AGT-003 | info | Agent registered (new node) | Complete node setup (credentials, Configure Failover) if required. |
| NBC-AGT-004 | info | Agent upgraded | Verify the new version appears in the node detail page. |

---

## Service Events (NBC-SVC-*)

These events are derived from consecutive heartbeat comparisons and from explicit `agent.service_state_change` messages.

| Code | Severity | Description | Remediation |
|---|---|---|---|
| NBC-SVC-001 | info | NetBox started | — |
| NBC-SVC-002 | error | NetBox stopped | Check `systemctl status netbox` on the affected node. |
| NBC-SVC-003 | info | RQ worker started | — |
| NBC-SVC-004 | error | RQ worker stopped | Check `systemctl status netbox-rq`. Background tasks will not run until RQ is restored. |
| NBC-SVC-005 | info | Patroni started | — |
| NBC-SVC-006 | error | Patroni stopped | Patroni manages PostgreSQL HA. Investigate immediately if this is a primary node. |
| NBC-SVC-007 | info | PostgreSQL became ready | — |
| NBC-SVC-008 | error | PostgreSQL became unavailable | Check `systemctl status postgresql` and Patroni logs. |
| NBC-SVC-009 | info | Redis started | — |
| NBC-SVC-010 | error | Redis stopped | NetBox session handling and caching will degrade. |
| NBC-SVC-011 | info | Sentinel started | — |
| NBC-SVC-012 | error | Sentinel stopped | Redis high-availability is impaired until Sentinel is restored. |

---

## HA / Failover Events (NBC-HA-*)

| Code | Severity | Description | Remediation |
|---|---|---|---|
| NBC-HA-001 | warn | Failover initiated | Monitor for NBC-HA-002 or NBC-HA-003 to confirm outcome. |
| NBC-HA-002 | info | Failover completed successfully | — |
| NBC-HA-003 | error | Failover failed | Review task logs. Manual intervention may be required. |
| NBC-HA-004 | info | Failback initiated | — |
| NBC-HA-005 | info | Failback completed | — |
| NBC-HA-006 | info | Patroni role changed | No action required unless the new role is unexpected. |
| NBC-HA-007 | warn | Maintenance failover initiated | Triggered by enabling maintenance mode on the primary. Verify the standby took over. |

---

## Config Events (NBC-CFG-*)

| Code | Severity | Description | Remediation |
|---|---|---|---|
| NBC-CFG-001 | warn | Credentials rotated | Confirm all nodes receive the updated credentials via a config push. |
| NBC-CFG-002 | info | Failover settings updated | — |
| NBC-CFG-003 | info | Node configuration updated | If identity fields (IP, role) changed, re-run Configure Failover. |
| NBC-CFG-004 | info | Config pushed to node | — |
| NBC-CFG-005 | info | Patroni reconfigured | — |
| NBC-CFG-006 | info | Failover configured | — |
