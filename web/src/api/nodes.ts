import client from './client'

export interface Node {
  id: string
  cluster_id: string
  hostname: string
  ip_address: string
  role: 'hyperconverged' | 'app' | 'db_only'
  failover_priority: number
  agent_status: 'connected' | 'disconnected' | 'unknown'
  agent_version?: string
  netbox_running: boolean | null
  rq_running: boolean | null
  netbox_version?: string
  health_status?: 'healthy' | 'degraded' | 'offline'
  suppress_auto_start: boolean
  maintenance_mode: boolean
  ssh_port: number
  last_seen_at?: string
  patroni_state?: Record<string, unknown>
  created_at: string
  updated_at: string
  // Service-level health indicators from heartbeat
  redis_running?: boolean | null
  redis_role?: string
  sentinel_running?: boolean | null
  patroni_running?: boolean | null
  postgres_running?: boolean | null
}

export interface CreateNodeBody {
  hostname: string
  ip_address: string
  role: 'hyperconverged' | 'app' | 'db_only'
  failover_priority?: number
  ssh_port?: number
}

export interface EditNodeBody {
  hostname?: string
  ip_address?: string
  role?: 'hyperconverged' | 'app' | 'db_only'
  failover_priority?: number
  suppress_auto_start?: boolean
}

export interface RegTokenResponse {
  token: string
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

  update: (clusterId: string, nodeId: string, body: EditNodeBody) =>
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

  fetchAgentEnvText: (clusterId: string, nodeId: string) =>
    client
      .post<string>(`/clusters/${clusterId}/nodes/${nodeId}/agent-env`, {}, { responseType: 'text' })
      .then((r) => r.data as unknown as string),

  downloadAgentEnv: async (clusterId: string, nodeId: string, hostname: string) => {
    const response = await client.post(
      `/clusters/${clusterId}/nodes/${nodeId}/agent-env`,
      {},
      { responseType: 'blob' }
    )
    const url = window.URL.createObjectURL(response.data as Blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `netbox-agent-${hostname}.env`
    a.click()
    window.URL.revokeObjectURL(url)
  },

  setMaintenance: (clusterId: string, nodeId: string, enabled: boolean) =>
    client
      .put<Node>(`/clusters/${clusterId}/nodes/${nodeId}/maintenance`, { enabled })
      .then((r) => r.data),

  getLogs: (clusterId: string, nodeId: string, lines = 200, source: 'agent' | 'netbox' = 'agent', logName?: string) =>
    client
      .get<{ lines: string[]; path: string; node: string }>(
        `/clusters/${clusterId}/nodes/${nodeId}/logs`,
        {
          params: {
            lines,
            ...(source === 'netbox'
              ? { source: 'netbox', ...(logName ? { log_name: logName } : {}) }
              : {}),
          },
        }
      )
      .then((r) => r.data),

  getNetboxLogNames: (clusterId: string, nodeId: string) =>
    client
      .get<{ names: string[] }>(`/clusters/${clusterId}/nodes/${nodeId}/netbox-log-names`)
      .then((r) => r.data),

  upgradeAgent: (clusterId: string, nodeId: string) =>
    client
      .post<{ task_id: string; status: string }>(
        `/clusters/${clusterId}/nodes/${nodeId}/upgrade-agent`
      )
      .then((r) => r.data),
}
