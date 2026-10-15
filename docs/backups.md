# Backup Configuration

Netbox Conductor manages pgBackRest on every cluster node. Operators configure storage locations and schedules through the Backups tab in the cluster detail view; the conductor pushes configuration to nodes automatically.

---

## Storage Locations

Up to four storage locations can be configured per cluster (matching pgBackRest's repo1–repo4 limit). Every backup goes to every location, one location after another; each location's backup is its own entry in the backup runs, with its own success or failure and retries.

### Supported types

| Type | Description |
|------|-------------|
| **Local Disk** | A path on the node's filesystem. Works for NFS/SMB mounts — use a network path to make backups available from multiple nodes. |
| **Amazon S3** | AWS S3 or any S3-compatible service (MinIO, Wasabi, Backblaze B2). Supply a custom endpoint for non-AWS providers. |
| **Azure Blob Storage** | Azure storage account and container with an access key. |
| **Google Cloud Storage** *(future)* | GCS bucket with a service account JSON key. Not offered for new storage locations yet. |
| **SFTP Server** *(future)* | Remote server accessible via SSH key authentication. Not offered for new storage locations yet. |

### Recovery window

Each storage location has a **recovery window** (days): how long its full backups are kept (pgBackRest `repo{N}-retention-full-type=time`, `repo{N}-retention-full=<days>`). Differential and log snapshots are kept until the full backup they build on expires, and WAL back to the oldest kept full backup. pgBackRest also keeps the newest full backup older than the window, so point-in-time restore reaches back about the window plus one full-backup interval (with the weekly default, 14 days → about 21). Each location has its own window, e.g. 14 days on local disk and 30 in S3.

### Local disk path

The folder must exist on every database node, owned by `postgres` (`chown postgres:postgres`, `chmod 750`); `install.sh` prepares the default `/var/lib/postgresql/backups`. pgBackRest and the backup sync write it as `postgres`; the agent only reads it (it is in the `postgres` group).

**Test path** checks this on every connected database node: `postgres` writes, and later removes, a test file, and the agent reads it back and lists the folder. The same test also runs when the location is saved and at the start of every backup sync, and those results are kept: a node that fails gets event `NBC-CLU-009` and a ⚠ on the location, with the error and the commands that fix it; the next passing test clears it.

### Local disk sync

When using a local disk location, backups written on the primary node are not automatically available on replica nodes. Enable **Sync backups to other nodes** on the storage location and select which nodes should keep a copy. The conductor syncs the location after each of its backups, when a node settles, and when the primary changes. A node is *settled* once its agent has stayed connected for the failback timer's length (failover delay × failback multiplier); every sync leaves out nodes that aren't (event NBC-CLU-015), and they catch up when they settle:

1. WAL that another node archived while it was primary, and that the primary's copy is missing, is first copied to the primary (if the primary's retention still covers it), so a restore can reach any moment, including while that node was in charge.
2. Every selected node's copy then becomes identical to the primary's: new and changed files are copied, and backups and WAL pgBackRest expired on the primary are deleted. Backups another node took while it was primary are deleted too; restores still cover that time through its WAL.

See [File Sync](file-sync.md) for how it works and its safety rules. Sync applies only to local disk locations; S3 and Azure locations get their own backups.

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

The full and differential schedules are cron expressions in UTC, entered on the **Automatic Backups** card. See [Cron schedules](file-sync.md#cron-schedules) for the format; an invalid schedule is refused when you save it, with the reason.

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
- the default local backup folder `/var/lib/postgresql/backups` owned by `postgres:postgres`, mode `750`

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

## Backup Rounds and Failures

Each backup is one round on the primary that backs up to every storage location in turn:

- **Starting.** A scheduled round that can't start (no connected primary, the cluster busy with another operation, or the job couldn't be sent) is tried again 1 minute later, 3 tries in all (warning NBC-CLU-011 each time). After the third, it is skipped: its runs are marked **abandoned** (error NBC-CLU-012). A backup started by hand is tried once; the reason shows at once.
- **Checking each location.** Before its backup, each location is checked with `pgbackrest info` for that location (it must be readable and valid). A failed check (warning NBC-CLU-013) sends that location to the back of the queue, not checked again for 1 minute, while the other locations go ahead. After 3 failed checks, that location's backup has failed (error NBC-CLU-014).
- **Backing up.** A location that passes its check is backed up once. A failure (error NBC-CLU-014) is not retried until the next scheduled backup: a retry could keep filling an overfull disk while someone fixes it, and WAL keeps archiving, so restores still reach the present from the previous backup.

Only the locations backed up successfully are then synced. A round may take, per location, 3 checks of up to 60 seconds, 2 one-minute waits and a backup of up to an hour.

---

## pgBackRest Internals

All pgBackRest concepts (stanza, WAL, PITR, repo) are hidden from operators. The conductor derives the stanza name from the cluster's Patroni scope. Credentials stored in storage location configuration are encrypted at rest using the conductor's master key.
