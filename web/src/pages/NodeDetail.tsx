import { useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { clustersApi } from '../api/clusters'
import { nodesApi } from '../api/nodes'
import client from '../api/client'
import Layout from '../components/Layout'

const serviceActions = [
  { label: 'Start NetBox', action: 'start' as const },
  { label: 'Stop NetBox', action: 'stop' as const },
  { label: 'Restart NetBox', action: 'restart' as const },
]

interface TaskRow {
  task_id: string
  task_type: string
  status: string
  queued_at: string
  completed_at?: string
}

const taskStatusColor: Record<string, string> = {
  success: 'text-emerald-400',
  failure: 'text-red-400',
  ack: 'text-blue-400',
  sent: 'text-yellow-400',
  queued: 'text-gray-400',
}

function StatusRow({ label, value }: { label: string; value?: string | number | boolean | null }) {
  const display =
    typeof value === 'boolean' ? (
      <span className={value ? 'text-emerald-400' : 'text-red-400'}>
        {value ? 'Running' : 'Stopped'}
      </span>
    ) : value == null ? (
      <span className="text-gray-600">—</span>
    ) : (
      <span className="text-gray-300">{String(value)}</span>
    )

  return (
    <div className="flex items-center justify-between py-3 border-b border-gray-800 last:border-0">
      <span className="text-sm text-gray-400">{label}</span>
      <span className="text-sm font-medium">{display}</span>
    </div>
  )
}

function AgentBadge({ status }: { status: string }) {
  const styles: Record<string, string> = {
    connected: 'bg-emerald-900/50 text-emerald-400 border-emerald-800',
    disconnected: 'bg-red-900/50 text-red-400 border-red-800',
    unknown: 'bg-gray-800 text-gray-400 border-gray-700',
  }
  return (
    <span className={`inline-flex items-center gap-1.5 text-xs px-2 py-0.5 rounded border ${styles[status] ?? styles.unknown}`}>
      <span className={`w-1.5 h-1.5 rounded-full ${status === 'connected' ? 'bg-emerald-400 animate-pulse' : status === 'disconnected' ? 'bg-red-400' : 'bg-gray-500'}`} />
      {status}
    </span>
  )
}

export default function NodeDetail() {
  const { id, nid } = useParams<{ id: string; nid: string }>()
  const qc = useQueryClient()
  const [actionResult, setActionResult] = useState<{ success: boolean; message: string } | null>(null)

  const { data: cluster } = useQuery({
    queryKey: ['cluster', id],
    queryFn: () => clustersApi.get(id!),
    enabled: !!id,
  })

  const { data: node, isLoading } = useQuery({
    queryKey: ['node', nid],
    queryFn: () => nodesApi.get(id!, nid!),
    enabled: !!id && !!nid,
    refetchInterval: 15_000,
  })

  const serviceAction = useMutation({
    mutationFn: (action: 'start' | 'stop' | 'restart') =>
      nodesApi.serviceAction(id!, nid!, action),
    onSuccess: (data) => {
      setActionResult({ success: true, message: `Task dispatched: ${data.task_id}` })
      setTimeout(() => {
        qc.invalidateQueries({ queryKey: ['node', nid] })
        qc.invalidateQueries({ queryKey: ['node-tasks', nid] })
      }, 2000)
    },
    onError: (e: any) => {
      setActionResult({
        success: false,
        message: e.response?.data?.message ?? 'Failed to dispatch task',
      })
    },
  })

  const restartRQ = useMutation({
    mutationFn: () => nodesApi.restartRQ(id!, nid!),
    onSuccess: (data) => {
      setActionResult({ success: true, message: `Task dispatched: ${data.task_id}` })
      setTimeout(() => qc.invalidateQueries({ queryKey: ['node-tasks', nid] }), 2000)
    },
    onError: (e: any) => {
      setActionResult({
        success: false,
        message: e.response?.data?.message ?? 'Failed to dispatch task',
      })
    },
  })

  const toggleSuppress = useMutation({
    mutationFn: (suppress: boolean) => nodesApi.update(id!, nid!, { suppress_auto_start: suppress }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['node', nid] }),
  })

  const { data: tasksData } = useQuery({
    queryKey: ['node-tasks', nid],
    queryFn: () =>
      client
        .get<{ tasks: TaskRow[] }>(`/clusters/${id}/nodes/${nid}/tasks?limit=20`)
        .then((r) => r.data),
    enabled: !!id && !!nid,
    refetchInterval: 10_000,
  })

  if (isLoading) {
    return <Layout><div className="text-gray-500 text-sm">Loading…</div></Layout>
  }
  if (!node) {
    return <Layout><div className="text-red-400">Node not found.</div></Layout>
  }

  return (
    <Layout>
      {/* Breadcrumb */}
      <div className="flex items-center gap-2 text-sm text-gray-500 mb-6">
        <Link to="/clusters" className="hover:text-gray-300 transition-colors">Clusters</Link>
        <span>/</span>
        <Link to={`/clusters/${id}`} className="hover:text-gray-300 transition-colors">
          {cluster?.name ?? id}
        </Link>
        <span>/</span>
        <span className="text-white">{node.hostname}</span>
      </div>

      {/* Header */}
      <div className="flex items-start justify-between mb-6">
        <div>
          <div className="flex items-center gap-3">
            <h2 className="text-2xl font-semibold">{node.hostname}</h2>
            <AgentBadge status={node.agent_status} />
          </div>
          <p className="text-sm text-gray-400 mt-1 font-mono">{node.ip_address}</p>
        </div>

        {/* Service controls */}
        <div className="flex gap-2 flex-wrap">
          {serviceActions.map(({ label, action }) => (
            <button
              key={action}
              onClick={() => serviceAction.mutate(action)}
              disabled={serviceAction.isPending || restartRQ.isPending || node.agent_status !== 'connected'}
              className="bg-gray-800 hover:bg-gray-700 disabled:opacity-40 text-sm px-3 py-1.5 rounded-lg transition-colors"
            >
              {label}
            </button>
          ))}
          <button
            onClick={() => restartRQ.mutate()}
            disabled={serviceAction.isPending || restartRQ.isPending || node.agent_status !== 'connected'}
            className="bg-gray-800 hover:bg-gray-700 disabled:opacity-40 text-sm px-3 py-1.5 rounded-lg transition-colors"
          >
            Restart RQ
          </button>
        </div>
      </div>

      {actionResult && (
        <div className={`mb-6 p-3 rounded-lg text-sm ${actionResult.success ? 'bg-emerald-900/30 text-emerald-300 border border-emerald-800' : 'bg-red-900/30 text-red-300 border border-red-800'}`}>
          {actionResult.message}
        </div>
      )}

      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        {/* Node status */}
        <div className="bg-gray-900 border border-gray-800 rounded-xl p-6">
          <h3 className="font-medium mb-4">Node Status</h3>
          <StatusRow label="Role" value={node.role.replace('_', ' ')} />
          <StatusRow label="Failover Priority" value={node.failover_priority} />
          <StatusRow label="SSH Port" value={node.ssh_port} />
          <StatusRow label="Agent Status" value={node.agent_status} />
          {node.last_seen_at && (
            <StatusRow label="Last Seen" value={new Date(node.last_seen_at).toLocaleString()} />
          )}
        </div>

        {/* Services */}
        <div className="bg-gray-900 border border-gray-800 rounded-xl p-6">
          <h3 className="font-medium mb-4">Services</h3>
          <StatusRow label="NetBox" value={node.netbox_running} />
          <StatusRow label="NetBox-RQ" value={node.rq_running} />

          {/* Suppress auto-start toggle */}
          <div className="flex items-center justify-between py-3 border-t border-gray-800 mt-1">
            <div>
              <p className="text-sm text-gray-400">Suppress Auto-Start</p>
              <p className="text-xs text-gray-600 mt-0.5">
                Prevent agent from auto-starting NetBox on Patroni promotion
              </p>
            </div>
            <button
              onClick={() => toggleSuppress.mutate(!node.suppress_auto_start)}
              className={`relative w-10 h-6 rounded-full transition-colors ${
                node.suppress_auto_start ? 'bg-yellow-600' : 'bg-gray-700'
              }`}
            >
              <span
                className={`absolute top-1 w-4 h-4 bg-white rounded-full shadow transition-all ${
                  node.suppress_auto_start ? 'left-5' : 'left-1'
                }`}
              />
            </button>
          </div>
        </div>

        {/* Patroni state */}
        {node.patroni_state && (
          <div className="bg-gray-900 border border-gray-800 rounded-xl p-6 md:col-span-2">
            <h3 className="font-medium mb-4">Patroni State</h3>
            <pre className="text-xs font-mono text-gray-400 overflow-auto max-h-64">
              {JSON.stringify(node.patroni_state, null, 2)}
            </pre>
          </div>
        )}

        {/* Task history */}
        <div className="bg-gray-900 border border-gray-800 rounded-xl p-6 md:col-span-2">
          <h3 className="font-medium mb-4">Recent Tasks</h3>
          {!tasksData?.tasks?.length ? (
            <p className="text-sm text-gray-500">No tasks dispatched yet.</p>
          ) : (
            <table className="w-full text-sm">
              <thead>
                <tr className="text-gray-400 border-b border-gray-800">
                  <th className="text-left py-2 pr-4 font-medium">Task Type</th>
                  <th className="text-left py-2 pr-4 font-medium">Status</th>
                  <th className="text-left py-2 pr-4 font-medium">Queued</th>
                  <th className="text-left py-2 font-medium">Completed</th>
                </tr>
              </thead>
              <tbody>
                {tasksData.tasks.map((t) => (
                  <tr key={t.task_id} className="border-b border-gray-800 last:border-0">
                    <td className="py-2 pr-4 font-mono text-xs text-gray-300">{t.task_type}</td>
                    <td className={`py-2 pr-4 font-medium ${taskStatusColor[t.status] ?? 'text-gray-400'}`}>
                      {t.status}
                    </td>
                    <td className="py-2 pr-4 text-gray-500 text-xs">
                      {new Date(t.queued_at).toLocaleString()}
                    </td>
                    <td className="py-2 text-gray-500 text-xs">
                      {t.completed_at ? new Date(t.completed_at).toLocaleString() : '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </Layout>
  )
}
