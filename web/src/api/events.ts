import client from './client'

export const SEVERITIES = ['debug', 'info', 'warn', 'error', 'critical']
export const CATEGORIES = ['cluster', 'service', 'ha', 'config', 'agent']

// Text colour per event severity.
export const SEV_TEXT: Record<string, string> = {
  debug:    'text-gray-400',
  info:     'text-blue-400',
  warn:     'text-amber-400',
  error:    'text-red-400',
  critical: 'text-red-300 font-semibold',
}

export interface Event {
  id: string
  cluster_id?: string
  node_id?: string
  cluster_name?: string
  node_name?: string
  category: string
  severity: 'debug' | 'info' | 'warn' | 'error' | 'critical'
  code: string
  message: string
  actor: string
  metadata?: Record<string, unknown>
  occurred_at: string
}

export interface Heartbeat {
  id: string
  node_id: string
  cluster_id: string
  load_avg_1?: number
  load_avg_5?: number
  mem_used_pct?: number
  disk_used_pct?: number
  netbox_running?: boolean
  rq_running?: boolean
  redis_running?: boolean
  sentinel_running?: boolean
  patroni_running?: boolean
  postgres_running?: boolean
  patroni_role?: string
  redis_role?: string
  replication_lag_bytes?: number
  recorded_at: string
}

export interface EventFilter {
  category?: string
  severity?: string
  code?: string
  cluster_id?: string
  node_id?: string
  from?: string
  to?: string
  limit?: number
  offset?: number
}

export const eventsApi = {
  list: (params?: EventFilter) =>
    client.get<Event[]>('/events', { params }).then((r) => r.data),

  listForCluster: (clusterId: string, params?: { category?: string; severity?: string; limit?: number }) =>
    client.get<Event[]>(`/clusters/${clusterId}/events`, { params }).then((r) => r.data),

  listForNode: (clusterId: string, nodeId: string, params?: { category?: string; severity?: string; limit?: number }) =>
    client.get<Event[]>(`/clusters/${clusterId}/nodes/${nodeId}/events`, { params }).then((r) => r.data),

  listHeartbeats: (nodeId: string, params?: { from?: string; to?: string; limit?: number }) =>
    client.get<Heartbeat[]>('/heartbeats', { params: { node_id: nodeId, ...params } }).then((r) => r.data),
}
