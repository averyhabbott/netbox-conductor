import client from './client'

export interface AlertConfig {
  id: string
  name: string
  type: 'webhook' | 'email'
  enabled: boolean
  conditions: string[]
  webhook_url?: string
  email_to?: string
  created_at: string
  updated_at: string
}

export interface ActiveAlert {
  id: string
  cluster_id?: string
  node_id?: string
  severity: 'warn' | 'error'
  condition: string
  message: string
  fired_at: string
  resolved_at?: string
  acknowledged_at?: string
}

export interface ClusterLogEntry {
  id: string
  cluster_id: string
  node_id?: string
  hostname: string
  level: string
  source: string
  message: string
  log_file?: string
  occurred_at: string
}

export type AlertConfigBody = {
  name: string
  type: 'webhook' | 'email'
  enabled: boolean
  conditions: string[]
  webhook_url?: string | null
  email_to?: string | null
}

export const ALERT_CONDITIONS = [
  { value: 'agent_disconnected', label: 'Agent disconnected' },
  { value: 'netbox_down', label: 'NetBox service down' },
  { value: 'rq_down', label: 'RQ worker down' },
] as const

export const alertsApi = {
  listActive: () =>
    client.get<ActiveAlert[]>('/alerts').then((r) => r.data),

  acknowledge: (id: string) =>
    client.post(`/alerts/${id}/acknowledge`),

  listConfigs: () =>
    client.get<AlertConfig[]>('/alert-configs').then((r) => r.data),

  createConfig: (body: AlertConfigBody) =>
    client.post<AlertConfig>('/alert-configs', body).then((r) => r.data),

  updateConfig: (id: string, body: AlertConfigBody) =>
    client.put<AlertConfig>(`/alert-configs/${id}`, body).then((r) => r.data),

  deleteConfig: (id: string) =>
    client.delete(`/alert-configs/${id}`),

  clusterLogs: (clusterId: string, params?: { level?: string; limit?: number }) =>
    client
      .get<ClusterLogEntry[]>(`/clusters/${clusterId}/logs`, { params })
      .then((r) => r.data),

  systemLogs: (lines = 200) =>
    client.get<{ lines: string[] }>('/system/logs', { params: { lines } }).then((r) => r.data),
}
