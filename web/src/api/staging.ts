import client from './client'

export interface StagingToken {
  id: string
  label: string
  created_at: string
  expires_at: string
  used_at: string | null
  // token field only present on creation response
  token?: string
}

export interface StagingAgent {
  id: string
  hostname: string
  ip_address: string
  os: string
  arch: string
  agent_version: string
  status: 'connected' | 'disconnected'
  connected: boolean
  last_seen_at: string | null
  created_at: string
}

export interface AssignAgentBody {
  cluster_id: string
  role: 'hyperconverged' | 'app' | 'db_only'
  failover_priority?: number
  ssh_port?: number
}

export interface AssignAgentResponse {
  node_id: string
  cluster_id: string
  hostname: string
  role: string
}

export const stagingApi = {
  listTokens: () =>
    client.get<StagingToken[]>('/staging/tokens').then((r) => r.data),

  createToken: (label: string, expiresInHours = 24) =>
    client
      .post<StagingToken>('/staging/tokens', { label, expires_in_hours: expiresInHours })
      .then((r) => r.data),

  deleteToken: (id: string) => client.delete(`/staging/tokens/${id}`),

  listAgents: () =>
    client.get<StagingAgent[]>('/staging/agents').then((r) => r.data),

  deleteAgent: (id: string) => client.delete(`/staging/agents/${id}`),

  assignAgent: (id: string, body: AssignAgentBody) =>
    client
      .post<AssignAgentResponse>(`/staging/agents/${id}/assign`, body)
      .then((r) => r.data),
}
