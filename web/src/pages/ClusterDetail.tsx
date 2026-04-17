import { useState, useRef, useEffect } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { clustersApi } from '../api/clusters'
import type { Cluster, ClusterSyncResult, ConfigureFailoverResult, FailoverEvent } from '../api/clusters'
import { nodesApi } from '../api/nodes'
import type { Node, EditNodeBody } from '../api/nodes'
import { credentialsApi, credentialLabels } from '../api/credentials'
import type { Credential, CredentialKind, GeneratedCredential } from '../api/credentials'
import { patroniApi } from '../api/patroni'
import type { PushResult } from '../api/patroni'
import { alertsApi } from '../api/alerts'
import type { ClusterLogEntry } from '../api/alerts'
import client from '../api/client'
import Layout from '../components/Layout'
import AddNodeWizard from './AddNodeWizard'

// The agent version that ships with this conductor build.
// Keep in sync with agentVersion in internal/agent/ws/client.go.
const CURRENT_AGENT_VERSION = '0.1.0'

function AgentStatusBadge({ status }: { status: string }) {
  const styles: Record<string, string> = {
    connected: 'bg-emerald-900/50 text-emerald-400 border-emerald-800',
    disconnected: 'bg-red-900/50 text-red-400 border-red-800',
    unknown: 'bg-gray-800 text-gray-400 border-gray-700',
  }
  return (
    <span
      className={`inline-flex items-center gap-1.5 text-xs px-2 py-0.5 rounded border ${
        styles[status] ?? styles.unknown
      }`}
    >
      <span
        className={`w-1.5 h-1.5 rounded-full ${
          status === 'connected'
            ? 'bg-emerald-400'
            : status === 'disconnected'
            ? 'bg-red-400'
            : 'bg-gray-500'
        }`}
      />
      {status}
    </span>
  )
}

function HealthDot({ status }: { status?: string }) {
  const color =
    status === 'healthy'
      ? 'bg-emerald-400'
      : status === 'degraded'
      ? 'bg-amber-400'
      : 'bg-red-500'
  const title =
    status === 'healthy'
      ? 'Healthy'
      : status === 'degraded'
      ? 'Degraded (service down)'
      : 'Offline'
  return (
    <span title={title} className={`inline-block w-2.5 h-2.5 rounded-full ${color}`} />
  )
}

function ServiceBadge({ running, label }: { running: boolean | null | undefined; label: string }) {
  if (running === null || running === undefined)
    return <span className="text-gray-600 text-xs">{label}: —</span>
  return (
    <span
      className={`text-xs ${running ? 'text-emerald-400' : 'text-red-400'}`}
    >
      {label}: {running ? '✓' : '✗'}
    </span>
  )
}

function ServiceBadgeWithRole({
  running,
  label,
  role,
}: {
  running: boolean | null | undefined
  label: string
  role?: string
}) {
  if (running === null || running === undefined)
    return <span className="text-gray-600 text-xs">{label}: —</span>
  const roleStr = role ? ` (${role})` : ''
  return (
    <span className={`text-xs ${running ? 'text-emerald-400' : 'text-red-400'}`}>
      {label}: {running ? '✓' : '✗'}{roleStr}
    </span>
  )
}

function EditNodeModal({
  node,
  clusterId,
  onClose,
}: {
  node: Node
  clusterId: string
  onClose: () => void
}) {
  const qc = useQueryClient()
  const [form, setForm] = useState<EditNodeBody>({
    hostname: node.hostname,
    ip_address: node.ip_address,
    role: node.role,
    failover_priority: node.failover_priority,
  })
  const [error, setError] = useState('')

  const save = useMutation({
    mutationFn: (body: EditNodeBody) => nodesApi.update(clusterId, node.id, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['nodes', clusterId] })
      onClose()
    },
    onError: (e: any) => {
      setError(e.response?.data?.message ?? 'Failed to save changes')
    },
  })

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    save.mutate(form)
  }

  const labelCls = 'block text-xs text-gray-400 mb-1'
  const inputCls =
    'w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:border-blue-500'

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4">
      <div className="bg-gray-900 border border-gray-700 rounded-xl w-full max-w-md">
        <div className="flex items-center justify-between px-6 py-4 border-b border-gray-800">
          <div>
            <h3 className="font-semibold">Edit Node</h3>
            <p className="text-xs text-gray-500 mt-0.5 font-mono">{node.hostname}</p>
          </div>
          <button onClick={onClose} className="text-gray-500 hover:text-gray-300 text-xl leading-none">×</button>
        </div>

        <form onSubmit={handleSubmit} className="px-6 py-5 space-y-4">
          {/* Warning */}
          <div className="flex gap-2.5 bg-amber-950/60 border border-amber-800/60 rounded-lg px-4 py-3">
            <span className="text-amber-400 text-base flex-shrink-0">⚠</span>
            <p className="text-xs text-amber-300 leading-relaxed">
              Changing the hostname, IP address, role, or failover priority requires
              re-running <strong>Configure Failover</strong> to take effect.
            </p>
          </div>

          <div>
            <label className={labelCls}>Hostname</label>
            <input
              className={inputCls}
              value={form.hostname ?? ''}
              onChange={(e) => setForm((f) => ({ ...f, hostname: e.target.value }))}
              required
            />
          </div>

          <div>
            <label className={labelCls}>IP Address</label>
            <input
              className={inputCls}
              value={form.ip_address ?? ''}
              onChange={(e) => setForm((f) => ({ ...f, ip_address: e.target.value }))}
              required
            />
          </div>

          <div>
            <label className={labelCls}>Role</label>
            <select
              className={inputCls}
              value={form.role}
              onChange={(e) =>
                setForm((f) => ({ ...f, role: e.target.value as EditNodeBody['role'] }))
              }
            >
              <option value="hyperconverged">Hyperconverged</option>
              <option value="app">App</option>
              <option value="db_only">DB Only</option>
            </select>
          </div>

          <div>
            <label className={labelCls}>Failover Priority</label>
            <input
              type="number"
              className={inputCls}
              value={form.failover_priority ?? ''}
              onChange={(e) =>
                setForm((f) => ({ ...f, failover_priority: parseInt(e.target.value, 10) || 0 }))
              }
              min={1}
            />
          </div>

          {error && <p className="text-xs text-red-400">{error}</p>}

          <div className="flex justify-end gap-3 pt-2">
            <button
              type="button"
              onClick={onClose}
              className="text-sm px-4 py-2 bg-gray-800 hover:bg-gray-700 rounded-lg transition-colors"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={save.isPending}
              className="text-sm px-4 py-2 bg-blue-600 hover:bg-blue-500 disabled:opacity-50 rounded-lg transition-colors"
            >
              {save.isPending ? 'Saving…' : 'Save'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

function NodeRow({
  node,
  clusterId,
  onEdit,
}: {
  node: Node
  clusterId: string
  onEdit: (node: Node) => void
}) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const upgrade = useMutation({
    mutationFn: () => nodesApi.upgradeAgent(clusterId, node.id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['nodes', clusterId] }),
  })
  const upgradeAvailable =
    node.agent_status === 'connected' &&
    node.agent_version != null &&
    node.agent_version !== CURRENT_AGENT_VERSION

  return (
    <tr
      onClick={() => navigate(`/clusters/${clusterId}/nodes/${node.id}`)}
      className="border-b border-gray-800 last:border-0 hover:bg-gray-800/40 cursor-pointer"
    >
      <td className="px-6 py-4">
        <div className="flex items-center gap-2">
          <HealthDot status={node.health_status} />
          <span className="font-medium">{node.hostname}</span>
          {node.maintenance_mode && (
            <span className="text-xs px-1.5 py-0.5 rounded bg-amber-900/50 text-amber-400 border border-amber-800">
              maintenance
            </span>
          )}
        </div>
        <div className="text-xs text-gray-500 font-mono">{node.ip_address}</div>
      </td>
      {/* Active App — green checkmark when NetBox is running */}
      <td className="px-6 py-4 text-center">
        {node.netbox_running === true && (
          <span className="text-emerald-400 text-base" title="Active app node">✓</span>
        )}
      </td>
      {/* Active DB — green checkmark when this node is the Patroni primary */}
      <td className="px-6 py-4 text-center">
        {(node.patroni_state?.role === 'primary' || node.patroni_state?.role === 'master') && (
          <span className="text-emerald-400 text-base" title="Active DB (Patroni primary)">✓</span>
        )}
      </td>
      <td className="px-6 py-4 text-gray-300 text-sm capitalize">
        {node.role.replace('_', ' ')}
      </td>
      <td className="px-6 py-4">
        <AgentStatusBadge status={node.agent_status} />
        {node.agent_version && (
          <div className="text-xs text-gray-500 mt-0.5 font-mono">
            v{node.agent_version}
            {upgradeAvailable && (
              <button
                onClick={(e) => { e.stopPropagation(); upgrade.mutate() }}
                disabled={upgrade.isPending}
                className="ml-2 text-amber-400 hover:text-amber-300 disabled:opacity-40"
                title={`Upgrade from v${node.agent_version} to v${CURRENT_AGENT_VERSION}`}
              >
                {upgrade.isPending ? 'upgrading…' : '↑ upgrade'}
              </button>
            )}
          </div>
        )}
      </td>
      <td className="px-6 py-4">
        <div className="flex gap-3">
          <ServiceBadge running={node.netbox_running} label="NetBox" />
          <ServiceBadge running={node.rq_running} label="RQ" />
        </div>
        <div className="flex gap-3 mt-1">
          <ServiceBadgeWithRole
            running={node.patroni_running}
            label="Patroni"
            role={node.patroni_state?.role as string | undefined}
          />
          <ServiceBadgeWithRole
            running={node.redis_running}
            label="Redis"
            role={node.redis_role}
          />
          <ServiceBadge running={node.sentinel_running} label="Sentinel" />
          <ServiceBadge running={node.postgres_running} label="DB" />
        </div>
        {node.netbox_version && (
          <div className="text-xs text-gray-500 mt-0.5">nb {node.netbox_version}</div>
        )}
      </td>
      <td className="px-6 py-4 text-gray-400 text-sm">{node.failover_priority}</td>
      <td className="px-4 py-4 text-right">
        <button
          onClick={(e) => { e.stopPropagation(); onEdit(node) }}
          className="text-xs text-gray-500 hover:text-gray-300 px-2 py-1 rounded hover:bg-gray-800 transition-colors"
          title="Edit node"
        >
          Edit
        </button>
      </td>
    </tr>
  )
}

const CRED_KINDS: CredentialKind[] = [
  'postgres_superuser',
  'postgres_replication',
  'netbox_db_user',
  'redis_password',
  'patroni_rest_password',
]

function CredentialRow({
  kind,
  cred,
  clusterId,
}: {
  kind: CredentialKind
  cred?: Credential
  clusterId: string
}) {
  const qc = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [username, setUsername] = useState(cred?.username ?? '')
  const [password, setPassword] = useState('')
  const [dbName, setDbName] = useState(cred?.db_name ?? '')
  const [error, setError] = useState('')

  const save = useMutation({
    mutationFn: () =>
      credentialsApi.upsert(clusterId, kind, {
        username,
        password,
        db_name: kind === 'netbox_db_user' ? dbName || undefined : undefined,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['credentials', clusterId] })
      setEditing(false)
      setPassword('')
      setError('')
    },
    onError: (e: any) => setError(e.response?.data?.message ?? 'Failed to save'),
  })

  if (!editing) {
    return (
      <div className="flex items-center justify-between py-3 border-b border-gray-800 last:border-0">
        <div>
          <p className="text-sm font-medium">{credentialLabels[kind]}</p>
          {cred ? (
            <p className="text-xs text-gray-500 mt-0.5">
              {cred.username}
              {cred.db_name ? ` · db: ${cred.db_name}` : ''}
              {' · '}last set {new Date(cred.rotated_at ?? cred.created_at).toLocaleDateString()}
            </p>
          ) : (
            <p className="text-xs text-yellow-600 mt-0.5">Not configured</p>
          )}
        </div>
        <button
          onClick={() => {
            setUsername(cred?.username ?? '')
            setDbName(cred?.db_name ?? '')
            setPassword('')
            setEditing(true)
          }}
          className="text-xs text-blue-400 hover:text-blue-300"
        >
          {cred ? 'Update' : 'Set'}
        </button>
      </div>
    )
  }

  return (
    <div className="py-3 border-b border-gray-800 last:border-0">
      <p className="text-sm font-medium mb-2">{credentialLabels[kind]}</p>
      <div className="grid grid-cols-2 gap-2 mb-2">
        <input
          className="bg-gray-800 border border-gray-700 rounded px-2 py-1 text-sm"
          placeholder="Username"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
        />
        <input
          type="password"
          className="bg-gray-800 border border-gray-700 rounded px-2 py-1 text-sm"
          placeholder="Password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        {kind === 'netbox_db_user' && (
          <input
            className="bg-gray-800 border border-gray-700 rounded px-2 py-1 text-sm col-span-2"
            placeholder="Database name (default: netbox)"
            value={dbName}
            onChange={(e) => setDbName(e.target.value)}
          />
        )}
      </div>
      {error && <p className="text-xs text-red-400 mb-2">{error}</p>}
      <div className="flex gap-2">
        <button
          onClick={() => save.mutate()}
          disabled={save.isPending || !username || !password}
          className="bg-blue-600 hover:bg-blue-500 disabled:opacity-40 text-xs px-3 py-1 rounded"
        >
          {save.isPending ? 'Saving…' : 'Save'}
        </button>
        <button
          onClick={() => setEditing(false)}
          className="text-xs text-gray-400 hover:text-gray-300"
        >
          Cancel
        </button>
      </div>
    </div>
  )
}

// ── Delete confirm dialog ─────────────────────────────────────────────────────

function DeleteClusterDialog({
  clusterName,
  onConfirm,
  onCancel,
  isPending,
}: {
  clusterName: string
  onConfirm: () => void
  onCancel: () => void
  isPending: boolean
}) {
  const [input, setInput] = useState('')
  return (
    <div className="fixed inset-0 bg-black/70 flex items-center justify-center z-50 p-4">
      <div className="bg-gray-900 border border-gray-700 rounded-xl p-6 w-full max-w-md">
        <h3 className="text-lg font-semibold text-red-400 mb-2">Delete cluster</h3>
        <p className="text-sm text-gray-300 mb-1">
          This will disconnect all agents, stop the Patroni witness, and permanently delete{' '}
          <span className="font-mono text-white">{clusterName}</span> and all its nodes,
          credentials, and configuration. This cannot be undone.
        </p>
        <p className="text-sm text-gray-400 mt-4 mb-2">
          Type <span className="font-mono text-white">{clusterName}</span> to confirm:
        </p>
        <input
          className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm mb-4 font-mono"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder={clusterName}
          autoFocus
        />
        <div className="flex justify-end gap-3">
          <button
            onClick={onCancel}
            className="text-sm text-gray-400 hover:text-gray-200 px-4 py-2"
          >
            Cancel
          </button>
          <button
            onClick={onConfirm}
            disabled={input !== clusterName || isPending}
            className="bg-red-700 hover:bg-red-600 disabled:opacity-40 text-sm px-4 py-2 rounded-lg"
          >
            {isPending ? 'Deleting…' : 'Delete cluster'}
          </button>
        </div>
      </div>
    </div>
  )
}

// ── Auto-generate credentials result modal ────────────────────────────────────

function GeneratedCredsModal({
  generated,
  onClose,
}: {
  generated: GeneratedCredential[]
  onClose: () => void
}) {
  return (
    <div className="fixed inset-0 bg-black/70 flex items-center justify-center z-50 p-4">
      <div className="bg-gray-900 border border-gray-700 rounded-xl p-6 w-full max-w-lg">
        <h3 className="text-lg font-semibold mb-1">Generated credentials</h3>
        <p className="text-xs text-amber-400 bg-amber-900/30 border border-amber-800 rounded px-3 py-2 mb-4">
          These passwords will not be shown again. Copy them now before closing.
        </p>
        <div className="space-y-3 mb-6">
          {generated.map((g) => (
            <div key={g.kind} className="bg-gray-800 rounded-lg p-3">
              <p className="text-xs text-gray-400 mb-1">{credentialLabels[g.kind as CredentialKind]}</p>
              <div className="flex items-center justify-between gap-2">
                <span className="font-mono text-xs text-gray-300">{g.username}</span>
                <span className="font-mono text-xs text-emerald-300 break-all">{g.password}</span>
              </div>
              {g.db_name && (
                <p className="text-xs text-gray-500 mt-0.5">db: {g.db_name}</p>
              )}
            </div>
          ))}
        </div>
        <div className="flex justify-end">
          <button
            onClick={onClose}
            className="bg-gray-700 hover:bg-gray-600 text-sm px-4 py-2 rounded-lg"
          >
            I've copied the passwords
          </button>
        </div>
      </div>
    </div>
  )
}

// ── Retention policy card ────────────────────────────────────────────────────

function RetentionCard({ clusterId }: { clusterId: string }) {
  const qc = useQueryClient()
  const [days, setDays] = useState<number | ''>('')
  const [expireCmd, setExpireCmd] = useState('')
  const [editing, setEditing] = useState(false)
  const [msg, setMsg] = useState<string | null>(null)

  const { data: policy } = useQuery({
    queryKey: ['retention-policy', clusterId],
    queryFn: () => patroniApi.getRetentionPolicy(clusterId),
  })

  const save = useMutation({
    mutationFn: () => patroniApi.setRetentionPolicy(clusterId, Number(days), expireCmd),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['retention-policy', clusterId] })
      setEditing(false)
      setMsg(null)
    },
    onError: (e: any) => setMsg(e?.response?.data?.message ?? 'Failed to save'),
  })

  const enforce = useMutation({
    mutationFn: () => patroniApi.enforceRetention(clusterId),
    onSuccess: (d) => setMsg(`Enforcement dispatched to ${d.hostname}`),
    onError: (e: any) => setMsg(e?.response?.data?.message ?? 'Failed to enforce'),
  })

  return (
    <div className="bg-gray-900 border border-gray-800 rounded-xl p-5">
      <h3 className="font-medium mb-1">Backup Retention</h3>
      <p className="text-xs text-gray-500 mb-3">
        Configure pgBackRest retention and trigger expiry on the primary DB node.
      </p>

      {!editing ? (
        <div className="space-y-2">
          <div className="flex items-center justify-between text-sm">
            <span className="text-gray-400">Retention</span>
            <span className="font-mono text-gray-200">
              {policy?.retention_days ?? '—'} days
            </span>
          </div>
          {policy?.expire_cmd && (
            <div className="text-xs text-gray-500 font-mono break-all">{policy.expire_cmd}</div>
          )}
          <button
            onClick={() => {
              setDays(policy?.retention_days ?? 7)
              setExpireCmd(policy?.expire_cmd ?? '')
              setEditing(true)
            }}
            className="text-xs text-blue-400 hover:text-blue-300"
          >
            Edit
          </button>
        </div>
      ) : (
        <div className="space-y-2">
          <div>
            <label className="block text-xs text-gray-400 mb-1">Retention days</label>
            <input
              type="number"
              min={1}
              className="w-full bg-gray-800 border border-gray-700 rounded px-2 py-1 text-sm"
              value={days}
              onChange={(e) => setDays(e.target.value === '' ? '' : Number(e.target.value))}
            />
          </div>
          <div>
            <label className="block text-xs text-gray-400 mb-1">
              Custom expire cmd <span className="text-gray-600">(optional)</span>
            </label>
            <input
              type="text"
              className="w-full bg-gray-800 border border-gray-700 rounded px-2 py-1 text-sm font-mono"
              placeholder="pgbackrest --stanza=main expire"
              value={expireCmd}
              onChange={(e) => setExpireCmd(e.target.value)}
            />
          </div>
          {msg && <p className="text-xs text-red-400">{msg}</p>}
          <div className="flex gap-2">
            <button
              onClick={() => save.mutate()}
              disabled={save.isPending || days === '' || Number(days) < 1}
              className="text-xs bg-blue-600 hover:bg-blue-500 disabled:opacity-40 px-3 py-1 rounded"
            >
              {save.isPending ? 'Saving…' : 'Save'}
            </button>
            <button
              onClick={() => { setEditing(false); setMsg(null) }}
              className="text-xs text-gray-400 hover:text-gray-300"
            >
              Cancel
            </button>
          </div>
        </div>
      )}

      {msg && !editing && (
        <p className="text-xs text-emerald-400 mt-2">{msg}</p>
      )}

      <button
        onClick={() => { setMsg(null); enforce.mutate() }}
        disabled={enforce.isPending}
        className="w-full mt-3 text-sm py-1.5 bg-gray-700 hover:bg-gray-600 disabled:opacity-40 rounded-lg transition-colors"
      >
        {enforce.isPending ? 'Running…' : 'Run expire now'}
      </button>
    </div>
  )
}

// ── Shared push-result list ───────────────────────────────────────────────────

function PushResultList({ results }: { results: PushResult[] }) {
  if (!results.length) return null
  return (
    <div className="mt-3 space-y-1">
      {results.map((r) => (
        <div
          key={r.node_id}
          className={`flex items-center justify-between text-xs px-3 py-1.5 rounded ${
            r.status === 'dispatched'
              ? 'bg-emerald-900/30 text-emerald-400'
              : r.status === 'offline'
              ? 'bg-amber-900/30 text-amber-400'
              : 'bg-red-900/30 text-red-400'
          }`}
        >
          <span className="font-medium">{r.hostname}</span>
          <span>{r.status}{r.error ? ` — ${r.error}` : ''}</span>
        </div>
      ))}
    </div>
  )
}

// ── Database tab (topology, history, failover ops, retention) ─────────────────

function DatabaseTab({ clusterId }: { clusterId: string }) {
  const [failoverMsg, setFailoverMsg] = useState<string | null>(null)

  const { data: topology, isLoading: topoLoading, refetch: refetchTopo } = useQuery({
    queryKey: ['patroni-topology', clusterId],
    queryFn: () => patroniApi.topology(clusterId),
    refetchInterval: 15_000,
  })

  const { data: history, isLoading: histLoading } = useQuery({
    queryKey: ['patroni-history', clusterId],
    queryFn: () => patroniApi.history(clusterId),
    refetchInterval: 30_000,
  })

  const switchover = useMutation({
    mutationFn: () => patroniApi.switchover(clusterId),
    onSuccess: () => refetchTopo(),
  })

  const primaryNode = topology?.nodes.find((n) => n.patroni_role === 'primary')

  const failover = useMutation({
    mutationFn: () => patroniApi.failover(clusterId, primaryNode?.hostname ?? ''),
    onSuccess: () => { refetchTopo(); setFailoverMsg('Failover dispatched.') },
    onError: (e: any) => setFailoverMsg(e?.response?.data?.message ?? 'Failover failed'),
  })

  const roleColor: Record<string, string> = {
    primary: 'text-emerald-400',
    replica: 'text-blue-400',
    standby_leader: 'text-amber-400',
  }

  return (
    <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
      {/* Topology + History */}
      <div className="lg:col-span-2 space-y-4">
        <div className="bg-gray-900 border border-gray-800 rounded-xl p-5">
          <div className="flex items-center justify-between mb-4">
            <h3 className="font-medium">Topology</h3>
            <button
              onClick={() => refetchTopo()}
              className="text-xs text-gray-500 hover:text-gray-300"
            >
              Refresh
            </button>
          </div>
          {topoLoading ? (
            <p className="text-gray-500 text-sm">Loading…</p>
          ) : topology?.nodes.length === 0 ? (
            <p className="text-gray-500 text-sm">No nodes found.</p>
          ) : (
            <div className="space-y-2">
              {topology?.nodes.map((n) => (
                <div
                  key={n.node_id}
                  className="flex items-center justify-between text-sm bg-gray-800/50 rounded-lg px-4 py-3"
                >
                  <div>
                    <span className="font-medium">{n.hostname}</span>
                    <span className="text-gray-500 text-xs ml-2">{n.role.replace('_', ' ')}</span>
                  </div>
                  <div className="flex items-center gap-3">
                    <span className={`text-xs font-mono ${roleColor[n.patroni_role] ?? 'text-gray-500'}`}>
                      {n.patroni_role || '—'}
                    </span>
                    <span
                      className={`text-xs px-2 py-0.5 rounded ${
                        n.agent_status === 'connected'
                          ? 'bg-emerald-900/50 text-emerald-400'
                          : 'bg-gray-800 text-gray-500'
                      }`}
                    >
                      {n.agent_status}
                    </span>
                  </div>
                </div>
              ))}
              {topology?.witness_addr && (
                <div className="text-xs text-gray-500 mt-2">
                  Witness: <span className="font-mono">{topology.witness_addr}</span>
                </div>
              )}
            </div>
          )}
        </div>

        <div className="bg-gray-900 border border-gray-800 rounded-xl p-5">
          <h3 className="font-medium mb-3">Task History</h3>
          {histLoading ? (
            <p className="text-gray-500 text-sm">Loading…</p>
          ) : !history?.history.length ? (
            <p className="text-gray-500 text-sm">No tasks yet.</p>
          ) : (
            <div className="space-y-1 max-h-64 overflow-y-auto">
              {history.history.map((row) => (
                <div
                  key={row.task_id}
                  className="flex items-center justify-between text-xs px-3 py-1.5 hover:bg-gray-800/40 rounded"
                >
                  <div className="flex items-center gap-3">
                    <span
                      className={`w-1.5 h-1.5 rounded-full flex-shrink-0 ${
                        row.status === 'success'
                          ? 'bg-emerald-400'
                          : row.status === 'failure' || row.status === 'timeout'
                          ? 'bg-red-400'
                          : 'bg-amber-400'
                      }`}
                    />
                    <span className="font-mono text-gray-400">{row.task_type}</span>
                    <span className="text-gray-600">{row.hostname}</span>
                  </div>
                  <div className="flex items-center gap-3 text-gray-500">
                    <span className={row.status === 'success' ? 'text-emerald-400' : row.status === 'failure' ? 'text-red-400' : ''}>{row.status}</span>
                    <span>{new Date(row.queued_at).toLocaleString()}</span>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>

      {/* Actions */}
      <div className="space-y-4">
        <div className="bg-gray-900 border border-gray-800 rounded-xl p-5">
          <h3 className="font-medium mb-1">Switchover</h3>
          <p className="text-xs text-gray-500 mb-3">
            Trigger a graceful Patroni switchover. The current primary will step down and a replica will be promoted.
          </p>
          <button
            onClick={() => switchover.mutate()}
            disabled={switchover.isPending}
            className="w-full text-sm py-2 bg-amber-700 hover:bg-amber-600 disabled:opacity-40 rounded-lg transition-colors"
          >
            {switchover.isPending ? 'Switching…' : 'Initiate switchover'}
          </button>
          {switchover.isSuccess && (
            <p className="text-xs text-emerald-400 mt-2">Switchover dispatched.</p>
          )}
          {switchover.isError && (
            <p className="text-xs text-red-400 mt-2">
              {(switchover.error as any)?.response?.data?.message ?? 'Switchover failed'}
            </p>
          )}
        </div>

        <div className="bg-gray-900 border border-gray-800 rounded-xl p-5">
          <h3 className="font-medium mb-1">Manual Failover</h3>
          <p className="text-xs text-gray-500 mb-3">
            Force an immediate Patroni failover. Use only when the primary is unresponsive and switchover is not possible.
          </p>
          <button
            onClick={() => { setFailoverMsg(null); failover.mutate() }}
            disabled={failover.isPending}
            className="w-full text-sm py-2 bg-red-800 hover:bg-red-700 disabled:opacity-40 rounded-lg transition-colors"
          >
            {failover.isPending ? 'Failing over…' : 'Force failover'}
          </button>
          {failoverMsg && (
            <p className={`text-xs mt-2 ${failover.isError ? 'text-red-400' : 'text-emerald-400'}`}>
              {failoverMsg}
            </p>
          )}
        </div>

        <RetentionCard clusterId={clusterId} />
      </div>
    </div>
  )
}

// ── Deployment tab (config editor + push Patroni + push Sentinel) ─────────────

function DeploymentTab({ clusterId }: { clusterId: string }) {
  const [patroniPushResult, setPatroniPushResult] = useState<PushResult[] | null>(null)
  const [sentinelPushResult, setSentinelPushResult] = useState<PushResult[] | null>(null)
  const [sentinelRestart, setSentinelRestart] = useState(false)

  const pushPatroni = useMutation({
    mutationFn: () => patroniApi.pushPatroniConfig(clusterId),
    onSuccess: (data) => setPatroniPushResult(data.nodes),
  })

  const pushSentinel = useMutation({
    mutationFn: () => patroniApi.pushSentinelConfig(clusterId, sentinelRestart),
    onSuccess: (data) => setSentinelPushResult(data.nodes),
  })

  return (
    <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
      {/* NetBox configuration.py */}
      <div className="bg-gray-900 border border-gray-800 rounded-xl p-6">
        <h3 className="font-medium mb-1">NetBox Configuration</h3>
        <p className="text-xs text-gray-500 mb-4">
          Edit and push <code className="text-xs bg-gray-800 px-1.5 py-0.5 rounded font-mono">configuration.py</code> to all nodes in this cluster.
        </p>
        <Link
          to={`/clusters/${clusterId}/config`}
          className="inline-block bg-blue-600 hover:bg-blue-500 text-sm font-medium px-4 py-2 rounded-lg transition-colors"
        >
          Open Config Editor →
        </Link>
      </div>

      {/* Push Patroni config */}
      <div className="bg-gray-900 border border-gray-800 rounded-xl p-6">
        <h3 className="font-medium mb-1">Push Patroni Config</h3>
        <p className="text-xs text-gray-500 mb-4">
          Renders and writes <code className="font-mono text-xs">patroni.yml</code> to all DB/hyperconverged nodes.
        </p>
        <button
          onClick={() => { setPatroniPushResult(null); pushPatroni.mutate() }}
          disabled={pushPatroni.isPending}
          className="w-full text-sm py-2 bg-blue-700 hover:bg-blue-600 disabled:opacity-40 rounded-lg transition-colors"
        >
          {pushPatroni.isPending ? 'Pushing…' : 'Push patroni.yml'}
        </button>
        {patroniPushResult && <PushResultList results={patroniPushResult} />}
      </div>

      {/* Push Sentinel config */}
      <div className="bg-gray-900 border border-gray-800 rounded-xl p-6">
        <h3 className="font-medium mb-1">Push Sentinel Config</h3>
        <p className="text-xs text-gray-500 mb-4">
          Renders and writes <code className="font-mono text-xs">sentinel.conf</code> to all nodes using the Redis password credential.
        </p>
        <label className="flex items-center gap-2 text-xs text-gray-400 mb-3 cursor-pointer">
          <input
            type="checkbox"
            checked={sentinelRestart}
            onChange={(e) => setSentinelRestart(e.target.checked)}
            className="rounded border-gray-600"
          />
          Restart redis-sentinel after write
        </label>
        <button
          onClick={() => { setSentinelPushResult(null); pushSentinel.mutate() }}
          disabled={pushSentinel.isPending}
          className="w-full text-sm py-2 bg-purple-700 hover:bg-purple-600 disabled:opacity-40 rounded-lg transition-colors"
        >
          {pushSentinel.isPending ? 'Pushing…' : 'Push sentinel.conf'}
        </button>
        {sentinelPushResult && <PushResultList results={sentinelPushResult} />}
      </div>
    </div>
  )
}

// ── Cluster audit tab ─────────────────────────────────────────────────────────

function AuditTab({ clusterId }: { clusterId: string }) {
  const { data: logs = [], isLoading, refetch } = useQuery({
    queryKey: ['cluster-audit', clusterId],
    queryFn: () =>
      client.get<any[]>(`/clusters/${clusterId}/audit-logs`).then((r) => r.data),
    refetchInterval: 30_000,
  })

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="font-medium">Audit Log</h3>
        <button
          onClick={() => refetch()}
          className="text-xs text-gray-500 hover:text-gray-300"
        >
          Refresh
        </button>
      </div>

      {isLoading ? (
        <p className="text-gray-500 text-sm">Loading…</p>
      ) : logs.length === 0 ? (
        <p className="text-gray-500 text-sm">No audit entries for this cluster yet.</p>
      ) : (
        <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-gray-800 text-gray-400 text-xs uppercase">
                <th className="text-left px-4 py-3">Time</th>
                <th className="text-left px-4 py-3">Action</th>
                <th className="text-left px-4 py-3">Target</th>
                <th className="text-left px-4 py-3">Actor</th>
                <th className="text-left px-4 py-3">Outcome</th>
              </tr>
            </thead>
            <tbody>
              {logs.map((l: any) => (
                <tr key={l.id} className="border-b border-gray-800 last:border-0 hover:bg-gray-800/30">
                  <td className="px-4 py-2 text-xs text-gray-500 whitespace-nowrap">
                    {new Date(l.created_at).toLocaleString()}
                  </td>
                  <td className="px-4 py-2 font-mono text-xs text-gray-300">{l.action}</td>
                  <td className="px-4 py-2 text-xs text-gray-400">
                    {l.target_type ?? '—'}
                    {l.target_id ? (
                      <span className="ml-1 text-gray-600 font-mono">
                        {String(l.target_id).slice(0, 8)}…
                      </span>
                    ) : null}
                  </td>
                  <td className="px-4 py-2 text-xs font-mono text-gray-400">
                    {l.actor_user_id
                      ? String(l.actor_user_id).slice(0, 8) + '…'
                      : l.actor_agent_node_id
                      ? 'agent:' + String(l.actor_agent_node_id).slice(0, 8) + '…'
                      : '—'}
                  </td>
                  <td className="px-4 py-2 text-xs">
                    <span
                      className={
                        l.outcome === 'success'
                          ? 'text-emerald-400'
                          : l.outcome === 'failure'
                          ? 'text-red-400'
                          : 'text-gray-500'
                      }
                    >
                      {l.outcome ?? '—'}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

// ── Failover card ─────────────────────────────────────────────────────────────

function FailoverToggle({
  label,
  description,
  checked,
  onChange,
  disabled,
}: {
  label: string
  description: string
  checked: boolean
  onChange: (v: boolean) => void
  disabled?: boolean
}) {
  return (
    <label className="flex items-start justify-between gap-4 py-3 border-b border-gray-800 cursor-pointer last:border-0">
      <div className="min-w-0">
        <p className="text-sm font-medium text-gray-200">{label}</p>
        <p className="text-xs text-gray-500 mt-0.5">{description}</p>
      </div>
      <button
        role="switch"
        aria-checked={checked}
        onClick={() => onChange(!checked)}
        disabled={disabled}
        className={`relative shrink-0 mt-0.5 w-10 h-5 rounded-full transition-colors ${checked ? 'bg-blue-600' : 'bg-gray-700'} disabled:opacity-40 disabled:cursor-not-allowed`}
      >
        <span
          className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full shadow transition-transform ${checked ? 'translate-x-5' : 'translate-x-0'}`}
        />
      </button>
    </label>
  )
}

function FailoverCard({ cluster, nodes }: { cluster: Cluster; nodes?: Node[] }) {
  const qc = useQueryClient()

  const [autoFailover, setAutoFailover] = useState(cluster.auto_failover)
  const [autoFailback, setAutoFailback] = useState(cluster.auto_failback)
  const [appTierAlwaysAvailable, setAppTierAlwaysAvailable] = useState(cluster.app_tier_always_available)
  const [failoverOnMaintenance, setFailoverOnMaintenance] = useState(cluster.failover_on_maintenance)
  const [delaySecs, setDelaySecs] = useState(String(cluster.failover_delay_secs || 30))
  const [sentinelMaster, setSentinelMaster] = useState(cluster.redis_sentinel_master || 'netbox')
  const [saveBackup, setSaveBackup] = useState(true)
  const [primaryNodeId, setPrimaryNodeId] = useState('')

  // Warning modal state (shown when Patroni is already configured)
  const [showWarning, setShowWarning] = useState(false)

  // Result panel shown after a successful configure
  const [result, setResult] = useState<ConfigureFailoverResult | null>(null)

  // Stay in sync if parent cluster data refreshes
  useEffect(() => {
    setAutoFailover(cluster.auto_failover)
    setAutoFailback(cluster.auto_failback)
    setAppTierAlwaysAvailable(cluster.app_tier_always_available)
    setFailoverOnMaintenance(cluster.failover_on_maintenance)
    setDelaySecs(String(cluster.failover_delay_secs || 30))
    setSentinelMaster(cluster.redis_sentinel_master || 'netbox')
  }, [
    cluster.auto_failover,
    cluster.auto_failback,
    cluster.app_tier_always_available,
    cluster.failover_on_maintenance,
    cluster.failover_delay_secs,
    cluster.redis_sentinel_master,
  ])

  const configure = useMutation({
    mutationFn: () =>
      clustersApi.configureFailover(cluster.id, {
        auto_failover: autoFailover,
        auto_failback: autoFailback,
        app_tier_always_available: appTierAlwaysAvailable,
        failover_on_maintenance: failoverOnMaintenance,
        failover_delay_secs: Math.max(1, parseInt(delaySecs, 10) || 30),
        vip: cluster.vip ?? null,
        redis_sentinel_master: sentinelMaster,
        save_backup: saveBackup,
        primary_node_id: primaryNodeId || undefined,
      }),
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ['cluster', cluster.id] })
      setShowWarning(false)
      setResult(data)
    },
  })

  const isActiveStandby = cluster.mode === 'active_standby'
  const isPending = configure.isPending

  function handleConfigureClick() {
    if (cluster.patroni_configured) {
      setShowWarning(true)
    } else {
      configure.mutate()
    }
  }

  return (
    <div className="bg-gray-900 border border-gray-800 rounded-xl p-6 col-span-full">
      <div className="flex items-center justify-between mb-1">
        <h3 className="font-medium">Failover</h3>
        {!isActiveStandby && (
          <span className="text-xs text-gray-500 bg-gray-800 px-2 py-0.5 rounded">active/standby only</span>
        )}
      </div>
      <p className="text-xs text-gray-500 mb-4">
        Configure how Conductor responds to node outages and maintenance events.
        Clicking <strong className="text-gray-300">Configure Failover</strong> pushes Patroni and
        (if enabled) Sentinel configs to all nodes and starts the Patroni witness on the conductor.
      </p>

      <FailoverToggle
        label="App tier is always available"
        description="All nodes run NetBox pointed at the current Patroni primary. A reverse proxy health check steers traffic to healthy nodes automatically. Redis Sentinel handles Redis HA."
        checked={appTierAlwaysAvailable}
        onChange={setAppTierAlwaysAvailable}
        disabled={!isActiveStandby || isPending}
      />

      {/* Sentinel master name — only relevant when app tier is always available */}
      {appTierAlwaysAvailable && (
        <div className="flex items-center justify-between py-3 pl-10">
          <div>
            <p className="text-sm font-medium text-gray-300">Redis Sentinel master name</p>
            <p className="text-xs text-gray-500 mt-0.5">
              Must match the <code className="text-gray-400">sentinel monitor</code> name in your Sentinel config.
            </p>
          </div>
          <input
            type="text"
            value={sentinelMaster}
            onChange={(e) => setSentinelMaster(e.target.value)}
            disabled={isPending}
            placeholder="netbox"
            className="w-36 text-sm bg-gray-800 border border-gray-700 rounded px-2 py-1 text-gray-200 focus:outline-none focus:border-gray-500 disabled:opacity-40"
          />
        </div>
      )}

      <FailoverToggle
        label="Automatic failover"
        description="When the active node disconnects, Conductor starts NetBox on the highest-priority standby after the delay below."
        checked={autoFailover}
        onChange={setAutoFailover}
        disabled={!isActiveStandby || isPending}
      />
      <FailoverToggle
        label="Automatic failback"
        description="When a higher-priority node reconnects and is healthy, Conductor moves NetBox back to it automatically."
        checked={autoFailback}
        onChange={setAutoFailback}
        disabled={!isActiveStandby || !autoFailover || isPending}
      />
      <FailoverToggle
        label="Failover on maintenance mode"
        description="When a node is put into maintenance mode while running NetBox, Conductor immediately moves it to the next candidate."
        checked={failoverOnMaintenance}
        onChange={setFailoverOnMaintenance}
        disabled={!isActiveStandby || !autoFailover || isPending}
      />

      {/* Failover delay */}
      <div className="flex items-center justify-between py-3">
        <div>
          <p className="text-sm font-medium text-gray-200">Failover delay</p>
          <p className="text-xs text-gray-500 mt-0.5">
            Seconds Conductor waits for a disconnected node to reconnect before triggering failover.
          </p>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <input
            type="number"
            min={1}
            max={300}
            value={delaySecs}
            onChange={(e) => setDelaySecs(e.target.value)}
            disabled={!isActiveStandby || !autoFailover || isPending}
            className="w-20 text-sm text-right bg-gray-800 border border-gray-700 rounded px-2 py-1 text-gray-200 focus:outline-none focus:border-gray-500 disabled:opacity-40"
          />
          <span className="text-xs text-gray-500">seconds</span>
        </div>
      </div>

      {/* Standby-mode notice */}
      {!appTierAlwaysAvailable && isActiveStandby && (
        <p className="text-xs text-amber-500/80 bg-amber-950/30 border border-amber-900/40 rounded px-3 py-2 mt-1">
          Standby nodes will return database errors for write operations. Only the active
          (Patroni-primary) node serves traffic. Clients must be pointed at the active node directly.
        </p>
      )}

      {/* Backup checkbox */}
      {isActiveStandby && (
        <label className="flex items-start gap-3 py-3 cursor-pointer select-none">
          <input
            type="checkbox"
            checked={saveBackup}
            onChange={(e) => setSaveBackup(e.target.checked)}
            disabled={isPending}
            className="mt-0.5 accent-blue-500"
          />
          <div>
            <p className="text-sm font-medium text-gray-200">Back up primary database before configuring</p>
            <p className="text-xs text-gray-500 mt-0.5">
              Runs <code className="text-gray-400">pg_dump</code> on the primary node and saves the dump to{' '}
              <code className="text-gray-400">/var/lib/postgresql/backups/</code>. Recommended on first setup.
            </p>
          </div>
        </label>
      )}

      {/* Primary node selector — shown when multiple nodes exist so the operator can
          pin the primary explicitly. When left on "Auto", the backend picks the
          highest-priority connected node that's running NetBox. */}
      {nodes && nodes.length > 1 && isActiveStandby && (
        <div className="flex items-center justify-between py-3 border-b border-gray-800">
          <div>
            <p className="text-sm font-medium text-gray-200">Primary node</p>
            <p className="text-xs text-gray-500 mt-0.5">
              The node that will become the Patroni primary. Auto selects the highest-priority running node.
            </p>
          </div>
          <select
            value={primaryNodeId}
            onChange={(e) => setPrimaryNodeId(e.target.value)}
            disabled={isPending}
            className="w-44 text-sm bg-gray-800 border border-gray-700 rounded px-2 py-1 text-gray-200 focus:outline-none focus:border-gray-500 disabled:opacity-40"
          >
            <option value="">Auto</option>
            {nodes.map((n) => (
              <option key={n.id} value={n.id}>
                {n.hostname}
              </option>
            ))}
          </select>
        </div>
      )}

      {/* Result panel */}
      {result && (
        <div className="mt-4 p-4 bg-gray-800/60 border border-gray-700 rounded-lg text-xs space-y-2">
          <p className="text-emerald-400 font-medium">Configuration dispatched</p>
          <p className="text-gray-400">Primary: <span className="text-gray-200">{result.primary_node}</span></p>
          {result.witness_addr && (
            <p className="text-gray-400">Witness: <span className="text-gray-200">{result.witness_addr}</span></p>
          )}
          {result.backup_task && (
            <p className="text-gray-400">
              Backup task: <span className="text-gray-200">{result.backup_task.task_id}</span>
              {' '}on {result.backup_task.hostname}
            </p>
          )}
          <p className="text-gray-400">
            Patroni tasks: <span className="text-gray-200">{result.patroni_tasks.length}</span>
            {result.sentinel_tasks.length > 0 && (
              <>, Sentinel tasks: <span className="text-gray-200">{result.sentinel_tasks.length}</span></>
            )}
          </p>
          {result.warnings.length > 0 && (
            <ul className="text-amber-400 space-y-0.5">
              {result.warnings.map((w, i) => <li key={i}>⚠ {w}</li>)}
            </ul>
          )}
          <div className="flex items-center gap-4 mt-1">
            <button
              onClick={() => setResult(null)}
              className="text-gray-500 hover:text-gray-300 text-xs"
            >
              Dismiss
            </button>
            <Link
              to={`/clusters/${cluster.id}?tab=logs`}
              className="text-xs text-blue-400 hover:text-blue-300 font-medium transition-colors"
            >
              Follow →
            </Link>
          </div>
        </div>
      )}

      {/* Action row */}
      <div className="flex items-center justify-between mt-4 pt-4 border-t border-gray-800">
        {configure.isError && (
          <p className="text-xs text-red-400">
            {(configure.error as { response?: { data?: { message?: string } } })?.response?.data?.message ?? 'Configuration failed — check node connectivity and try again'}
          </p>
        )}
        {!configure.isError && <span />}
        <button
          onClick={handleConfigureClick}
          disabled={!isActiveStandby || isPending}
          className="text-sm bg-blue-600 hover:bg-blue-500 disabled:opacity-40 disabled:cursor-not-allowed px-5 py-1.5 rounded-lg transition-colors font-medium"
        >
          {isPending ? 'Configuring…' : 'Configure Failover'}
        </button>
      </div>

      {/* Restart warning modal */}
      {showWarning && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
          <div className="bg-gray-900 border border-gray-700 rounded-xl p-6 max-w-md w-full shadow-2xl">
            <h4 className="font-semibold text-gray-100 mb-2">Reconfigure Patroni?</h4>
            <p className="text-sm text-gray-400 mb-4">
              Patroni has already been configured for this cluster. Proceeding will push a new{' '}
              <code className="text-gray-300">patroni.yml</code> and restart Patroni on all nodes
              simultaneously. This causes a brief outage while a new primary is elected.
            </p>
            <div className="flex justify-end gap-3">
              <button
                onClick={() => setShowWarning(false)}
                className="text-sm px-4 py-1.5 rounded-lg bg-gray-800 hover:bg-gray-700 text-gray-300 transition-colors"
              >
                Cancel
              </button>
              <button
                onClick={() => configure.mutate()}
                className="text-sm px-4 py-1.5 rounded-lg bg-amber-600 hover:bg-amber-500 text-white transition-colors font-medium"
              >
                Reconfigure anyway
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

// ── Media sync card ───────────────────────────────────────────────────────────

function MediaSyncCard({ cluster }: { cluster: Cluster }) {
  const qc = useQueryClient()

  // rows mirrors the editable folder list; always has at least one row
  const serverFolders = cluster.extra_sync_folders ?? []
  const [rows, setRows] = useState<string[]>(() =>
    serverFolders.length > 0 ? serverFolders : ['']
  )
  const [dirty, setDirty] = useState(false)
  const [syncResult, setSyncResult] = useState<ClusterSyncResult | null>(null)
  const [syncError, setSyncError] = useState<string | null>(null)

  // Sync rows back from server after a successful save
  useEffect(() => {
    const f = cluster.extra_sync_folders ?? []
    setRows(f.length > 0 ? f : [''])
    setDirty(false)
  }, [cluster.extra_sync_folders])

  const updateSettings = useMutation({
    mutationFn: (body: { media_sync_enabled: boolean; extra_folders_sync_enabled: boolean; extra_sync_folders: string[] }) =>
      clustersApi.updateMediaSyncSettings(cluster.id, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['cluster', cluster.id] }),
  })

  const syncNow = useMutation({
    mutationFn: () => clustersApi.syncMedia(cluster.id),
    onSuccess: (data) => { setSyncResult(data); setSyncError(null) },
    onError: (e: unknown) => {
      const msg = (e as { response?: { data?: { message?: string } } })?.response?.data?.message ?? 'Sync failed'
      setSyncError(msg)
      setSyncResult(null)
    },
  })

  function toggle(field: 'media_sync_enabled' | 'extra_folders_sync_enabled') {
    updateSettings.mutate({
      media_sync_enabled: cluster.media_sync_enabled,
      extra_folders_sync_enabled: cluster.extra_folders_sync_enabled,
      extra_sync_folders: rows.filter((r) => r.trim() !== ''),
      [field]: !cluster[field],
    })
  }

  function updateRow(i: number, value: string) {
    setRows((prev) => prev.map((r, idx) => (idx === i ? value : r)))
    setDirty(true)
  }

  function removeRow(i: number) {
    setRows((prev) => {
      const next = prev.filter((_, idx) => idx !== i)
      return next.length > 0 ? next : ['']
    })
    setDirty(true)
  }

  function addRow() {
    setRows((prev) => [...prev, ''])
  }

  function saveFolders() {
    updateSettings.mutate({
      media_sync_enabled: cluster.media_sync_enabled,
      extra_folders_sync_enabled: cluster.extra_folders_sync_enabled,
      extra_sync_folders: rows.filter((r) => r.trim() !== ''),
    })
  }

  return (
    <div className="bg-gray-900 border border-gray-800 rounded-xl p-6 col-span-full">
      <div className="flex items-center justify-between mb-4">
        <h3 className="font-medium">Media Sync</h3>
        <button
          onClick={() => { setSyncResult(null); setSyncError(null); syncNow.mutate() }}
          disabled={!cluster.media_sync_enabled || syncNow.isPending}
          className="text-sm bg-blue-600 hover:bg-blue-500 disabled:opacity-40 disabled:cursor-not-allowed px-4 py-1.5 rounded-lg transition-colors"
          title={!cluster.media_sync_enabled ? 'Enable media sync first' : 'Pull from active node, push to others'}
        >
          {syncNow.isPending ? 'Syncing…' : 'Sync Now'}
        </button>
      </div>

      <p className="text-xs text-gray-500 mb-4">
        When triggered, the conductor pulls from the most recently active app-tier or hyperconverged
        node and pushes to all other connected nodes of the same type.
      </p>

      {/* Toggle: Sync Media */}
      <label className="flex items-center justify-between py-2 border-b border-gray-800 cursor-pointer">
        <span className="text-sm">Sync Media Root (<code className="text-xs text-gray-400">NETBOX_MEDIA_ROOT</code>)</span>
        <button
          role="switch"
          aria-checked={cluster.media_sync_enabled}
          onClick={() => toggle('media_sync_enabled')}
          disabled={updateSettings.isPending}
          className={`relative w-10 h-5 rounded-full transition-colors ${cluster.media_sync_enabled ? 'bg-blue-600' : 'bg-gray-700'}`}
        >
          <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full shadow transition-transform ${cluster.media_sync_enabled ? 'translate-x-5' : 'translate-x-0'}`} />
        </button>
      </label>

      {/* Toggle: Sync Additional Folders */}
      <label className="flex items-center justify-between py-2 border-b border-gray-800 cursor-pointer">
        <span className="text-sm text-gray-300">Sync Additional Folders</span>
        <button
          role="switch"
          aria-checked={cluster.extra_folders_sync_enabled}
          onClick={() => toggle('extra_folders_sync_enabled')}
          disabled={updateSettings.isPending || !cluster.media_sync_enabled}
          className={`relative w-10 h-5 rounded-full transition-colors ${cluster.extra_folders_sync_enabled && cluster.media_sync_enabled ? 'bg-blue-600' : 'bg-gray-700'} disabled:opacity-40`}
        >
          <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full shadow transition-transform ${cluster.extra_folders_sync_enabled && cluster.media_sync_enabled ? 'translate-x-5' : 'translate-x-0'}`} />
        </button>
      </label>

      {/* Editable folder rows */}
      {cluster.extra_folders_sync_enabled && cluster.media_sync_enabled && (
        <div className="mt-3 space-y-1.5">
          {rows.map((row, i) => (
            <div key={i} className="flex items-center gap-2">
              <input
                type="text"
                value={row}
                onChange={(e) => updateRow(i, e.target.value)}
                placeholder="/opt/netbox/custom-path"
                className="flex-1 text-xs font-mono bg-gray-800 border border-gray-700 rounded px-2 py-1 text-gray-200 placeholder-gray-600 focus:outline-none focus:border-gray-500"
              />
              <button
                onClick={() => removeRow(i)}
                className="text-gray-500 hover:text-red-400 text-xs transition-colors shrink-0"
              >
                remove
              </button>
            </div>
          ))}
          <div className="flex items-center justify-between mt-2">
            <button
              onClick={addRow}
              className="text-xs text-gray-400 hover:text-gray-200 transition-colors"
            >
              + Add Row
            </button>
            <button
              onClick={saveFolders}
              disabled={!dirty || updateSettings.isPending}
              className="text-xs bg-blue-600 hover:bg-blue-500 disabled:opacity-40 disabled:cursor-not-allowed px-3 py-1 rounded transition-colors"
            >
              {updateSettings.isPending ? 'Saving…' : 'Save'}
            </button>
          </div>
        </div>
      )}

      {/* Sync result / error */}
      {syncResult && (
        <div className="mt-4 text-xs text-emerald-400 bg-emerald-900/20 border border-emerald-800 rounded-lg p-3">
          <p className="font-medium mb-1">Sync started — source: <span className="font-mono">{syncResult.source_hostname}</span></p>
          <ul className="space-y-0.5 text-emerald-300">
            {syncResult.syncs.map((s) => (
              <li key={s.transfer_id}>
                → <span className="font-mono">{s.target_hostname}</span>
                {s.source_path && <span className="text-emerald-500"> ({s.source_path})</span>}
                <span className="text-emerald-600 ml-1">task {s.task_id.slice(0, 8)}…</span>
              </li>
            ))}
          </ul>
        </div>
      )}
      {syncError && (
        <p className="mt-3 text-xs text-red-400">{syncError}</p>
      )}
    </div>
  )
}

function FailoverHistoryTab({ events }: { events: FailoverEvent[] }) {
  if (events.length === 0) {
    return (
      <div className="text-center py-16 text-gray-500 text-sm">
        No failover events recorded yet. Events appear here when automatic
        failover, failback, or maintenance-triggered moves occur.
      </div>
    )
  }

  const eventLabel: Record<string, string> = {
    failover: 'Failover',
    failback: 'Failback',
    maintenance_failover: 'Maintenance',
  }
  const triggerLabel: Record<string, string> = {
    disconnect: 'Agent disconnected',
    heartbeat: 'NetBox stopped',
    maintenance: 'Maintenance mode',
    reconnect: 'Node reconnected',
  }

  return (
    <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-gray-800 text-gray-400">
            <th className="text-left px-6 py-3 font-medium">Time</th>
            <th className="text-left px-6 py-3 font-medium">Type</th>
            <th className="text-left px-6 py-3 font-medium">Trigger</th>
            <th className="text-left px-6 py-3 font-medium">From</th>
            <th className="text-left px-6 py-3 font-medium">To</th>
            <th className="text-left px-6 py-3 font-medium">Result</th>
          </tr>
        </thead>
        <tbody>
          {events.map((ev) => (
            <tr key={ev.id} className="border-b border-gray-800 last:border-0 hover:bg-gray-800/30">
              <td className="px-6 py-3 text-gray-400 whitespace-nowrap">
                {new Date(ev.occurred_at).toLocaleString()}
              </td>
              <td className="px-6 py-3 font-medium">
                {eventLabel[ev.event_type] ?? ev.event_type}
              </td>
              <td className="px-6 py-3 text-gray-400">
                {triggerLabel[ev.trigger] ?? ev.trigger}
              </td>
              <td className="px-6 py-3 text-gray-300">
                {ev.failed_node_name || '—'}
              </td>
              <td className="px-6 py-3 text-gray-300">
                {ev.target_node_name || '—'}
              </td>
              <td className="px-6 py-3">
                {ev.success ? (
                  <span className="text-emerald-400 font-medium">Success</span>
                ) : (
                  <span className="text-red-400 font-medium" title={ev.reason ?? ''}>
                    Failed{ev.reason ? ` — ${ev.reason}` : ''}
                  </span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// ── Logs tab ──────────────────────────────────────────────────────────────────

const LOG_LEVEL_COLOR: Record<string, string> = {
  debug: 'text-gray-500',
  info: 'text-gray-300',
  warn: 'text-amber-400',
  error: 'text-red-400',
}

const LOG_SOURCE_BADGE: Record<string, string> = {
  conductor: 'bg-blue-900/40 text-blue-300',
  agent: 'bg-purple-900/40 text-purple-300',
  netbox: 'bg-emerald-900/40 text-emerald-300',
}

function LogsTab({ clusterId }: { clusterId: string }) {
  const [minLevel, setMinLevel] = useState('')
  const { data: entries = [], isLoading, refetch } = useQuery({
    queryKey: ['cluster-logs', clusterId, minLevel],
    queryFn: () => alertsApi.clusterLogs(clusterId, { level: minLevel || undefined, limit: 300 }),
    refetchInterval: 15_000,
  })

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="font-medium">Cluster Logs</h3>
        <div className="flex items-center gap-3">
          <select
            value={minLevel}
            onChange={(e) => setMinLevel(e.target.value)}
            className="text-xs bg-gray-800 border border-gray-700 rounded px-2 py-1 text-gray-300 focus:outline-none"
          >
            <option value="">All levels</option>
            <option value="debug">Debug+</option>
            <option value="info">Info+</option>
            <option value="warn">Warn+</option>
            <option value="error">Error only</option>
          </select>
          <button onClick={() => refetch()} className="text-xs text-gray-500 hover:text-gray-300">
            Refresh
          </button>
        </div>
      </div>

      {isLoading ? (
        <p className="text-gray-500 text-sm">Loading…</p>
      ) : entries.length === 0 ? (
        <p className="text-gray-500 text-sm">No log entries yet.</p>
      ) : (
        <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
          <div className="max-h-[32rem] overflow-y-auto divide-y divide-gray-800/60">
            {entries.map((e: ClusterLogEntry) => (
              <div key={e.id} className="px-4 py-2 flex items-start gap-3 text-xs">
                <span className="text-gray-600 flex-shrink-0 w-36 font-mono">
                  {new Date(e.occurred_at).toLocaleTimeString()}
                </span>
                <span className={`font-bold w-10 flex-shrink-0 ${LOG_LEVEL_COLOR[e.level] ?? 'text-gray-400'}`}>
                  {e.level.toUpperCase()}
                </span>
                <span className={`text-xs px-1.5 py-0.5 rounded flex-shrink-0 ${LOG_SOURCE_BADGE[e.source] ?? 'bg-gray-800 text-gray-400'}`}>
                  {e.source}
                </span>
                {e.hostname && (
                  <span className="text-gray-500 flex-shrink-0">{e.hostname}</span>
                )}
                <span className="text-gray-200 break-all">{e.message}</span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

type Tab = 'nodes' | 'database' | 'settings' | 'deployment' | 'history' | 'audit' | 'logs'

export default function ClusterDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const initialTab = (new URLSearchParams(window.location.search).get('tab') as Tab | null) ?? 'nodes'
  const [tab, setTab] = useState<Tab>(initialTab)
  const [showWizard, setShowWizard] = useState(false)
  const [editingNode, setEditingNode] = useState<Node | null>(null)
  const [showDeleteDialog, setShowDeleteDialog] = useState(false)
  const [showAgentMenu, setShowAgentMenu] = useState(false)
  const agentMenuRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!showAgentMenu) return
    const handler = (e: MouseEvent) => {
      if (agentMenuRef.current && !agentMenuRef.current.contains(e.target as HTMLElement)) {
        setShowAgentMenu(false)
      }
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [showAgentMenu])
  const [generatedCreds, setGeneratedCreds] = useState<GeneratedCredential[] | null>(null)

  const { data: cluster, isLoading: loadingCluster } = useQuery({
    queryKey: ['cluster', id],
    queryFn: () => clustersApi.get(id!),
    enabled: !!id,
    refetchInterval: 30_000,
  })

  const { data: nodes, isLoading: loadingNodes, refetch: refetchNodes } = useQuery({
    queryKey: ['nodes', id],
    queryFn: () => nodesApi.list(id!),
    enabled: !!id,
    refetchInterval: 15_000,
  })

  const { data: credentials } = useQuery({
    queryKey: ['credentials', id],
    queryFn: () => credentialsApi.list(id!),
    enabled: !!id && tab === 'settings',
  })

  const { data: failoverEvents } = useQuery({
    queryKey: ['failover-events', id],
    queryFn: () => clustersApi.failoverEvents(id!),
    enabled: !!id && tab === 'history',
    refetchInterval: tab === 'history' ? 15_000 : false,
  })

  const deleteCluster = useMutation({
    mutationFn: () => clustersApi.delete(id!),
    onSuccess: () => {
      qc.removeQueries({ queryKey: ['cluster', id] })
      navigate('/clusters')
    },
  })

  const generateCreds = useMutation({
    mutationFn: () => credentialsApi.generateCredentials(id!),
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ['credentials', id] })
      setGeneratedCreds(data.generated)
    },
  })

  if (loadingCluster) {
    return (
      <Layout>
        <div className="text-gray-500 text-sm">Loading…</div>
      </Layout>
    )
  }

  if (!cluster) {
    return (
      <Layout>
        <div className="text-red-400">Cluster not found.</div>
      </Layout>
    )
  }

  const connectedCount = nodes?.filter((n) => n.agent_status === 'connected').length ?? 0

  // Prefer the live version reported by any connected node over the DB-stored cluster field,
  // which may still be the coarse "4.x" value set at cluster creation.
  const netboxVersion =
    nodes?.find((n) => n.agent_status === 'connected' && n.netbox_version)?.netbox_version
    ?? cluster.netbox_version

  return (
    <Layout>
      {/* Breadcrumb */}
      <div className="flex items-center gap-2 text-sm text-gray-500 mb-6">
        <Link to="/clusters" className="hover:text-gray-300 transition-colors">
          Clusters
        </Link>
        <span>/</span>
        <span className="text-white">{cluster.name}</span>
      </div>

      {/* Header */}
      <div className="flex items-start justify-between mb-6">
        <div>
          <h2 className="text-2xl font-semibold">{cluster.name}</h2>
          <div className="flex items-center gap-3 mt-1 text-sm text-gray-400">
            <span>{cluster.mode === 'active_standby' ? 'Active / Standby' : 'HA'}</span>
            <span>·</span>
            <span>Patroni scope: <code className="font-mono text-xs">{cluster.patroni_scope}</code></span>
            <span>·</span>
            <span>NetBox {netboxVersion}</span>
            <span>·</span>
            <span className={cluster.auto_failover ? 'text-emerald-400' : 'text-gray-500'}>
              Auto-failover {cluster.auto_failover ? 'on' : 'off'}
            </span>
          </div>
        </div>
        <button
          onClick={() => setShowDeleteDialog(true)}
          className="text-sm text-red-500 hover:text-red-400 border border-red-900 hover:border-red-700 px-3 py-1.5 rounded-lg transition-colors"
        >
          Delete cluster
        </button>
      </div>

      {/* Stats row */}
      <div className="grid grid-cols-3 gap-4 mb-6">
        {[
          { label: 'Nodes', value: nodes?.length ?? '—' },
          { label: 'Connected', value: connectedCount },
          { label: 'Mode', value: cluster.mode === 'active_standby' ? 'A/S' : 'HA' },
        ].map((s) => (
          <div key={s.label} className="bg-gray-900 border border-gray-800 rounded-xl p-4">
            <p className="text-xs text-gray-500 mb-1">{s.label}</p>
            <p className="text-2xl font-semibold">{s.value}</p>
          </div>
        ))}
      </div>

      {/* Tabs */}
      <div className="border-b border-gray-800 mb-6">
        {(['nodes', 'database', 'settings', 'deployment', 'history', 'audit', 'logs'] as Tab[]).map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={`px-4 py-2 text-sm capitalize border-b-2 -mb-px transition-colors ${
              tab === t
                ? 'border-blue-500 text-white'
                : 'border-transparent text-gray-400 hover:text-white'
            }`}
          >
            {t}
          </button>
        ))}
      </div>

      {/* Nodes tab */}
      {tab === 'nodes' && (
        <div>
          <div className="flex items-center justify-between mb-4">
            <h3 className="font-medium">Nodes ({nodes?.length ?? 0})</h3>
            <div className="flex gap-2">
              {/* Download agent dropdown */}
              <div className="relative" ref={agentMenuRef}>
                <button
                  onClick={() => setShowAgentMenu((v) => !v)}
                  className="bg-gray-800 hover:bg-gray-700 text-sm px-3 py-2 rounded-lg transition-colors"
                >
                  Download agent ▾
                </button>
                {showAgentMenu && (
                  <div className="absolute right-0 top-full mt-1 bg-gray-800 border border-gray-700 rounded-lg shadow-xl z-10 min-w-max">
                    <a
                      href="/api/v1/downloads/agent-linux-amd64"
                      download
                      onClick={() => setShowAgentMenu(false)}
                      className="block px-4 py-2 text-sm hover:bg-gray-700 rounded-t-lg"
                    >
                      Linux amd64
                    </a>
                    <a
                      href="/api/v1/downloads/agent-linux-arm64"
                      download
                      onClick={() => setShowAgentMenu(false)}
                      className="block px-4 py-2 text-sm hover:bg-gray-700 rounded-b-lg"
                    >
                      Linux arm64
                    </a>
                  </div>
                )}
              </div>
              <button
                onClick={() => setShowWizard(true)}
                className="bg-blue-600 hover:bg-blue-500 text-sm font-medium px-4 py-2 rounded-lg transition-colors"
              >
                + Add Node
              </button>
            </div>
          </div>

          {loadingNodes ? (
            <p className="text-gray-500 text-sm">Loading…</p>
          ) : nodes && nodes.length > 0 ? (
            <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-gray-800 text-gray-400">
                    <th className="text-left px-6 py-3 font-medium">Hostname</th>
                    <th className="px-6 py-3 font-medium text-center">Active App</th>
                    <th className="px-6 py-3 font-medium text-center">Active DB</th>
                    <th className="text-left px-6 py-3 font-medium">Role</th>
                    <th className="text-left px-6 py-3 font-medium">Agent</th>
                    <th className="text-left px-6 py-3 font-medium">Services</th>
                    <th className="text-left px-6 py-3 font-medium">Priority</th>
                    <th className="px-4 py-3"></th>
                  </tr>
                </thead>
                <tbody>
                  {nodes.map((node) => (
                    <NodeRow key={node.id} node={node} clusterId={id!} onEdit={setEditingNode} />
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <div className="bg-gray-900 border border-gray-800 rounded-xl p-12 text-center">
              <p className="text-gray-400 mb-4">No nodes in this cluster yet.</p>
              <button
                onClick={() => setShowWizard(true)}
                className="bg-blue-600 hover:bg-blue-500 text-sm font-medium px-4 py-2 rounded-lg transition-colors"
              >
                Add your first node
              </button>
            </div>
          )}
        </div>
      )}

      {/* Database tab */}
      {tab === 'database' && id && (
        <DatabaseTab clusterId={id} />
      )}

      {/* Deployment tab */}
      {tab === 'deployment' && id && (
        <DeploymentTab clusterId={id} />
      )}

      {/* Settings tab */}
      {tab === 'settings' && (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
          {/* Cluster info */}
          <div className="bg-gray-900 border border-gray-800 rounded-xl p-6">
            <h3 className="font-medium mb-4">Cluster Info</h3>
            <dl className="space-y-3 text-sm">
              {[
                { label: 'Mode', value: cluster.mode === 'active_standby' ? 'Active / Standby' : 'HA' },
                { label: 'Patroni Scope', value: cluster.patroni_scope },
                { label: 'NetBox Version', value: netboxVersion },
                { label: 'VIP', value: cluster.vip ?? '—' },
                { label: 'Created', value: new Date(cluster.created_at).toLocaleDateString() },
              ].map(({ label, value }) => (
                <div key={label} className="flex justify-between">
                  <dt className="text-gray-500">{label}</dt>
                  <dd className="text-gray-200 font-mono text-xs">{value}</dd>
                </div>
              ))}
            </dl>
          </div>

          {/* Credentials */}
          <div className="bg-gray-900 border border-gray-800 rounded-xl p-6">
            <div className="flex items-center justify-between mb-1">
              <h3 className="font-medium">Credentials</h3>
              <button
                onClick={() => generateCreds.mutate()}
                disabled={generateCreds.isPending}
                className="text-xs bg-gray-800 hover:bg-gray-700 disabled:opacity-40 px-3 py-1 rounded-lg"
              >
                {generateCreds.isPending ? 'Generating…' : 'Auto-generate'}
              </button>
            </div>
            <p className="text-xs text-gray-500 mb-4">
              Passwords are stored AES-256-GCM encrypted. Only the server decrypts them at render time.
            </p>
            {CRED_KINDS.map((kind) => (
              <CredentialRow
                key={kind}
                kind={kind}
                cred={credentials?.find((c) => c.kind === kind)}
                clusterId={id!}
              />
            ))}
          </div>

          {/* Failover */}
          <FailoverCard cluster={cluster} nodes={nodes} />

          {/* Media Sync */}
          <MediaSyncCard cluster={cluster} />
        </div>
      )}

      {tab === 'history' && (
        <FailoverHistoryTab events={failoverEvents ?? []} />
      )}

      {tab === 'audit' && id && <AuditTab clusterId={id} />}

      {tab === 'logs' && id && <LogsTab clusterId={id} />}

      {showWizard && (
        <AddNodeWizard
          clusterId={id!}
          clusterName={cluster.name}
          onClose={() => {
            setShowWizard(false)
            refetchNodes()
          }}
        />
      )}

      {editingNode && (
        <EditNodeModal
          node={editingNode}
          clusterId={id!}
          onClose={() => setEditingNode(null)}
        />
      )}

      {showDeleteDialog && (
        <DeleteClusterDialog
          clusterName={cluster.name}
          onConfirm={() => deleteCluster.mutate()}
          onCancel={() => setShowDeleteDialog(false)}
          isPending={deleteCluster.isPending}
        />
      )}

      {generatedCreds && (
        <GeneratedCredsModal
          generated={generatedCreds}
          onClose={() => setGeneratedCreds(null)}
        />
      )}
    </Layout>
  )
}
