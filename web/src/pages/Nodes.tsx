import { useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { clustersApi } from '../api/clusters'
import { nodesApi } from '../api/nodes'
import type { Node } from '../api/nodes'
import Layout from '../components/Layout'

type HealthStatus = 'healthy' | 'degraded' | 'offline'

function nodeHealth(n: Node): HealthStatus {
  if (n.agent_status !== 'connected') return 'offline'
  if (n.netbox_running && n.rq_running) return 'healthy'
  return 'degraded'
}

const HEALTH_DOT: Record<HealthStatus, string> = {
  healthy: 'bg-emerald-400',
  degraded: 'bg-amber-400',
  offline: 'bg-red-500',
}

const STATUS_BADGE: Record<string, string> = {
  connected: 'bg-emerald-900/50 text-emerald-300',
  disconnected: 'bg-red-900/50 text-red-300',
  unknown: 'bg-gray-800 text-gray-400',
}

const ROLE_LABEL: Record<string, string> = {
  hyperconverged: 'Hyper-converged',
  app: 'App',
  db_only: 'DB only',
}

function ServiceDot({ running }: { running: boolean | null }) {
  if (running === null) return <span className="text-gray-600 text-xs">—</span>
  return (
    <span
      className={`w-2 h-2 rounded-full inline-block ${running ? 'bg-emerald-400' : 'bg-red-500'}`}
    />
  )
}

export default function Nodes() {
  const navigate = useNavigate()

  const { data: clusters } = useQuery({
    queryKey: ['clusters'],
    queryFn: clustersApi.list,
    refetchInterval: 30_000,
  })

  const { data: allNodes, isLoading } = useQuery({
    queryKey: ['all-nodes', clusters?.map((c) => c.id).join(',')],
    queryFn: async () => {
      if (!clusters?.length) return []
      const results = await Promise.all(clusters.map((c) => nodesApi.list(c.id)))
      return results.flat()
    },
    enabled: !!clusters?.length,
    refetchInterval: 30_000,
  })

  const clusterName = Object.fromEntries((clusters ?? []).map((c) => [c.id, c.name]))

  return (
    <Layout>
      <h2 className="text-2xl font-semibold mb-6">Nodes</h2>

      <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
        {isLoading ? (
          <div className="p-8 text-center text-gray-500 text-sm">Loading…</div>
        ) : !allNodes?.length ? (
          <div className="p-8 text-center text-gray-500 text-sm">No nodes configured yet.</div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-gray-800 text-xs text-gray-500 uppercase tracking-wide">
                <th className="px-6 py-3 text-left">Node</th>
                <th className="px-6 py-3 text-left">Cluster</th>
                <th className="px-6 py-3 text-left">Role</th>
                <th className="px-6 py-3 text-left">Agent</th>
                <th className="px-6 py-3 text-center">NetBox</th>
                <th className="px-6 py-3 text-center">RQ</th>
                <th className="px-6 py-3 text-left">Last Seen</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-800">
              {allNodes.map((n) => {
                const health = nodeHealth(n)
                return (
                  <tr
                    key={n.id}
                    onClick={() => navigate(`/clusters/${n.cluster_id}/nodes/${n.id}`)}
                    className="hover:bg-gray-800/40 transition-colors cursor-pointer"
                  >
                    <td className="px-6 py-3">
                      <div className="flex items-center gap-2.5">
                        <span
                          className={`w-2 h-2 rounded-full flex-shrink-0 ${HEALTH_DOT[health]}`}
                        />
                        <span className="font-medium">{n.hostname}</span>
                      </div>
                    </td>
                    <td className="px-6 py-3">
                      <span
                        onClick={(e) => {
                          e.stopPropagation()
                          navigate(`/clusters/${n.cluster_id}`)
                        }}
                        className="text-gray-400 hover:text-blue-400 transition-colors cursor-pointer"
                      >
                        {clusterName[n.cluster_id] ?? n.cluster_id.slice(0, 8)}
                      </span>
                    </td>
                    <td className="px-6 py-3 text-gray-400">
                      {ROLE_LABEL[n.role] ?? n.role}
                    </td>
                    <td className="px-6 py-3">
                      <span
                        className={`text-xs px-2 py-0.5 rounded-full ${STATUS_BADGE[n.agent_status] ?? STATUS_BADGE.unknown}`}
                      >
                        {n.agent_status}
                      </span>
                    </td>
                    <td className="px-6 py-3 text-center">
                      <ServiceDot running={n.netbox_running} />
                    </td>
                    <td className="px-6 py-3 text-center">
                      <ServiceDot running={n.rq_running} />
                    </td>
                    <td className="px-6 py-3 text-xs text-gray-500">
                      {n.last_seen_at ? new Date(n.last_seen_at).toLocaleString() : '—'}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </div>
    </Layout>
  )
}
