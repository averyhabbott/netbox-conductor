import client from './client'

export interface NodeTopology {
  node_id: string
  hostname: string
  role: string
  agent_status: string
  patroni_role: string
  patroni_state?: unknown
}

export interface TopologyResponse {
  cluster_id: string
  witness_addr: string
  nodes: NodeTopology[]
}

export interface HistoryRow {
  task_id: string
  node_id: string
  hostname: string
  task_type: string
  status: string
  queued_at: string
  completed_at?: string
}

export interface PushResult {
  node_id: string
  hostname: string
  task_id?: string
  status: string
  error?: string
}

export const patroniApi = {
  topology: (clusterId: string) =>
    client.get<TopologyResponse>(`/clusters/${clusterId}/patroni/topology`).then((r) => r.data),

  history: (clusterId: string) =>
    client.get<{ cluster_id: string; history: HistoryRow[] }>(
      `/clusters/${clusterId}/patroni/history`
    ).then((r) => r.data),

  switchover: (clusterId: string, candidate?: string) =>
    client
      .post(`/clusters/${clusterId}/patroni/switchover`, { candidate: candidate ?? '' })
      .then((r) => r.data),

  pushPatroniConfig: (clusterId: string) =>
    client
      .post<{ cluster_id: string; nodes: PushResult[] }>(
        `/clusters/${clusterId}/patroni/push-config`,
        {}
      )
      .then((r) => r.data),

  pushPatroniConfigNode: (clusterId: string, nodeId: string) =>
    client
      .post(`/clusters/${clusterId}/nodes/${nodeId}/push-patroni-config`, {})
      .then((r) => r.data),

  startWitness: (clusterId: string) =>
    client.post(`/clusters/${clusterId}/patroni/witness/start`, {}).then((r) => r.data),

  pushSentinelConfig: (clusterId: string, restartAfter = false) =>
    client
      .post<{ cluster_id: string; master_host: string; nodes: PushResult[] }>(
        `/clusters/${clusterId}/sentinel/push-config`,
        { restart_after: restartAfter }
      )
      .then((r) => r.data),

  failover: (clusterId: string, candidate?: string) =>
    client
      .post(`/clusters/${clusterId}/patroni/failover`, { candidate: candidate ?? '' })
      .then((r) => r.data),

  getRetentionPolicy: (clusterId: string) =>
    client
      .get<{ cluster_id: string; retention_days: number; expire_cmd: string }>(
        `/clusters/${clusterId}/retention-policy`
      )
      .then((r) => r.data),

  setRetentionPolicy: (clusterId: string, retentionDays: number, expireCmd: string) =>
    client
      .put<{ cluster_id: string; retention_days: number; expire_cmd: string }>(
        `/clusters/${clusterId}/retention-policy`,
        { retention_days: retentionDays, expire_cmd: expireCmd }
      )
      .then((r) => r.data),

  enforceRetention: (clusterId: string) =>
    client
      .post<{ task_id: string; message: string }>(
        `/clusters/${clusterId}/retention-policy/enforce`,
        {}
      )
      .then((r) => r.data),
}
