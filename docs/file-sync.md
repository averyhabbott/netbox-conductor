# File Sync

The conductor keeps folders the same on several nodes by copying files through its own connections to the agents. Nodes never connect to each other. Three kinds of folder sync this way:

| Folder | How | Nodes | When |
| --- | --- | --- | --- |
| Backups (a local-disk storage location with **Sync backups to other nodes**) | one way: the other nodes become exact copies of the Patroni primary's folder, after the WAL only they hold is gathered to it | the primary and the nodes picked on the storage location | after each backup of that location, when a node settles, and when the primary changes (nodes that aren't settled are left out; see [Backups](backups.md#local-disk-sync)) |
| NetBox media (`<NETBOX_ROOT>/netbox/media`) | two way: a file added, changed or deleted on any node is added, changed or deleted on all of them | every connected app-tier or hyperconverged node (at least two) | on the cluster's sync schedule and on **Sync Now** |
| Extra sync folders | same as media | same as media | same as media |

Media and extra folders are set up on the cluster's **Settings → File Sync** card.

## What a sync does

1. Every node lists the folder: each file's path, size, SHA-256 checksum and modification time. Every file is read in full on every sync.
2. The conductor works out what each node needs (see below).
3. Each needed file is sent once, by a node that has the right version; the conductor passes it on to only the nodes that need it. Nothing is stored on the conductor except the record described below.
4. A receiving node writes each file under a temporary `.nbc-partial` name and moves it into place only once its checksum matches the sender's.
5. Deletes happen last, and only on a node that received every file it needed.

### Backups: gather, then one way

A backup sync first runs the storage path test on every node (see [Backups](backups.md#local-disk-path)); a node that fails is left out with an error. It then reads which WAL the primary's pgBackRest still keeps (`pgbackrest info`: the oldest kept WAL of each database, the "cutoff"). Then, in one run:

- **Gather.** WAL a picked node holds and the primary doesn't (archived while that node was primary) is copied to the primary, and to every other picked node, if it's at or after the cutoff. Timeline `.history` files are always gathered. Nothing on the primary is ever overwritten.
- **Spread.** Each picked node gets every file the primary has (plus what was gathered) that it's missing or has a different version of. Files only it has are deleted, including WAL older than the cutoff (pgBackRest expired it) and backups it took while it was primary.
- **Split brain.** If the same WAL segment, or `.history` file, exists with different contents on two nodes (two nodes were primary at once), it isn't gathered and no node's copy of it is changed or deleted; event `NBC-CLU-010` names it.

If the primary's folder is missing or empty, the sync stops with an error: an unmounted share is never mirrored onto the other nodes. pgBackRest's index files (`backup.info`, `archive.info` and their `.copy`) are copied after everything else, so a node never lists a backup it doesn't have yet. The sync runs only when exactly one connected node reports itself Patroni primary.

### Media and extra folders: two way

The conductor keeps a record, per folder and per node, of the files (path and checksum) each node had after its last successful sync. Each sync compares every node with its own record:

| On a node, compared with its record | Meaning | What happens |
| --- | --- | --- |
| not in the record | added there | copied to every node |
| in the record, checksum differs | changed there | that version is copied to every node |
| in the record, now missing | deleted there | deleted on every node |
| changed on two nodes | conflict | the version with the newest modification time is copied everywhere; event `NBC-CLU-008` |
| changed on one node, deleted on another | conflict | the change wins and is copied back; nothing is deleted; event `NBC-CLU-008` |

- **Deletes reach nodes that were offline.** A deleted file is remembered until every app-tier and hyperconverged node in the cluster has synced since, so a node that was off when the file was deleted deletes it when it's back.
- **A node that missed syncs catches up.** If nothing changed a file since the last sync but nodes hold different versions, the version on the node that synced most recently is copied to the others.
- **First sync is copy only.** A node with no record yet (a new node, a folder just added, or the first sync after upgrading) never causes deletes on other nodes. Files only it has are copied to the others; where the other nodes already agree on a file, their version is kept. It does receive deletes of files the cluster already deleted. When no node has a record, different versions of the same file are settled by newest modification time (`NBC-CLU-008` names them).
- **An empty or missing folder is left out** of that sync with an error for that node, so an unmounted share can never look like every file was deleted. Deleting the last file in a folder therefore doesn't sync.
- **A failed sync keeps the last good record** for that node. Files it did receive are recognised next time and not mistaken for changes.
- Removing a folder from the File Sync card (or turning extra folders off) forgets its record; adding it back starts over with a copy-only sync.

## Safety rules (all folders)

- A node without enough free disk space for the files it needs is skipped with an error.
- Only one sync of a folder runs at a time per cluster. A scheduled or manual media sync that finds one running is skipped. A backup sync is queued and starts when the running one finishes.
- Symlinks are skipped, never followed.

Each receiving node's result is recorded as an event: `NBC-CLU-006` (finished, with the number of files copied and deleted) or `NBC-CLU-007` (failed, with the reason). A failed sync changes nothing that was already in place, and the next sync picks up where it left off.

## Sync schedule

Media and extra folders always sync when **Sync Now** is clicked. To also sync them automatically, turn on **Sync on a Schedule** on the File Sync card (off by default). The schedule field is filled in with `*/30 * * * *` (every 30 minutes). It takes a standard 5-field cron expression (minute hour day month weekday) in UTC, the same format as the backup schedule; for example `0 * * * *` is every hour on the hour. Turning the schedule off keeps the expression for next time.

### Cron schedules

The file sync and backup schedules use the same 5-field format: `minute hour day month weekday`, in UTC.

| Field | Values |
| --- | --- |
| minute | 0–59 |
| hour | 0–23 |
| day | 1–31 |
| month | 1–12 |
| weekday | 0–7 (0 and 7 are both Sunday) |

Each field takes `*` (any), a number, a range (`1-5`), a step (`*/15`, `0-30/10`, or `5/20` = 5, 25, 45) or a comma list of these (`0,30`). Names such as `MON` or `JAN` aren't supported. When both day and weekday are set, both must match (standard cron runs when either matches). A schedule that isn't valid is refused when you save it, with the reason, so a saved schedule always runs.

Backup folders don't use this schedule: they sync after each backup, when a node settles, and when the primary changes.

## Preparing folder permissions

The agent runs as the `netbox-agent` user. It must be able to read every file in a synced folder on the source, and create, replace and delete files on destinations. NetBox (the `netbox` user) must still be able to read and delete files the agent copies in.

On each app-tier or hyperconverged node, after the folders are configured on the File Sync card, run:

```bash
sudo netbox-agent check-sync-permissions
```

It fetches the media root and extra folders from the conductor and gives `netbox-agent` and `netbox` read/write access to each folder and everything in it, using POSIX ACLs. It also sets default ACLs, so files and folders created later get the same access. The change takes effect immediately; the agent doesn't need a restart. Run it again after adding a folder.

It needs the `setfacl` command from the `acl` package (`install.sh` installs it):

```bash
apt-get install acl     # Debian/Ubuntu
dnf install acl         # RHEL/Fedora
```

To do the same by hand for a folder:

```bash
setfacl -R -m u:netbox-agent:rwX,d:u:netbox-agent:rwX,u:netbox:rwX,d:u:netbox:rwX /path/to/folder
getfacl /path/to/folder    # check
```

`rwX` gives execute permission only on folders (and on files that are already executable).

`netbox-agent` also needs to be able to enter every parent folder of a synced folder (execute permission). Standard paths such as `/opt/netbox/netbox/media` already allow this.

**Network shares (NFS, SMB):** ACLs set on the client usually don't apply. Grant `netbox-agent` read/write access on the file server instead, or don't sync a folder that is already shared between the nodes.

The backup folder doesn't use this command. The agent writes and deletes backup files as `postgres`, through its own helper (`sudo -u postgres netbox-agent repo ...`, allowed in its sudoers file, refusing any folder that isn't a local-disk location in `pgbackrest.conf`), and reads them as a member of the `postgres` group. `install.sh` prepares the default local backup path (`/var/lib/postgresql/backups`, owner `postgres:postgres`, mode `750`). A different local backup path needs the same on every node; **Test path** checks it.
