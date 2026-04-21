import client from './client'
import type { Event } from './events'

export interface Cluster {
  id: string
  name: string
  description: string
  mode: 'active_standby' | 'ha'
  auto_failover: boolean
  auto_failback: boolean
  app_tier_always_available: boolean
  failover_on_maintenance: boolean
  failover_delay_secs: number
  failback_multiplier: number
  vip?: string
  patroni_scope: string
  netbox_version: string
  media_sync_enabled: boolean
  extra_folders_sync_enabled: boolean
  extra_sync_folders: string[]
  patroni_configured: boolean
  redis_sentinel_master: string
  created_at: string
  updated_at: string
}

export interface ConfigureFailoverBody {
  auto_failover: boolean
  auto_failback: boolean
  app_tier_always_available: boolean
  failover_on_maintenance: boolean
  failover_delay_secs: number
  failback_multiplier: number
  vip?: string | null
  redis_sentinel_master: string
  save_backup: boolean
  primary_node_id?: string
}

export interface ConfigureFailoverTaskRef {
  node_id: string
  hostname: string
  task_id?: string
  status: string
  error?: string
}

export interface ConfigureFailoverResult {
  cluster_id: string
  witness_addr: string
  primary_node: string
  started_at: string
  backup_task?: ConfigureFailoverTaskRef
  stop_tasks: ConfigureFailoverTaskRef[]
  patroni_tasks: ConfigureFailoverTaskRef[]
  sentinel_tasks: ConfigureFailoverTaskRef[]
  netbox_restart_task?: ConfigureFailoverTaskRef
  warnings: string[]
}

export interface ClusterSyncResult {
  source_node_id: string
  source_hostname: string
  syncs: {
    target_node_id: string
    target_hostname: string
    transfer_id: string
    task_id: string
    source_path?: string
  }[]
}

export interface CreateClusterBody {
  name: string
  description?: string
  mode: 'active_standby' | 'ha'
  patroni_scope?: string
  netbox_version?: string
}

export interface ClusterStatus {
  cluster_id: string
  nodes: {
    node_id: string
    hostname: string
    agent_status: string
    netbox_running: boolean | null
    rq_running: boolean | null
    patroni_role: string
    last_seen_at?: string
  }[]
}

export interface FailoverEvent {
  id: string
  cluster_id: string
  event_type: 'failover' | 'failback' | 'maintenance_failover'
  trigger: 'disconnect' | 'heartbeat' | 'maintenance' | 'reconnect'
  failed_node_id?: string
  failed_node_name: string
  target_node_id?: string
  target_node_name: string
  success: boolean
  reason?: string
  occurred_at: string
}

// ─── Backup types ────────────────────────────────────────────────────────────

export type BackupTargetType = 'posix' | 's3' | 'gcs' | 'azure' | 'sftp'

export interface BackupTarget {
  id: string
  cluster_id: string
  repo_index: number
  label: string
  target_type: BackupTargetType
  recovery_days: number
  sync_to_nodes: string[]
  created_at: string
  updated_at: string
  // posix
  posix_path?: string
  // s3
  s3_bucket?: string
  s3_region?: string
  s3_endpoint?: string
  s3_key_id_set?: boolean
  s3_secret_set?: boolean
  // gcs
  gcs_bucket?: string
  gcs_key_set?: boolean
  // azure
  azure_account?: string
  azure_container?: string
  azure_key_set?: boolean
  // sftp
  sftp_host?: string
  sftp_port?: number
  sftp_user?: string
  sftp_path?: string
  sftp_private_key_set?: boolean
}

export interface BackupSchedule {
  cluster_id: string
  enabled: boolean
  full_backup_cron: string
  diff_backup_cron: string
  incr_backup_interval_hrs: number
  stanza_name?: string
  stanza_initialized: boolean
  first_backup_run: boolean
  restore_in_progress: boolean
  updated_at: string
}

export interface BackupCatalogEntry {
  type: 'full' | 'diff' | 'incr'
  label: string
  started_at: string
  finished_at: string
}

export interface BackupConfig {
  cluster_id: string
  targets: BackupTarget[]
  schedule: BackupSchedule | null
  cached_catalog?: {
    id: string
    cluster_id: string
    fetched_at: string
    oldest_restore_point?: string
    newest_restore_point?: string
    backups: BackupCatalogEntry[]
  }
}

export interface BackupRun {
  id: string
  cluster_id: string
  backup_type: 'full' | 'diff' | 'incr'
  task_id?: string
  attempt: number
  status: 'pending' | 'running' | 'success' | 'failed' | 'abandoned'
  scheduled_at: string
  dispatched_at?: string
  completed_at?: string
  retry_after?: string
  error_message?: string
}

export interface BackupCatalog {
  id: string
  cluster_id: string
  fetched_at: string
  catalog_json: unknown
  oldest_restore_point?: string
  newest_restore_point?: string
}

export interface BackupTaskRef {
  node_id: string
  hostname: string
  task_id?: string
  status: string
  error?: string
}

export interface CreateBackupTargetBody {
  label: string
  target_type: BackupTargetType
  recovery_days?: number
  sync_to_nodes?: string[]
  // posix
  posix_path?: string
  // s3
  s3_bucket?: string
  s3_region?: string
  s3_endpoint?: string
  s3_key_id?: string
  s3_secret?: string
  // gcs
  gcs_bucket?: string
  gcs_key?: string
  // azure
  azure_account?: string
  azure_container?: string
  azure_key?: string
  // sftp
  sftp_host?: string
  sftp_port?: number
  sftp_user?: string
  sftp_private_key?: string
  sftp_path?: string
}

export const clustersApi = {
  list: () => client.get<Cluster[]>('/clusters').then((r) => r.data),

  get: (id: string) => client.get<Cluster>(`/clusters/${id}`).then((r) => r.data),

  create: (body: CreateClusterBody) =>
    client.post<Cluster>('/clusters', body).then((r) => r.data),

  delete: (id: string) => client.delete(`/clusters/${id}`),

  status: (id: string) =>
    client.get<ClusterStatus>(`/clusters/${id}/status`).then((r) => r.data),

  updateFailoverSettings: (
    id: string,
    body: {
      auto_failover: boolean
      auto_failback: boolean
      app_tier_always_available: boolean
      failover_on_maintenance: boolean
      failover_delay_secs: number
      failback_multiplier: number
      vip?: string | null
    }
  ) =>
    client.patch<Cluster>(`/clusters/${id}/failover-settings`, body).then((r) => r.data),

  updateMediaSyncSettings: (
    id: string,
    body: { media_sync_enabled: boolean; extra_folders_sync_enabled: boolean; extra_sync_folders: string[] }
  ) =>
    client.patch<Cluster>(`/clusters/${id}/media-sync-settings`, body).then((r) => r.data),

  syncMedia: (id: string) =>
    client.post<ClusterSyncResult>(`/clusters/${id}/media-sync`).then((r) => r.data),

  configureFailover: (id: string, body: ConfigureFailoverBody) =>
    client.post<ConfigureFailoverResult>(`/clusters/${id}/configure-failover`, body).then((r) => r.data),

  failoverEvents: (id: string) =>
    client.get<Event[]>(`/clusters/${id}/failover-events`).then((r) => r.data),

  // ── Backup ─────────────────────────────────────────────────────────────────

  getBackupConfig: (id: string) =>
    client.get<BackupConfig>(`/clusters/${id}/backup-config`).then((r) => r.data),

  putBackupSchedule: (
    id: string,
    body: {
      enabled: boolean
      full_backup_cron: string
      diff_backup_cron: string
      incr_backup_interval_hrs: number
    }
  ) => client.put(`/clusters/${id}/backup-config`, body).then((r) => r.data),

  createBackupTarget: (id: string, body: CreateBackupTargetBody) =>
    client.post<BackupTarget>(`/clusters/${id}/backup-targets`, body).then((r) => r.data),

  updateBackupTarget: (id: string, tid: string, body: Partial<CreateBackupTargetBody>) =>
    client.put<BackupTarget>(`/clusters/${id}/backup-targets/${tid}`, body).then((r) => r.data),

  deleteBackupTarget: (id: string, tid: string) =>
    client.delete(`/clusters/${id}/backup-targets/${tid}`).then((r) => r.data),

  enableBackups: (id: string) =>
    client
      .post<{ cluster_id: string; config_tasks: BackupTaskRef[]; stanza_task?: BackupTaskRef }>(
        `/clusters/${id}/backup-config/enable`
      )
      .then((r) => r.data),

  pushBackupConfig: (id: string) =>
    client
      .post<{ cluster_id: string; nodes: BackupTaskRef[] }>(`/clusters/${id}/backup-config/push`)
      .then((r) => r.data),

  getBackupCatalog: (id: string) =>
    client
      .get<{ cluster_id: string; task_id: string; cached_catalog: BackupCatalog | null }>(
        `/clusters/${id}/backup-catalog`
      )
      .then((r) => r.data),

  runBackup: (id: string, type: 'full' | 'diff' | 'incr') =>
    client
      .post<{ cluster_id: string; task_id: string; hostname: string; type: string; status: string }>(
        `/clusters/${id}/backup/run`,
        { type }
      )
      .then((r) => r.data),

  clusterRestore: (
    id: string,
    body: { target_time: string; restore_node_id?: string; restore_cmd?: string }
  ) =>
    client
      .post<{
        cluster_id: string
        target_time: string
        restore_node: BackupTaskRef
        stop_tasks: BackupTaskRef[]
        replica_tasks: BackupTaskRef[]
      }>(`/clusters/${id}/backup-restore`, body)
      .then((r) => r.data),

  getBackupRuns: (id: string) =>
    client.get<{ cluster_id: string; runs: BackupRun[] }>(`/clusters/${id}/backup-runs`).then((r) => r.data),

  testBackupPath: (id: string, path: string) =>
    client
      .post<{ task_id: string; node_id: string; hostname: string }>(`/clusters/${id}/backup-path/test`, { path })
      .then((r) => r.data),

  getTask: (taskId: string) =>
    client
      .get<{
        ID: string
        NodeID: string
        TaskID: string
        TaskType: string
        Status: string
        RequestPayload: unknown
        ResponsePayload: unknown
        QueuedAt: string
        CompletedAt?: string
      }>(`/tasks/${taskId}`)
      .then((r) => r.data),
}
