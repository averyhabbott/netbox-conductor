import client from './client'

export interface NetboxConfig {
  id: string
  cluster_id: string
  version: number
  config_template: string
  rendered_hash?: string
  pushed_at?: string
  push_status?: string
  created_at: string
  is_default?: boolean
}

export interface ConfigOverride {
  config_id: string
  node_id: string
  key: string
  value: string
}

export interface ConfigWithOverrides {
  config: NetboxConfig
  overrides: ConfigOverride[]
}

export interface PushNodeResult {
  node_id: string
  hostname: string
  task_id?: string
  status: 'dispatched' | 'offline' | 'error'
  error?: string
}

export interface PushResult {
  config_id: string
  version: number
  status: string
  nodes: PushNodeResult[]
}

export interface ReadNodeConfigResult {
  raw_config: string
  parsed: {
    netbox_secret_key: string
    netbox_api_token_pepper: string
    netbox_db_user_username: string
    netbox_db_user_password: string
    redis_tasks_password: string
    redis_caching_password: string
  }
}

export interface SyncConfigBody {
  source_node_id?: string
  destination_node_ids: string[]
  content: string
  restart_after: boolean
}

export const configsApi = {
  getOrCreate: (clusterId: string) =>
    client.get<ConfigWithOverrides>(`/clusters/${clusterId}/config`).then((r) => r.data),

  save: (clusterId: string, configTemplate: string) =>
    client
      .post<NetboxConfig>(`/clusters/${clusterId}/config`, { config_template: configTemplate })
      .then((r) => r.data),

  preview: (clusterId: string, nodeId?: string, configTemplate?: string) =>
    client
      .post<{ content: string; sha256: string; char_count: number }>(
        `/clusters/${clusterId}/config/preview`,
        { node_id: nodeId, config_template: configTemplate }
      )
      .then((r) => r.data),

  push: (clusterId: string, version: number, restartAfter = false) =>
    client
      .post<PushResult>(`/clusters/${clusterId}/config/${version}/push`, {
        restart_after: restartAfter,
      })
      .then((r) => r.data),

  pushStatus: (clusterId: string, version: number) =>
    client
      .get<{ config_id: string; version: number; push_status?: string; pushed_at?: string }>(
        `/clusters/${clusterId}/config/${version}/push-status`
      )
      .then((r) => r.data),

  readNodeConfig: (clusterId: string, nodeId: string) =>
    client
      .post<ReadNodeConfigResult>(`/clusters/${clusterId}/nodes/${nodeId}/config/read`)
      .then((r) => r.data),

  syncConfig: (clusterId: string, body: SyncConfigBody) =>
    client
      .post<PushResult>(`/clusters/${clusterId}/config/sync`, body)
      .then((r) => r.data),
}
