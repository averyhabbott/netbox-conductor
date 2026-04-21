import { type Event } from '../api/events'

const SEV_BADGE: Record<string, string> = {
  debug:    'bg-gray-700 text-gray-300',
  info:     'bg-blue-900 text-blue-300',
  warn:     'bg-amber-900 text-amber-300',
  error:    'bg-red-900 text-red-400',
  critical: 'bg-red-700 text-white font-semibold',
}

const CAT_BADGE: Record<string, string> = {
  cluster: 'bg-indigo-900 text-indigo-300',
  service: 'bg-cyan-900 text-cyan-300',
  ha:      'bg-orange-900 text-orange-300',
  config:  'bg-purple-900 text-purple-300',
  agent:   'bg-teal-900 text-teal-300',
}

function fmt(ts: string) {
  return new Date(ts).toLocaleString(undefined, {
    month: 'short', day: 'numeric',
    hour: '2-digit', minute: '2-digit', second: '2-digit',
  })
}

interface Props {
  events: Event[]
  loading?: boolean
  showCluster?: boolean
  showNode?: boolean
}

export default function EventsTable({ events, loading, showCluster, showNode }: Props) {
  if (loading) {
    return <div className="text-gray-500 text-sm py-8 text-center">Loading events…</div>
  }
  if (events.length === 0) {
    return <div className="text-gray-600 text-sm py-8 text-center">No events found.</div>
  }

  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-gray-500 text-xs border-b border-gray-800">
            <th className="text-left py-2 pr-4 font-medium w-40">Time</th>
            <th className="text-left py-2 pr-4 font-medium w-20">Sev</th>
            <th className="text-left py-2 pr-4 font-medium w-24">Category</th>
            <th className="text-left py-2 pr-4 font-medium w-28">Code</th>
            {showCluster && <th className="text-left py-2 pr-4 font-medium w-32">Cluster</th>}
            {showNode    && <th className="text-left py-2 pr-4 font-medium w-32">Node</th>}
            <th className="text-left py-2 pr-4 font-medium">Message</th>
            <th className="text-left py-2 font-medium w-20">Actor</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-gray-900">
          {events.map((ev) => (
            <tr key={ev.id} className="hover:bg-gray-900/50 transition-colors">
              <td className="py-2 pr-4 text-gray-500 whitespace-nowrap font-mono text-xs">
                {fmt(ev.occurred_at)}
              </td>
              <td className="py-2 pr-4">
                <span className={`inline-block px-1.5 py-0.5 rounded text-xs ${SEV_BADGE[ev.severity] ?? 'bg-gray-800 text-gray-400'}`}>
                  {ev.severity}
                </span>
              </td>
              <td className="py-2 pr-4">
                <span className={`inline-block px-1.5 py-0.5 rounded text-xs ${CAT_BADGE[ev.category] ?? 'bg-gray-800 text-gray-400'}`}>
                  {ev.category}
                </span>
              </td>
              <td className="py-2 pr-4 font-mono text-xs text-gray-400">{ev.code}</td>
              {showCluster && (
                <td className="py-2 pr-4 text-gray-400 text-xs truncate max-w-[8rem]">
                  {ev.cluster_name ?? ev.cluster_id?.slice(0, 8) ?? '—'}
                </td>
              )}
              {showNode && (
                <td className="py-2 pr-4 text-gray-400 text-xs truncate max-w-[8rem]">
                  {ev.node_name ?? (ev.node_id ? ev.node_id.slice(0, 8) : '—')}
                </td>
              )}
              <td className="py-2 pr-4 text-gray-200">{ev.message}</td>
              <td className="py-2 text-gray-500 text-xs truncate">{ev.actor}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
