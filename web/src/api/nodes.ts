import client from './client'

export interface Node {
  id: string
  cluster_id: string
  hostname: string
  ip_address: string
  role: 'hyperconverged' | 'app' | 'db_only'
  failover_priority: number
  agent_status: 'connected' | 'disconnected' | 'unknown'
  netbox_running: boolean | null
  rq_running: boolean | null
  suppress_auto_start: boolean
  ssh_port: number
  last_seen_at?: string
  patroni_state?: Record<string, unknown>
  created_at: string
  updated_at: string
}

export interface CreateNodeBody {
  hostname: string
  ip_address: string
  role: 'hyperconverged' | 'app' | 'db_only'
  failover_priority?: number
  ssh_port?: number
}

export interface RegTokenResponse {
  token: string
  expires_at: string
  env_snippet: string
  node_id: string
  hostname: string
}

export const nodesApi = {
  list: (clusterId: string) =>
    client.get<Node[]>(`/clusters/${clusterId}/nodes`).then((r) => r.data),

  get: (clusterId: string, nodeId: string) =>
    client.get<Node>(`/clusters/${clusterId}/nodes/${nodeId}`).then((r) => r.data),

  create: (clusterId: string, body: CreateNodeBody) =>
    client.post<Node>(`/clusters/${clusterId}/nodes`, body).then((r) => r.data),

  update: (
    clusterId: string,
    nodeId: string,
    body: { failover_priority?: number; suppress_auto_start?: boolean }
  ) =>
    client.put<Node>(`/clusters/${clusterId}/nodes/${nodeId}`, body).then((r) => r.data),

  delete: (clusterId: string, nodeId: string) =>
    client.delete(`/clusters/${clusterId}/nodes/${nodeId}`),

  generateRegToken: (clusterId: string, nodeId: string) =>
    client
      .post<RegTokenResponse>(
        `/clusters/${clusterId}/nodes/${nodeId}/registration-token`
      )
      .then((r) => r.data),

  serviceAction: (clusterId: string, nodeId: string, action: 'start' | 'stop' | 'restart') =>
    client
      .post<{ task_id: string; status: string }>(
        `/clusters/${clusterId}/nodes/${nodeId}/${action}-netbox`
      )
      .then((r) => r.data),

  restartRQ: (clusterId: string, nodeId: string) =>
    client
      .post<{ task_id: string; status: string }>(
        `/clusters/${clusterId}/nodes/${nodeId}/restart-rq`
      )
      .then((r) => r.data),
}
