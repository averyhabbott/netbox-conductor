# Backup Configuration

Netbox Conductor manages pgBackRest on every cluster node. Operators configure storage locations and schedules through the Backups tab in the cluster detail view; the conductor pushes configuration to nodes automatically.

---

## Storage Locations

Up to four storage locations can be configured per cluster (matching pgBackRest's repo1–repo4 limit).

### Supported types

| Type | Description |
|------|-------------|
| **Local Disk** | A path on the node's filesystem. Works for NFS/SMB mounts — use a network path to make backups available from multiple nodes. |
| **Amazon S3** | AWS S3 or any S3-compatible service (MinIO, Wasabi, Backblaze B2). Supply a custom endpoint for non-AWS providers. |
| **Google Cloud Storage** | GCS bucket with a service account JSON key. |
| **Azure Blob Storage** | Azure storage account and container with an access key. |
| **SFTP Server** | Remote server accessible via SSH key authentication. |

### Recovery window

Each storage location has a **recovery window** (days). This single value controls how far back point-in-time restore can go from that location. The conductor derives pgBackRest's `repo{N}-retention-full`, `repo{N}-retention-diff`, and WAL retention values automatically from this number.

### Local disk sync

When using a local disk location, backups written on the primary node are not automatically available on replica nodes. Enable **Sync backups to other nodes** on the storage location and select which nodes should receive a copy after each backup. The conductor relays repo files through its WebSocket connection after every successful backup.

For large databases (many GB), a shared storage location (cloud or NFS mount) is strongly recommended over local disk sync — sync throughput is limited by the conductor relay bandwidth.

If all storage locations are local disk with no sync configured, the conductor shows a warning: restoring from any node other than the original backup source requires manually copying the pgBackRest repository first.

---

## Backup Schedule

Three automatic backup tiers run on configurable schedules:

| Tier | Default schedule | Description |
|------|-----------------|-------------|
| **Full backup** | Weekly, Sunday 1:00 AM UTC | Complete backup of the entire database |
| **Daily snapshot** | Daily (Mon–Sat), 1:00 AM UTC | Incremental from the last full backup |
| **Log snapshot** | Every hour | Captures changes since the last snapshot |

Schedules are stored as cron expressions internally. The UI presents them as plain-language dropdowns; no cron syntax is exposed.

Disable the schedule toggle to pause all automatic backups. Manual backups can still be triggered with **Run full backup now**.

---

## Activating Backups

Click **Configure Backups** after adding at least one storage location. The conductor:

1. Pushes updated Patroni configuration to all nodes (enables WAL archiving via `archive_mode = on` and `archive_command`). If this is the first time enabling archiving, PostgreSQL restarts.
2. Pushes `pgbackrest.conf` to all nodes.
3. Runs `pgbackrest stanza-create` on the primary node.
4. Marks the cluster backup-ready once all steps complete.

**Prerequisites on each node** (handled by `install.sh` on fresh installs):
- pgBackRest installed (`apt-get install pgbackrest`)
- `/var/lib/pgbackrest` owned by `postgres:postgres`, mode `750`
- `/var/log/pgbackrest` owned by `postgres:postgres`, mode `770`
- `/etc/pgbackrest` owned by `netbox-agent:postgres`, mode `755`
- `netbox-agent` in the `postgres` supplementary group

---

## Backup History

Click **Refresh** in the Backup History card to fetch the current backup catalog from the primary node. The catalog is also refreshed automatically after every successful scheduled or manual backup.

The catalog shows:
- Oldest and most recent available restore points
- A list of recent backup entries (full, daily snapshot, log snapshot)

---

## Restore to a Previous Point

The **Restore to a previous point** button is enabled once a backup catalog is present.

1. Select a date and time within the available restore window. Times are shown in your browser's local timezone; the UTC equivalent is displayed as confirmation.
2. Check the acknowledgment box.
3. Click **Restore Database**.

The conductor:
1. Stops Patroni on all cluster nodes (prevents split-brain).
2. Runs `pgbackrest restore --type=time --target='<target>'` on the designated node.
3. Restarts PostgreSQL and waits for it to finish recovery.
4. Reinitializes each replica by running a fresh base backup from the restored primary.
5. Restarts Patroni on all nodes.

Automatic backups are paused while a restore is in progress and resume when the cluster is back online.

---

## Retry Logic

Scheduled backup tasks retry up to 10 times with a 5-minute delay between attempts. All attempts are tracked on a single row in the database — the attempt counter increments in place rather than creating new rows. If all 10 attempts fail, the run is marked **abandoned** and an alert fires via the configured alerting channel.

---

## pgBackRest Internals

All pgBackRest concepts (stanza, WAL, PITR, repo) are hidden from operators. The conductor derives the stanza name from the cluster's Patroni scope. Credentials stored in storage location configuration are encrypted at rest using the conductor's master key.
