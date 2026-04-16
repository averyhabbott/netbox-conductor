import { useReducer, useCallback } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { clustersApi } from '../api/clusters'
import type { ClusterStatus } from '../api/clusters'
import { alertsApi } from '../api/alerts'
import Layout from '../components/Layout'
import { useSSE } from '../hooks/useSSE'
import type { SSEEvent } from '../hooks/useSSE'

// ── Live event feed ───────────────────────────────────────────────────────────

interface FeedEntry {
  id: number
  type: string
  nodeId?: string
  summary: string
  ts: Date
}

let feedSeq = 0

function summarize(ev: SSEEvent): string {
  const p = ev.payload
  switch (ev.type) {
    case 'node.status':
      return `Agent ${p.status ?? ''}`
    case 'node.heartbeat':
      return `Heartbeat — load ${(p.load_avg_1 as number)?.toFixed(2) ?? '?'}, mem ${(p.mem_used_pct as number)?.toFixed(0) ?? '?'}%`
    case 'task.complete':
      return `Task ${(p.task_id as string)?.slice(0, 8) ?? '?'} — ${p.success ? 'success' : 'failure'}`
    case 'patroni.state':
      return `Role change: ${p.prev_role ?? '?'} → ${p.role ?? '?'}`
    default:
      return ev.type
  }
}

type FeedAction =
  | { type: 'add'; entry: FeedEntry }
  | { type: 'clear' }

function feedReducer(state: FeedEntry[], action: FeedAction): FeedEntry[] {
  switch (action.type) {
    case 'add':
      return [action.entry, ...state].slice(0, 50)
    case 'clear':
      return []
    default:
      return state
  }
}

const eventTypeColor: Record<string, string> = {
  'node.status': 'text-blue-400',
  'node.heartbeat': 'text-gray-500',
  'task.complete': 'text-emerald-400',
  'patroni.state': 'text-amber-400',
}

// ── Health helpers ────────────────────────────────────────────────────────────

type HealthStatus = 'healthy' | 'degraded' | 'offline'

function nodeHealth(n: ClusterStatus['nodes'][number]): HealthStatus {
  if (n.agent_status !== 'connected') return 'offline'
  if (n.netbox_running && n.rq_running) return 'healthy'
  return 'degraded'
}

function clusterHealth(nodes: ClusterStatus['nodes']): HealthStatus {
  if (!nodes.length) return 'offline'
  const statuses = nodes.map(nodeHealth)
  if (statuses.every((s) => s === 'healthy')) return 'healthy'
  if (statuses.some((s) => s !== 'offline')) return 'degraded'
  return 'offline'
}

// ── Donut chart ───────────────────────────────────────────────────────────────

const DONUT_R = 40
const DONUT_CIRC = 2 * Math.PI * DONUT_R

const HEALTH_COLORS: Record<HealthStatus, string> = {
  healthy: '#34d399',
  degraded: '#fbbf24',
  offline: '#ef4444',
}

const HEALTH_KEYS: HealthStatus[] = ['healthy', 'degraded', 'offline']

function DonutChart({
  counts,
  total,
  label,
}: {
  counts: Record<HealthStatus, number>
  total: number
  label: string
}) {
  let acc = 0
  const slices = HEALTH_KEYS
    .filter((k) => counts[k] > 0)
    .map((k) => {
      const frac = counts[k] / total
      const dashArray = `${frac * DONUT_CIRC} ${DONUT_CIRC}`
      const dashOffset = -(acc * DONUT_CIRC)
      acc += frac
      return { k, dashArray, dashOffset }
    })

  return (
    <div className="flex flex-col items-center gap-3">
      <div className="relative w-32 h-32">
        <svg viewBox="0 0 100 100" className="w-full h-full -rotate-90">
          <circle cx="50" cy="50" r={DONUT_R} fill="none" stroke="#1f2937" strokeWidth="12" />
          {total > 0 && slices.map(({ k, dashArray, dashOffset }) => (
            <circle
              key={k}
              cx="50"
              cy="50"
              r={DONUT_R}
              fill="none"
              stroke={HEALTH_COLORS[k]}
              strokeWidth="12"
              strokeDasharray={dashArray}
              strokeDashoffset={dashOffset}
            />
          ))}
        </svg>
        <div className="absolute inset-0 flex items-center justify-center">
          <span className="text-3xl font-bold">{total}</span>
        </div>
      </div>
      <div className="text-center">
        <p className="text-sm font-medium text-gray-300">{label}</p>
        <div className="flex gap-3 mt-1.5 justify-center flex-wrap">
          {HEALTH_KEYS.filter((k) => counts[k] > 0).map((k) => (
            <span key={k} className="flex items-center gap-1 text-xs text-gray-400">
              <span className="w-2 h-2 rounded-full" style={{ backgroundColor: HEALTH_COLORS[k] }} />
              {counts[k]} {k}
            </span>
          ))}
          {total === 0 && <span className="text-xs text-gray-600">—</span>}
        </div>
      </div>
    </div>
  )
}

// ── Health dot ────────────────────────────────────────────────────────────────

const HEALTH_DOT_BG: Record<HealthStatus, string> = {
  healthy: 'bg-emerald-400',
  degraded: 'bg-amber-400',
  offline: 'bg-red-500',
}

function HealthDot({ status }: { status: HealthStatus }) {
  return (
    <span
      className={`w-2.5 h-2.5 rounded-full flex-shrink-0 ${HEALTH_DOT_BG[status]}`}
      title={status}
    />
  )
}

// ── Component ─────────────────────────────────────────────────────────────────

export default function Dashboard() {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [feed, dispatch] = useReducer(feedReducer, [])

  const { data: clusters } = useQuery({
    queryKey: ['clusters'],
    queryFn: clustersApi.list,
    refetchInterval: 30_000,
  })

  const { data: clusterStatuses } = useQuery({
    queryKey: ['cluster-statuses'],
    queryFn: async () => {
      if (!clusters?.length) return []
      return Promise.all(clusters.map((c) => clustersApi.status(c.id)))
    },
    enabled: !!clusters?.length,
    refetchInterval: 30_000,
  })

  const { data: activeAlerts = [] } = useQuery({
    queryKey: ['active-alerts'],
    queryFn: alertsApi.listActive,
    refetchInterval: 30_000,
  })

  const handleSSE = useCallback((ev: SSEEvent) => {
    if (ev.type !== 'node.heartbeat') {
      dispatch({
        type: 'add',
        entry: {
          id: ++feedSeq,
          type: ev.type,
          nodeId: ev.node_id,
          summary: summarize(ev),
          ts: new Date(),
        },
      })
    }
    if (ev.type === 'node.status' || ev.type === 'patroni.state') {
      qc.invalidateQueries({ queryKey: ['nodes'] })
    }
    if (ev.type === 'task.complete') {
      qc.invalidateQueries({ queryKey: ['node-tasks'] })
    }
  }, [qc])

  useSSE(handleSSE)

  // Derive health counts for pie charts
  const clusterHealthCounts: Record<HealthStatus, number> = { healthy: 0, degraded: 0, offline: 0 }
  const nodeHealthCounts: Record<HealthStatus, number> = { healthy: 0, degraded: 0, offline: 0 }

  if (clusters && clusterStatuses) {
    clusters.forEach((_, i) => {
      const status = clusterStatuses[i]
      clusterHealthCounts[status ? clusterHealth(status.nodes) : 'offline']++
      status?.nodes.forEach((n) => { nodeHealthCounts[nodeHealth(n)]++ })
    })
  }

  const totalClusters = clusters?.length ?? 0
  const totalNodes = nodeHealthCounts.healthy + nodeHealthCounts.degraded + nodeHealthCounts.offline

  // Flat node list for the nodes panel (includes cluster_id from parent status)
  const allStatusNodes = clusterStatuses?.flatMap((s) =>
    s.nodes.map((n) => ({ ...n, cluster_id: s.cluster_id }))
  ) ?? []

  return (
    <Layout>
      <h2 className="text-2xl font-semibold mb-6">Dashboard</h2>

      {/* Health pie charts — clickable */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mb-8">
        <div
          onClick={() => navigate('/clusters')}
          className="bg-gray-900 border border-gray-800 rounded-xl p-8 flex items-center justify-center cursor-pointer hover:border-gray-700 transition-colors"
        >
          <DonutChart counts={clusterHealthCounts} total={totalClusters} label="Clusters" />
        </div>
        <div
          onClick={() => navigate('/nodes')}
          className="bg-gray-900 border border-gray-800 rounded-xl p-8 flex items-center justify-center cursor-pointer hover:border-gray-700 transition-colors"
        >
          <DonutChart counts={nodeHealthCounts} total={totalNodes} label="Nodes" />
        </div>
      </div>

      {/* Clusters and Nodes lists */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 mb-6">
        {/* Clusters list */}
        <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
          <div className="flex items-center justify-between px-6 py-4 border-b border-gray-800">
            <h3 className="font-medium">Clusters</h3>
            <Link
              to="/clusters"
              className="text-sm text-blue-400 hover:text-blue-300 transition-colors"
            >
              View all →
            </Link>
          </div>

          {!clusters || clusters.length === 0 ? (
            <div className="p-8 text-center">
              <p className="text-gray-500 text-sm mb-3">No clusters configured yet.</p>
              <Link
                to="/clusters"
                className="text-blue-400 hover:text-blue-300 text-sm transition-colors"
              >
                Create your first cluster →
              </Link>
            </div>
          ) : (
            <ul className="divide-y divide-gray-800">
              {clusters.map((c, i) => {
                const status = clusterStatuses?.[i]
                const health = status ? clusterHealth(status.nodes) : 'offline'
                return (
                  <li key={c.id}>
                    <Link
                      to={`/clusters/${c.id}`}
                      className="flex items-center gap-3 px-6 py-3 hover:bg-gray-800/40 transition-colors"
                    >
                      <HealthDot status={health} />
                      <span className="font-medium text-sm">{c.name}</span>
                    </Link>
                  </li>
                )
              })}
            </ul>
          )}
        </div>

        {/* Nodes list */}
        <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
          <div className="flex items-center justify-between px-6 py-4 border-b border-gray-800">
            <h3 className="font-medium">Nodes</h3>
            <Link
              to="/nodes"
              className="text-sm text-blue-400 hover:text-blue-300 transition-colors"
            >
              View all →
            </Link>
          </div>

          {allStatusNodes.length === 0 ? (
            <div className="p-8 text-center">
              <p className="text-gray-500 text-sm">No nodes configured yet.</p>
            </div>
          ) : (
            <ul className="divide-y divide-gray-800">
              {allStatusNodes.map((n) => {
                const health = nodeHealth(n)
                return (
                  <li key={n.node_id}>
                    <Link
                      to={`/clusters/${n.cluster_id}/nodes/${n.node_id}`}
                      className="flex items-center gap-3 px-6 py-3 hover:bg-gray-800/40 transition-colors"
                    >
                      <HealthDot status={health} />
                      <span className="font-medium text-sm">{n.hostname}</span>
                    </Link>
                  </li>
                )
              })}
            </ul>
          )}
        </div>
      </div>

      {/* Active Alerts */}
      <div className="bg-gray-900 border border-gray-800 rounded-xl p-6 mb-6">
        <div className="flex items-center justify-between mb-1">
          <p className="text-sm text-gray-400">Active Alerts</p>
          <Link to="/settings?tab=alerting" className="text-xs text-blue-400 hover:text-blue-300 transition-colors">
            Configure →
          </Link>
        </div>
        <p className={`text-3xl font-semibold ${activeAlerts.length > 0 ? 'text-amber-400' : ''}`}>
          {activeAlerts.length}
        </p>
        {activeAlerts.length > 0 && (
          <ul className="mt-3 space-y-1">
            {activeAlerts.slice(0, 5).map((a) => (
              <li key={a.id} className="text-xs flex items-center gap-2">
                <span className={`w-1.5 h-1.5 rounded-full flex-shrink-0 ${a.severity === 'error' ? 'bg-red-400' : 'bg-amber-400'}`} />
                <span className="text-gray-300">{a.message}</span>
              </li>
            ))}
            {activeAlerts.length > 5 && (
              <li className="text-xs text-gray-500">…and {activeAlerts.length - 5} more</li>
            )}
          </ul>
        )}
      </div>

      {/* Live event feed — full width */}
      <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
        <div className="flex items-center justify-between px-6 py-4 border-b border-gray-800">
          <div className="flex items-center gap-2">
            <h3 className="font-medium">Live Events</h3>
            <span className="w-2 h-2 rounded-full bg-emerald-400 animate-pulse" />
          </div>
          <button
            onClick={() => dispatch({ type: 'clear' })}
            className="text-xs text-gray-500 hover:text-gray-300 transition-colors"
          >
            Clear
          </button>
        </div>

        {feed.length === 0 ? (
          <div className="px-6 py-8 text-center text-gray-500 text-sm">
            Waiting for events…
          </div>
        ) : (
          <div className="max-h-96 overflow-y-auto divide-y divide-gray-800">
            {feed.map((entry) => (
              <div key={entry.id} className="px-6 py-2.5 flex items-start gap-3">
                <span
                  className={`text-xs font-mono mt-0.5 flex-shrink-0 ${eventTypeColor[entry.type] ?? 'text-gray-400'}`}
                >
                  {entry.type}
                </span>
                <span className="text-xs text-gray-300 flex-1 min-w-0 break-words">
                  {entry.summary}
                </span>
                <span className="text-xs text-gray-600 flex-shrink-0">
                  {entry.ts.toLocaleTimeString()}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </Layout>
  )
}
