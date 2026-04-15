import client from './client'

export interface Cluster {
  id: string
  name: string
  mode: 'active_standby' | 'ha'
  auto_failover: boolean
  auto_failback: boolean
  vip?: string
  patroni_scope: string
  netbox_version: string
  created_at: string
  updated_at: string
}

export interface CreateClusterBody {
  name: string
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
    body: { auto_failover: boolean; auto_failback: boolean; vip?: string | null }
  ) =>
    client.patch<Cluster>(`/clusters/${id}/failover-settings`, body).then((r) => r.data),
}
