import { useState, useRef, useEffect } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { clustersApi } from '../api/clusters'
import { nodesApi } from '../api/nodes'
import type { Node } from '../api/nodes'
import { credentialsApi, credentialLabels } from '../api/credentials'
import type { Credential, CredentialKind, GeneratedCredential } from '../api/credentials'
import { patroniApi } from '../api/patroni'
import type { PushResult } from '../api/patroni'
import client from '../api/client'
import Layout from '../components/Layout'
import AddNodeWizard from './AddNodeWizard'

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

function ServiceBadge({ running, label }: { running: boolean | null; label: string }) {
  if (running === null)
    return <span className="text-gray-600 text-xs">{label}: —</span>
  return (
    <span
      className={`text-xs ${running ? 'text-emerald-400' : 'text-red-400'}`}
    >
      {label}: {running ? '✓' : '✗'}
    </span>
  )
}

function NodeRow({ node, clusterId }: { node: Node; clusterId: string }) {
  return (
    <tr className="border-b border-gray-800 last:border-0 hover:bg-gray-800/40">
      <td className="px-6 py-4">
        <div className="flex items-center gap-2">
          <span className="font-medium">{node.hostname}</span>
          {node.maintenance_mode && (
            <span className="text-xs px-1.5 py-0.5 rounded bg-amber-900/50 text-amber-400 border border-amber-800">
              maintenance
            </span>
          )}
        </div>
        <div className="text-xs text-gray-500 font-mono">{node.ip_address}</div>
      </td>
      <td className="px-6 py-4 text-gray-300 text-sm capitalize">
        {node.role.replace('_', ' ')}
      </td>
      <td className="px-6 py-4">
        <AgentStatusBadge status={node.agent_status} />
      </td>
      <td className="px-6 py-4">
        <div className="flex gap-3">
          <ServiceBadge running={node.netbox_running} label="NetBox" />
          <ServiceBadge running={node.rq_running} label="RQ" />
        </div>
      </td>
      <td className="px-6 py-4 text-gray-400 text-sm">{node.failover_priority}</td>
      <td className="px-6 py-4 text-right">
        <Link
          to={`/clusters/${clusterId}/nodes/${node.id}`}
          className="text-blue-400 hover:text-blue-300 text-sm transition-colors"
        >
          Details →
        </Link>
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
    onSuccess: (d) => setMsg(d.message ?? 'Enforcement dispatched'),
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

// ── Patroni tab ───────────────────────────────────────────────────────────────

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

function PatroniTab({ clusterId }: { clusterId: string }) {
  const [patroniPushResult, setPatroniPushResult] = useState<PushResult[] | null>(null)
  const [sentinelPushResult, setSentinelPushResult] = useState<PushResult[] | null>(null)
  const [sentinelRestart, setSentinelRestart] = useState(false)
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

  const pushPatroni = useMutation({
    mutationFn: () => patroniApi.pushPatroniConfig(clusterId),
    onSuccess: (data) => setPatroniPushResult(data.nodes),
  })

  const pushSentinel = useMutation({
    mutationFn: () => patroniApi.pushSentinelConfig(clusterId, sentinelRestart),
    onSuccess: (data) => setSentinelPushResult(data.nodes),
  })

  const switchover = useMutation({
    mutationFn: () => patroniApi.switchover(clusterId),
    onSuccess: () => refetchTopo(),
  })

  const failover = useMutation({
    mutationFn: () => patroniApi.failover(clusterId),
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
      {/* Topology */}
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

        {/* History */}
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

      {/* Actions panel */}
      <div className="space-y-4">
        {/* Push Patroni config */}
        <div className="bg-gray-900 border border-gray-800 rounded-xl p-5">
          <h3 className="font-medium mb-1">Push Patroni Config</h3>
          <p className="text-xs text-gray-500 mb-3">
            Renders and writes <code className="font-mono">patroni.yml</code> to all DB/hyperconverged nodes.
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
        <div className="bg-gray-900 border border-gray-800 rounded-xl p-5">
          <h3 className="font-medium mb-1">Push Sentinel Config</h3>
          <p className="text-xs text-gray-500 mb-3">
            Renders and writes <code className="font-mono">sentinel.conf</code> to all nodes using the Redis password credential.
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

        {/* Switchover */}
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

        {/* Failover */}
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

        {/* Retention */}
        <RetentionCard clusterId={clusterId} />
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

type Tab = 'nodes' | 'configuration' | 'patroni' | 'settings' | 'audit'

export default function ClusterDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [tab, setTab] = useState<Tab>('nodes')
  const [showWizard, setShowWizard] = useState(false)
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
            <span>NetBox {cluster.netbox_version}</span>
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
        {(['nodes', 'configuration', 'patroni', 'settings', 'audit'] as Tab[]).map((t) => (
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
                    <th className="text-left px-6 py-3 font-medium">Role</th>
                    <th className="text-left px-6 py-3 font-medium">Agent</th>
                    <th className="text-left px-6 py-3 font-medium">Services</th>
                    <th className="text-left px-6 py-3 font-medium">Priority</th>
                    <th className="px-6 py-3" />
                  </tr>
                </thead>
                <tbody>
                  {nodes.map((node) => (
                    <NodeRow key={node.id} node={node} clusterId={id!} />
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

      {/* Configuration tab */}
      {tab === 'configuration' && (
        <div className="bg-gray-900 border border-gray-800 rounded-xl p-8 text-center">
          <p className="text-gray-400 mb-4">
            Edit and push <code className="text-xs bg-gray-800 px-1.5 py-0.5 rounded font-mono">configuration.py</code> to all nodes in this cluster.
          </p>
          <Link
            to={`/clusters/${id}/config`}
            className="inline-block bg-blue-600 hover:bg-blue-500 text-sm font-medium px-4 py-2 rounded-lg transition-colors"
          >
            Open Config Editor →
          </Link>
        </div>
      )}

      {/* Patroni tab */}
      {tab === 'patroni' && id && (
        <PatroniTab clusterId={id} />
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
                { label: 'NetBox Version', value: cluster.netbox_version },
                { label: 'VIP', value: cluster.vip ?? '—' },
                { label: 'Auto Failover', value: cluster.auto_failover ? 'Enabled' : 'Disabled' },
                { label: 'Auto Failback', value: cluster.auto_failback ? 'Enabled' : 'Disabled' },
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
        </div>
      )}

      {tab === 'audit' && id && <AuditTab clusterId={id} />}

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
