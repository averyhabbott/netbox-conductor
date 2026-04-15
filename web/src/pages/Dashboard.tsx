import { useReducer, useCallback } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { clustersApi } from '../api/clusters'
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
      return [action.entry, ...state].slice(0, 50) // keep last 50
    case 'clear':
      return []
    default:
      return state
  }
}

// ── Connected-node tracker ────────────────────────────────────────────────────

const eventTypeColor: Record<string, string> = {
  'node.status': 'text-blue-400',
  'node.heartbeat': 'text-gray-500',
  'task.complete': 'text-emerald-400',
  'patroni.state': 'text-amber-400',
}

// ── Component ─────────────────────────────────────────────────────────────────

export default function Dashboard() {
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

  const handleSSE = useCallback((ev: SSEEvent) => {
    // Skip noisy heartbeats from the live feed (still count them for status)
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

    // Invalidate node queries on status or patroni changes so they stay fresh
    if (ev.type === 'node.status' || ev.type === 'patroni.state') {
      qc.invalidateQueries({ queryKey: ['nodes'] })
    }
    if (ev.type === 'task.complete') {
      qc.invalidateQueries({ queryKey: ['node-tasks'] })
    }
  }, [qc])

  useSSE(handleSSE)

  // Derive connected node count across all clusters
  const connectedCount = clusterStatuses
    ? clusterStatuses
        .flatMap((s) => s?.nodes ?? [])
        .filter((n) => n.agent_status === 'connected').length
    : '—'

  return (
    <Layout>
      <h2 className="text-2xl font-semibold mb-6">Dashboard</h2>

      <div className="grid grid-cols-1 md:grid-cols-3 gap-4 mb-8">
        {[
          { label: 'Clusters', value: clusters?.length ?? '—' },
          { label: 'Nodes Connected', value: connectedCount },
          { label: 'Active Alerts', value: '0' },
        ].map((stat) => (
          <div key={stat.label} className="bg-gray-900 border border-gray-800 rounded-xl p-6">
            <p className="text-sm text-gray-400 mb-1">{stat.label}</p>
            <p className="text-3xl font-semibold">{stat.value}</p>
          </div>
        ))}
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {/* Clusters table */}
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
            <table className="w-full text-sm">
              <thead>
                <tr className="text-gray-400 border-b border-gray-800">
                  <th className="text-left px-6 py-3 font-medium">Name</th>
                  <th className="text-left px-6 py-3 font-medium">Mode</th>
                  <th className="text-left px-6 py-3 font-medium">Auto Failover</th>
                  <th className="px-6 py-3" />
                </tr>
              </thead>
              <tbody>
                {clusters.map((c) => (
                  <tr
                    key={c.id}
                    className="border-b border-gray-800 last:border-0 hover:bg-gray-800/40"
                  >
                    <td className="px-6 py-3 font-medium">{c.name}</td>
                    <td className="px-6 py-3 text-gray-400">
                      {c.mode === 'active_standby' ? 'Active / Standby' : 'HA'}
                    </td>
                    <td className="px-6 py-3 text-gray-400">
                      {c.auto_failover ? (
                        <span className="text-emerald-400">On</span>
                      ) : (
                        'Off'
                      )}
                    </td>
                    <td className="px-6 py-3 text-right">
                      <Link
                        to={`/clusters/${c.id}`}
                        className="text-blue-400 hover:text-blue-300 transition-colors"
                      >
                        View →
                      </Link>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>

        {/* Live event feed */}
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
      </div>
    </Layout>
  )
}
