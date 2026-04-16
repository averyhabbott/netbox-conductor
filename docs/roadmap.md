# Roadmap

Items are roughly ordered by priority.

## Near-term

| Item | Description |
|---|---|
| **Failover outcome verification** | After dispatching `start.netbox` to a candidate, confirm via heartbeat; retry next candidate if the first fails within a configurable window |
| **Restore from backup** | UI to trigger `pg_dump` restore from a previously saved backup file |
| **Patroni install UI** | Button on Node Detail to trigger `patroni.install` task; executor already implemented |

## Medium-term

| Item | Description |
|---|---|
| **NetBox upgrade orchestration** | One-click rolling upgrade: upgrade standby → validate → migrate primary → upgrade old primary |
| **Cluster-wide failover freeze** | Operator-toggled flag to suppress all automatic failovers during maintenance windows |
| **Failback safety checks** | Verify replica lag before failing back to a reconnected node |
| **Persistent failover history export** | CSV/JSON download of the failover events table |

## Long-term / Wishlist

| Item | Description |
|---|---|
| **OAuth2 / LDAP / SAML** | External identity provider support |
| **Playwright E2E tests** | Browser-level integration test suite |
| **Graceful shutdown drain** | Close WebSocket sessions cleanly on SIGTERM before process exit |
