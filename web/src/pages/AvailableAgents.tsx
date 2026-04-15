import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import Layout from '../components/Layout'
import { stagingApi, type StagingAgent, type StagingToken } from '../api/staging'
import { clustersApi, type Cluster } from '../api/clusters'
import { useAuthStore } from '../store/auth'

export default function AvailableAgents() {
  const role = useAuthStore((s) => s.user?.role)
  const isOperator = role === 'operator' || role === 'admin'

  return (
    <Layout>
      <div className="space-y-8">
        <div>
          <h1 className="text-2xl font-bold">Available Agents</h1>
          <p className="text-gray-400 text-sm mt-1">
            Agents that have registered but are not yet assigned to a cluster.
          </p>
        </div>
        <div className="grid grid-cols-1 lg:grid-cols-3 gap-8">
          <div className="lg:col-span-2">
            <AgentsTable isOperator={isOperator} />
          </div>
          <div>
            {isOperator && <TokenManager />}
          </div>
        </div>
      </div>
    </Layout>
  )
}

// ── Agents table ──────────────────────────────────────────────────────────────

function AgentsTable({ isOperator }: { isOperator: boolean }) {
  const qc = useQueryClient()
  const { data: agents = [], isLoading } = useQuery({
    queryKey: ['staging-agents'],
    queryFn: stagingApi.listAgents,
    refetchInterval: 15_000,
  })
  const { data: clusters = [] } = useQuery({
    queryKey: ['clusters'],
    queryFn: clustersApi.list,
  })

  const [assigning, setAssigning] = useState<string | null>(null) // staging agent id

  const deleteMutation = useMutation({
    mutationFn: stagingApi.deleteAgent,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['staging-agents'] }),
  })

  if (isLoading) {
    return (
      <div className="bg-gray-900 border border-gray-800 rounded-lg p-6">
        <p className="text-gray-400 text-sm">Loading...</p>
      </div>
    )
  }

  return (
    <div className="bg-gray-900 border border-gray-800 rounded-lg overflow-hidden">
      <div className="px-4 py-3 border-b border-gray-800">
        <h2 className="font-medium">Staging agents ({agents.length})</h2>
      </div>
      {agents.length === 0 ? (
        <div className="p-6 text-center text-gray-500 text-sm">
          No agents waiting for assignment.
        </div>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-gray-400 border-b border-gray-800">
              <th className="px-4 py-2 font-medium">Hostname</th>
              <th className="px-4 py-2 font-medium">OS / Arch</th>
              <th className="px-4 py-2 font-medium">Version</th>
              <th className="px-4 py-2 font-medium">Status</th>
              <th className="px-4 py-2 font-medium">Registered</th>
              {isOperator && <th className="px-4 py-2" />}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-800">
            {agents.map((a) => (
              <tr key={a.id} className="hover:bg-gray-800/50">
                <td className="px-4 py-3 font-mono">
                  {a.hostname}
                  {a.ip_address && (
                    <span className="block text-xs text-gray-500">{a.ip_address}</span>
                  )}
                </td>
                <td className="px-4 py-3 text-gray-300">
                  {a.os || '—'} / {a.arch || '—'}
                </td>
                <td className="px-4 py-3 text-gray-300">{a.agent_version || '—'}</td>
                <td className="px-4 py-3">
                  <StatusBadge connected={a.connected} status={a.status} />
                </td>
                <td className="px-4 py-3 text-gray-400 text-xs">
                  {new Date(a.created_at).toLocaleString()}
                </td>
                {isOperator && (
                  <td className="px-4 py-3 text-right space-x-2">
                    <button
                      onClick={() => setAssigning(a.id)}
                      className="text-xs bg-blue-600 hover:bg-blue-700 px-2 py-1 rounded transition-colors"
                    >
                      Assign
                    </button>
                    <button
                      onClick={() => {
                        if (confirm(`Remove staging agent ${a.hostname}?`)) {
                          deleteMutation.mutate(a.id)
                        }
                      }}
                      className="text-xs text-red-400 hover:text-red-300 transition-colors"
                    >
                      Remove
                    </button>
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {assigning && (
        <AssignModal
          agent={agents.find((a) => a.id === assigning)!}
          clusters={clusters}
          onClose={() => setAssigning(null)}
          onAssigned={() => {
            setAssigning(null)
            qc.invalidateQueries({ queryKey: ['staging-agents'] })
            qc.invalidateQueries({ queryKey: ['clusters'] })
          }}
        />
      )}
    </div>
  )
}

function StatusBadge({ connected, status }: { connected: boolean; status: string }) {
  if (connected) {
    return (
      <span className="inline-flex items-center gap-1 text-xs text-green-400">
        <span className="w-1.5 h-1.5 rounded-full bg-green-400 animate-pulse" />
        connected
      </span>
    )
  }
  return (
    <span className="inline-flex items-center gap-1 text-xs text-gray-500">
      <span className="w-1.5 h-1.5 rounded-full bg-gray-600" />
      {status}
    </span>
  )
}

// ── Assign modal ──────────────────────────────────────────────────────────────

function AssignModal({
  agent,
  clusters,
  onClose,
  onAssigned,
}: {
  agent: StagingAgent
  clusters: Cluster[]
  onClose: () => void
  onAssigned: () => void
}) {
  const [clusterID, setClusterID] = useState(clusters[0]?.id ?? '')
  const [role, setRole] = useState<'hyperconverged' | 'app' | 'db_only'>('hyperconverged')
  const [priority, setPriority] = useState(100)
  const [sshPort, setSSHPort] = useState(22)
  const [error, setError] = useState('')

  const mutation = useMutation({
    mutationFn: () =>
      stagingApi.assignAgent(agent.id, {
        cluster_id: clusterID,
        role,
        failover_priority: priority,
        ssh_port: sshPort,
      }),
    onSuccess: () => onAssigned(),
    onError: (e: any) => setError(e.response?.data?.message ?? 'Assignment failed'),
  })

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50">
      <div className="bg-gray-900 border border-gray-700 rounded-lg p-6 w-full max-w-md shadow-xl">
        <h3 className="font-semibold text-lg mb-4">Assign {agent.hostname}</h3>

        <div className="space-y-4">
          <div>
            <label className="block text-sm text-gray-400 mb-1">Cluster</label>
            {clusters.length === 0 ? (
              <p className="text-sm text-red-400">No clusters found. Create one first.</p>
            ) : (
              <select
                value={clusterID}
                onChange={(e) => setClusterID(e.target.value)}
                className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
              >
                {clusters.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            )}
          </div>

          <div>
            <label className="block text-sm text-gray-400 mb-1">Role</label>
            <select
              value={role}
              onChange={(e) => setRole(e.target.value as typeof role)}
              className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
            >
              <option value="hyperconverged">Hyperconverged</option>
              <option value="app">App only</option>
              <option value="db_only">DB only</option>
            </select>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-sm text-gray-400 mb-1">Failover priority</label>
              <input
                type="number"
                min={1}
                value={priority}
                onChange={(e) => setPriority(Number(e.target.value))}
                className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
              />
            </div>
            <div>
              <label className="block text-sm text-gray-400 mb-1">SSH port</label>
              <input
                type="number"
                min={1}
                max={65535}
                value={sshPort}
                onChange={(e) => setSSHPort(Number(e.target.value))}
                className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
              />
            </div>
          </div>

          {error && <p className="text-red-400 text-sm">{error}</p>}
        </div>

        <div className="flex justify-end gap-3 mt-6">
          <button
            onClick={onClose}
            className="text-sm text-gray-400 hover:text-white px-4 py-2 transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={() => mutation.mutate()}
            disabled={mutation.isPending || !clusterID}
            className="text-sm bg-blue-600 hover:bg-blue-700 disabled:opacity-50 px-4 py-2 rounded transition-colors"
          >
            {mutation.isPending ? 'Assigning…' : 'Assign to cluster'}
          </button>
        </div>
      </div>
    </div>
  )
}

// ── Token manager ─────────────────────────────────────────────────────────────

function TokenManager() {
  const qc = useQueryClient()
  const [label, setLabel] = useState('')
  const [expiresIn, setExpiresIn] = useState(24)
  const [newToken, setNewToken] = useState<StagingToken | null>(null)
  const [copied, setCopied] = useState(false)

  const { data: tokens = [] } = useQuery({
    queryKey: ['staging-tokens'],
    queryFn: stagingApi.listTokens,
  })

  const createMutation = useMutation({
    mutationFn: () => stagingApi.createToken(label, expiresIn),
    onSuccess: (tok) => {
      setNewToken(tok)
      setLabel('')
      qc.invalidateQueries({ queryKey: ['staging-tokens'] })
    },
  })

  const deleteMutation = useMutation({
    mutationFn: stagingApi.deleteToken,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['staging-tokens'] }),
  })

  const handleCopy = (text: string) => {
    navigator.clipboard.writeText(text)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  const unusedTokens = tokens.filter((t) => !t.used_at)

  return (
    <div className="space-y-4">
      {/* New token reveal */}
      {newToken?.token && (
        <div className="bg-yellow-900/30 border border-yellow-700 rounded-lg p-4">
          <p className="text-yellow-300 text-xs font-medium mb-2">
            Token created — copy it now, it won't be shown again.
          </p>
          <div className="flex items-center gap-2">
            <code className="flex-1 text-xs font-mono bg-black/40 px-2 py-1 rounded break-all">
              {newToken.token}
            </code>
            <button
              onClick={() => handleCopy(newToken.token!)}
              className="text-xs text-yellow-300 hover:text-white shrink-0 transition-colors"
            >
              {copied ? 'Copied!' : 'Copy'}
            </button>
          </div>
          <button
            onClick={() => setNewToken(null)}
            className="mt-2 text-xs text-gray-400 hover:text-white transition-colors"
          >
            Dismiss
          </button>
        </div>
      )}

      {/* Create form */}
      <div className="bg-gray-900 border border-gray-800 rounded-lg p-4">
        <h3 className="font-medium text-sm mb-3">Generate staging token</h3>
        <div className="space-y-3">
          <div>
            <label className="block text-xs text-gray-400 mb-1">Label (optional)</label>
            <input
              type="text"
              placeholder="e.g. rack-7 install"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
            />
          </div>
          <div>
            <label className="block text-xs text-gray-400 mb-1">Expires in (hours)</label>
            <input
              type="number"
              min={1}
              max={720}
              value={expiresIn}
              onChange={(e) => setExpiresIn(Number(e.target.value))}
              className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
            />
          </div>
          <button
            onClick={() => createMutation.mutate()}
            disabled={createMutation.isPending}
            className="w-full text-sm bg-blue-600 hover:bg-blue-700 disabled:opacity-50 py-2 rounded transition-colors"
          >
            {createMutation.isPending ? 'Creating…' : 'Create token'}
          </button>
        </div>
      </div>

      {/* Active tokens list */}
      {unusedTokens.length > 0 && (
        <div className="bg-gray-900 border border-gray-800 rounded-lg overflow-hidden">
          <div className="px-4 py-3 border-b border-gray-800">
            <h3 className="text-sm font-medium">Active tokens ({unusedTokens.length})</h3>
          </div>
          <ul className="divide-y divide-gray-800">
            {unusedTokens.map((t) => (
              <li key={t.id} className="px-4 py-3 flex items-center justify-between gap-2">
                <div className="min-w-0">
                  <p className="text-sm truncate">{t.label || <span className="text-gray-500 italic">unlabeled</span>}</p>
                  <p className="text-xs text-gray-500">
                    Expires {new Date(t.expires_at).toLocaleString()}
                  </p>
                </div>
                <button
                  onClick={() => deleteMutation.mutate(t.id)}
                  className="text-xs text-red-400 hover:text-red-300 shrink-0 transition-colors"
                >
                  Revoke
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}
