import client from './client'

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
    client.get<FailoverEvent[]>(`/clusters/${id}/failover-events`).then((r) => r.data),
}
