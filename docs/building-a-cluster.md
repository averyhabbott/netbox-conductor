# Building a Cluster

## 1. Picking a Cluster Type

When creating a cluster you choose between two modes: **Active/Standby** and **HA**.

**Active/Standby** is the production-ready mode. Exactly one node runs NetBox at any given time — the others are on standby. When the active node becomes unreachable, the conductor promotes the next eligible node. The database layer runs Patroni with standard primary/replica replication across all nodes. This is the only mode that currently supports automatic failover, failback, and backup scheduling.

**HA** mode is reserved for a future all-active topology where every node runs NetBox simultaneously. It is not yet implemented — creating an HA cluster will let you add nodes and manage credentials, but the failover, backup, and configure-failover features will be unavailable. Do not use HA mode for production clusters.

**Config concern:** Mode is permanent. There is no migration path from Active/Standby to HA or back — changing mode would require rebuilding the cluster.

---

## 2. Adding Nodes

A cluster needs at least two nodes before you can run Configure Failover. Add nodes from the cluster detail page; each node requires an IP address and a role.

### Node Roles

**Hyperconverged** runs the full stack: NetBox, RQ workers, PostgreSQL (via Patroni), and Redis Sentinel. This is the standard role for most deployments. Hyperconverged nodes are eligible to become the active NetBox host and are also valid backup sources.

**App** runs only NetBox and RQ — no database. Use this role to add web/worker capacity that connects to a remote database node. App nodes participate in failover for the application tier but never host the database primary.

**db_only** runs only PostgreSQL, Patroni, and Redis — no NetBox. Use this as a dedicated database node, most commonly as a third node to provide Raft quorum without running a full NetBox instance. db_only nodes are never started as the active NetBox host and are excluded from app-tier failover.

**Risk:** A cluster with only db_only nodes and no hyperconverged or app nodes can replicate data but can never serve NetBox traffic. At least one hyperconverged node is required for the cluster to be functional.

### Node Priority

Every node gets a `failover_priority` integer — higher means preferred. Priority is assigned automatically in increment order when nodes are added, but can be edited at any time.

Priority determines which standby node is promoted when the active node fails. The failover manager selects the connected node with the highest priority that is not in maintenance mode and not suppressed. Among nodes at the same priority tier, a node already running NetBox is preferred over one that isn't.

Priority also drives failback: when a higher-priority node reconnects after being offline, the conductor arms a timer (`failback_multiplier × failover_delay_secs`) and moves NetBox back to it once the timer expires.

**Config concern:** Priority does not control which node becomes the Patroni database primary — Patroni uses Raft consensus for that independently. If `app_tier_always_available` is enabled, the conductor will request a Patroni switchover to try to align the database primary with the active NetBox node, but this is best-effort.

**Risk:** Setting two nodes to the same priority is valid but means failover target selection becomes arbitrary among them. Give your preferred standby a distinctly higher priority than others.

---

## 3. Importing and Generating Credentials

The conductor manages seven credential types for each cluster:

| Credential | Used For |
|---|---|
| `postgres_superuser` | PostgreSQL root access |
| `postgres_replication` | Patroni replication user |
| `netbox_db_user` | NetBox application database user |
| `patroni_rest_password` | Patroni REST API auth |
| `redis_tasks_password` | Redis `requirepass` and both task queue and caching auth |
| `netbox_secret_key` | NetBox `SECRET_KEY` |
| `netbox_api_token_pepper` | NetBox API token pepper |

**Generate** creates secure random passwords for any credential types that are not yet set. Generated passwords are returned in plaintext exactly once — copy them before dismissing the dialog, they cannot be recovered afterward. The conductor stores only the encrypted form.

**Import** lets you provide existing passwords — useful when taking over a cluster that already has PostgreSQL users established, or when your organization has a password management policy that pre-generates credentials.

**Expected outcome:** All seven credential types should show as set before running Configure Failover. Configure Failover will auto-generate any missing credentials, but doing it explicitly first gives you the opportunity to record them.

**Risk:** If you import a `postgres_superuser` or `postgres_replication` password that does not match what PostgreSQL currently has on the primary node, Configure Failover will fail when it tries to create or verify those roles. The create-role task will return an authentication error and the operation will stop before Patroni is configured.

**Config concern:** Credentials are encrypted at rest using a per-cluster AES-256 key. They are decrypted only when rendering configuration files to push to nodes — they are never included in API responses after initial entry.

---

## 4. Config Sync (Configure Failover)

Configure Failover is a single operation that bootstraps the full HA stack on the cluster. It must be run after nodes are added and credentials are set. You can re-run it at any time to update settings or add newly joined nodes — it is designed to be idempotent.

**Prerequisites:**
- Cluster mode is `active_standby`
- At least 2 nodes exist
- At least one node is connected and running NetBox (or already reporting as Patroni primary)
- All credentials set (or you accept that Configure Failover will auto-generate missing ones)

**What happens, in order:**

1. **Settings saved** — Failover parameters (delays, multipliers, VIP, sentinel master name) are written to the cluster record.

2. **Missing credentials generated** — Any of the four required credential types (superuser, replication, patroni REST, redis tasks) that are absent are auto-generated and encrypted.

3. **Primary node identified** — The conductor picks the primary using this precedence: explicit override you provided → node already reporting `patroni role=primary` → the single node running NetBox → highest-priority connected node running NetBox.

4. **Raft witness started** — The conductor starts a local Patroni witness subprocess. This provides the Raft quorum vote from the conductor host, allowing a two-node cluster to achieve consensus without a third physical node.

5. **Non-primary NetBox stopped** — `stop netbox` is dispatched to all nodes except the primary. This prevents split-brain while Patroni takes over database management.

6. **PostgreSQL roles created on primary** — The replication and superuser accounts are created or updated against the still-running PostgreSQL instance before Patroni restarts it.

7. **Optional safety backup** — If `save_backup` is checked, a `pg_dump` runs on the primary before any further changes. Recommended on first-time configure on a cluster with existing data.

8. **Patroni installed, configured, and restarted on primary** — Config is rendered from the cluster settings and credentials, written to `/etc/patroni/patroni.yml`, and Patroni is restarted. Patroni takes over management of PostgreSQL from this point forward.

9. **Redis Sentinel stopped and disabled** — Sentinel is not used in Active/Standby mode. The `redis-sentinel` service is stopped and disabled on all nodes to prevent it from interfering with Redis.

10. **Redis auth configured** — `redis-cli CONFIG SET requirepass` is called on each node, then `CONFIG REWRITE` persists it to `/etc/redis/redis.conf`. The `redis_tasks_password` credential is used. This runs before `configuration.py` is pushed so that Redis and the application config are in sync when services start.

11. **NetBox configuration pushed** — A full `configuration.py` is rendered with all credentials and database connection info and pushed to each node. Services are restarted immediately after the config is written.

12. **Replicas configured asynchronously** — In a background goroutine, the conductor polls the primary's Patroni REST API until it confirms leader status (up to 90 seconds), then dispatches the same Patroni → Sentinel → Config sequence to each replica node.

**Expected outcome:** The HTTP response returns immediately with a list of dispatched task IDs and their statuses. The primary node completes its sequence in approximately 2–5 minutes. Replicas follow once the primary has elected itself Patroni leader. You can monitor progress in the DB Events tab.

**Risks:**
- If the primary node goes offline mid-sequence, tasks are queued and will execute when it reconnects. The cluster may be in a partially configured state in the meantime.
- If credential generation fails silently, downstream config rendering will produce invalid files. Check the `warnings` list in the response.
- Replicas that are offline at configure time will receive their configuration tasks when they reconnect — they do not block the operation.

**Config concerns:**
- The `failover_delay_secs` setting (minimum 10, default 30) controls how long a node must be unreachable before failover triggers. Setting this too low on a flaky network causes unnecessary failovers.
- `failback_multiplier` (default 3) means a node must be reconnected and stable for `3 × failover_delay_secs` before failback occurs. Increase this if you see flip-flop behavior on unstable links.
- `app_tier_always_available` keeps NetBox running on all nodes at all times rather than only on the active node. This requires all nodes to be able to reach the database primary, and the conductor will attempt to co-locate the Patroni primary with the active NetBox node via switchover requests.

---

## 5. Configure Backup Jobs

Backups require Patroni to be configured first — WAL archiving is injected into the Patroni config, and pgBackRest must be installed on all database nodes (this is handled by the agent installer).

### Step 1: Add a Storage Location

Add at least one storage location from the Backups tab. Up to four locations can be configured per cluster (they map to pgBackRest's `repo1`–`repo4`).

| Storage Type | When to Use |
|---|---|
| Local Disk | NFS or SMB mount that all nodes share; or local disk with sync enabled |
| Amazon S3 | AWS S3 or any S3-compatible service (MinIO, Wasabi, Backblaze B2 via custom endpoint) |
| Google Cloud Storage | GCS bucket with service account key |
| Azure Blob Storage | Azure storage account and container |
| SFTP Server | Remote server accessible via SSH key auth |

For each location, set retention:
- **Keep N full backups** — how many weekly full backups to retain (default: 2)
- **Keep daily snapshots for N days** — differential backup retention (default: 7)
- **Keep backup history for N days** — how far back WAL replay can reach (default: 14)

**Config concern:** Local disk storage only protects against the database crashing — it does not protect against the node itself being lost. Use a network-mounted path or cloud storage if you need to be able to restore to a different node. If you use local disk without sync enabled, a warning will appear: restoring from that location requires the same physical server to be available.

**Risk:** Cloud storage credentials (S3 keys, GCS service account JSON, Azure key) are stored encrypted in the conductor database and pushed to nodes in plaintext inside pgbackrest.conf. Anyone with shell access to a configured node can read them. Scope credentials to write-only or backup-only permissions in your cloud provider.

### Step 2: Enable Backups

Click "Enable Backups." This runs a three-step bootstrap sequence:

1. **pgbackrest.conf pushed to all database nodes** — The conductor renders the config from your storage locations and credentials and dispatches it to every `hyperconverged` and `db_only` node.

2. **Stanza created on primary** — `pgbackrest stanza-create` is run on the primary node, initializing the backup repository at the storage location. This also runs `stanza-check` to verify the connection to storage. The stanza name is derived from the cluster's Patroni scope and is locked once the first backup runs — changing it would orphan all existing backups.

3. **`stanza_initialized` marked** — Once stanza creation succeeds, the cluster is marked backup-ready and the scheduler will begin honoring the backup schedule.

**Expected outcome:** Both tasks (configure push + stanza create) should complete within about 2 minutes for local and cloud storage. SFTP may take longer depending on network latency.

**Risks:**
- If pgBackRest is not installed on a node, the push task will fail. The agent installer handles this, but manually provisioned nodes may be missing it.
- If storage credentials are wrong (bad S3 key, wrong bucket name), stanza creation will fail with an access error. Fix the credentials and re-run Enable Backups.
- Stanza creation is idempotent — re-running Enable Backups on an already-initialized cluster is safe.

### Step 3: Configure the Schedule

Set the automatic backup schedule:

- **Full backup** — Runs a complete backup of the entire database. Default: Sundays at 1:00 AM UTC. These are the largest and slowest backups but the most complete restore points.
- **Daily snapshot** — A differential backup capturing only changes since the last full backup. Default: Monday–Saturday at 1:00 AM UTC.
- **Log snapshot** — Incremental captures at a regular interval. Default: every 1 hour. These are very fast and enable fine-grained point-in-time restore.

**Expected outcome:** The scheduler checks every minute. When a tier is due, it dispatches a backup task to the current Patroni primary. Completed backups appear in the backup history; failed backups are retried up to 3 times at 5-minute intervals before being marked abandoned and surfacing an alert.

**Config concern:** The schedule runs in UTC. If your organization expects backups at a specific local time, convert to UTC when setting the schedule — particularly around DST transitions.

**Risk:** Backups only dispatch to the Patroni primary. If the primary is offline or no primary has been elected (e.g., during a failover), the scheduled backup is skipped and a retry is queued. The cluster must have an elected Patroni primary for backups to run.
