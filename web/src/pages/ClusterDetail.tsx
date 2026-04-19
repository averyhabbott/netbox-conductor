import { useState, useRef, useEffect } from 'react'
import { useParams, Link, useNavigate, useSearchParams } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { clustersApi } from '../api/clusters'
import type { Cluster, ClusterSyncResult, ConfigureFailoverResult, FailoverEvent } from '../api/clusters'
import { nodesApi } from '../api/nodes'
import type { Node, EditNodeBody } from '../api/nodes'
import { credentialsApi, credentialLabels, secretOnlyKinds } from '../api/credentials'
import type { Credential, CredentialKind, GeneratedCredential } from '../api/credentials'
import { configsApi } from '../api/configs'
import type { ReadNodeConfigResult } from '../api/configs'
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

function ServiceBadge({ running, label, unknown }: { running: boolean | null | undefined; label: string; unknown?: boolean }) {
  if (unknown)
    return <span className="text-gray-500 text-xs">{label}: ?</span>
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
  unknown,
}: {
  running: boolean | null | undefined
  label: string
  role?: string
  unknown?: boolean
}) {
  if (unknown)
    return <span className="text-gray-500 text-xs">{label}: ?</span>
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
  const disconnected = node.agent_status !== 'connected'

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
      <td className="px-6 py-4 text-center">
        {!disconnected && node.netbox_running === true && (
          <span className="text-emerald-400 text-base" title="Active app node">✓</span>
        )}
      </td>
      <td className="px-6 py-4 text-center">
        {node.health_status === 'healthy' && (node.patroni_role === 'primary' || node.patroni_role === 'master') && (
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
      {/* Core */}
      <td className="px-6 py-4">
        <div className="flex gap-3">
          <ServiceBadge running={node.netbox_running} label="NetBox" unknown={disconnected} />
          <ServiceBadge running={node.rq_running} label="RQ" unknown={disconnected} />
        </div>
        {node.netbox_version && (
          <div className="text-xs text-gray-500 mt-0.5">nb {node.netbox_version}</div>
        )}
      </td>
      {/* App Tier */}
      <td className="px-6 py-4">
        <div className="flex gap-3">
          <ServiceBadgeWithRole
            running={node.redis_running}
            label="Redis"
            role={node.redis_role}
            unknown={disconnected}
          />
          <ServiceBadge running={node.sentinel_running} label="Sentinel" unknown={disconnected} />
        </div>
      </td>
      {/* DB Tier */}
      <td className="px-6 py-4">
        <div className="flex gap-3">
          <ServiceBadgeWithRole
            running={node.patroni_running}
            label="Patroni"
            role={node.patroni_role}
            unknown={disconnected}
          />
          <ServiceBadge running={node.postgres_running} label="DB" unknown={disconnected} />
        </div>
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
  'redis_tasks_password',
  'redis_caching_password',
  'netbox_secret_key',
  'netbox_api_token_pepper',
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
  const isSecretOnly = secretOnlyKinds.has(kind)

  const save = useMutation({
    mutationFn: () =>
      credentialsApi.upsert(clusterId, kind, {
        username: isSecretOnly ? '' : username,
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
              {!isSecretOnly && cred.username}
              {cred.db_name ? ` · db: ${cred.db_name}` : ''}
              {!isSecretOnly && ' · '}last set {new Date(cred.rotated_at ?? cred.created_at).toLocaleDateString()}
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
      <div className="space-y-2 mb-2">
        {!isSecretOnly && (
          <input
            className="w-full bg-gray-800 border border-gray-700 rounded px-2 py-1 text-sm"
            placeholder="Username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
        )}
        <input
          type="password"
          className="w-full bg-gray-800 border border-gray-700 rounded px-2 py-1 text-sm"
          placeholder={isSecretOnly ? 'Value' : 'Password'}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        {kind === 'netbox_db_user' && (
          <input
            className="w-full bg-gray-800 border border-gray-700 rounded px-2 py-1 text-sm"
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
          disabled={save.isPending || (!isSecretOnly && !username) || !password}
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

// ── Import From Existing wizard ───────────────────────────────────────────────

function ImportFromExistingModal({
  clusterId,
  nodes,
  onClose,
}: {
  clusterId: string
  nodes: { node_id: string; hostname: string; agent_status: string }[]
  onClose: () => void
}) {
  const qc = useQueryClient()
  const [step, setStep] = useState<1 | 2>(1)
  const [selectedNodeId, setSelectedNodeId] = useState(
    nodes.find((n) => n.agent_status === 'connected')?.node_id ?? nodes[0]?.node_id ?? ''
  )
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [parsed, setParsed] = useState<ReadNodeConfigResult['parsed'] | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [importing, setImporting] = useState(false)
  const [importSuccess, setImportSuccess] = useState(false)

  const kindMap: Record<string, CredentialKind> = {
    netbox_secret_key: 'netbox_secret_key',
    netbox_api_token_pepper: 'netbox_api_token_pepper',
    netbox_db_user_password: 'netbox_db_user',
    redis_tasks_password: 'redis_tasks_password',
    redis_caching_password: 'redis_caching_password',
  }

  async function fetchConfig() {
    setLoading(true)
    setError('')
    try {
      const result = await configsApi.readNodeConfig(clusterId, selectedNodeId)
      const found = new Set<string>()
      for (const [k, v] of Object.entries(result.parsed)) {
        if (v) found.add(k)
      }
      setParsed(result.parsed)
      setSelected(found)
      setStep(2)
    } catch (e: any) {
      setError(e.response?.data?.message ?? 'Failed to read config from node')
    } finally {
      setLoading(false)
    }
  }

  async function doImport() {
    if (!parsed) return
    setImporting(true)
    setError('')
    try {
      for (const key of selected) {
        const kind = kindMap[key]
        if (!kind) continue
        const value = parsed[key as keyof typeof parsed]
        if (!value) continue
        const username =
          key === 'netbox_db_user_password' ? (parsed.netbox_db_user_username ?? '') : ''
        await credentialsApi.upsert(clusterId, kind, {
          username,
          password: value,
        })
      }
      qc.invalidateQueries({ queryKey: ['credentials', clusterId] })
      setImportSuccess(true)
    } catch (e: any) {
      setError(e.response?.data?.message ?? 'Import failed')
    } finally {
      setImporting(false)
    }
  }

  const parsedLabels: Record<string, string> = {
    netbox_secret_key: 'NetBox Secret Key',
    netbox_api_token_pepper: 'API Token Pepper',
    netbox_db_user_password: 'NetBox DB Password',
    redis_tasks_password: 'Redis Password (Tasks)',
    redis_caching_password: 'Redis Password (Caching)',
  }

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50">
      <div className="bg-gray-900 border border-gray-700 rounded-xl w-full max-w-md p-6">
        <h3 className="text-lg font-semibold mb-1">Import From Existing</h3>
        <p className="text-xs text-gray-500 mb-4">
          Read credentials from a live node's configuration.py
        </p>

        {importSuccess ? (
          <div>
            <p className="text-sm text-emerald-400 mb-4">Credentials imported successfully.</p>
            <button
              onClick={onClose}
              className="w-full bg-gray-800 hover:bg-gray-700 rounded-lg py-2 text-sm"
            >
              Close
            </button>
          </div>
        ) : step === 1 ? (
          <div className="space-y-4">
            <div>
              <label className="block text-sm text-gray-400 mb-1">Source Node</label>
              <select
                className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm"
                value={selectedNodeId}
                onChange={(e) => setSelectedNodeId(e.target.value)}
              >
                {nodes.map((n) => (
                  <option key={n.node_id} value={n.node_id}>
                    {n.hostname} {n.agent_status !== 'connected' ? '(offline)' : ''}
                  </option>
                ))}
              </select>
            </div>
            {error && <p className="text-sm text-red-400">{error}</p>}
            <div className="flex gap-3">
              <button
                onClick={fetchConfig}
                disabled={loading || !selectedNodeId}
                className="flex-1 bg-blue-600 hover:bg-blue-500 disabled:opacity-50 rounded-lg py-2 text-sm font-medium"
              >
                {loading ? 'Reading…' : 'Next →'}
              </button>
              <button
                onClick={onClose}
                className="flex-1 bg-gray-800 hover:bg-gray-700 rounded-lg py-2 text-sm"
              >
                Cancel
              </button>
            </div>
          </div>
        ) : (
          <div className="space-y-3">
            <p className="text-sm text-gray-400">Select credentials to import:</p>
            {parsed && Object.entries(parsed).map(([key, value]) => {
              if (!value) return null
              return (
                <label key={key} className="flex items-center gap-3 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={selected.has(key)}
                    onChange={(e) => {
                      const next = new Set(selected)
                      if (e.target.checked) next.add(key)
                      else next.delete(key)
                      setSelected(next)
                    }}
                    className="rounded"
                  />
                  <span className="text-sm flex-1">{parsedLabels[key] ?? key}</span>
                  <span className="text-xs text-gray-500 font-mono">
                    {value.slice(0, 4)}{'*'.repeat(Math.min(8, value.length - 4))}
                  </span>
                </label>
              )
            })}
            {error && <p className="text-sm text-red-400">{error}</p>}
            <div className="flex gap-3 pt-2">
              <button
                onClick={doImport}
                disabled={importing || selected.size === 0}
                className="flex-1 bg-blue-600 hover:bg-blue-500 disabled:opacity-50 rounded-lg py-2 text-sm font-medium"
              >
                {importing ? 'Importing…' : 'Import'}
              </button>
              <button
                onClick={onClose}
                className="flex-1 bg-gray-800 hover:bg-gray-700 rounded-lg py-2 text-sm"
              >
                Cancel
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

// ── Sync Config modal ─────────────────────────────────────────────────────────

function SyncConfigModal({
  clusterId,
  nodes,
  onClose,
}: {
  clusterId: string
  nodes: { node_id: string; hostname: string; agent_status: string }[]
  onClose: () => void
}) {
  const connectedNodes = nodes.filter((n) => n.agent_status === 'connected')
  const [step, setStep] = useState<1 | 2>(1)
  const [sourceNodeId, setSourceNodeId] = useState(connectedNodes[0]?.node_id ?? '')
  const [destIds, setDestIds] = useState<Set<string>>(
    new Set(nodes.filter((n) => n.node_id !== sourceNodeId).map((n) => n.node_id))
  )
  const [restartAfter, setRestartAfter] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [content, setContent] = useState('')
  const [pushResult, setPushResult] = useState<{ nodes: { node_id: string; hostname: string; status: string; error?: string }[] } | null>(null)
  const [syncing, setSyncing] = useState(false)

  async function fetchLiveConfig() {
    if (!sourceNodeId) return
    setLoading(true)
    setError('')
    try {
      const result = await configsApi.readNodeConfig(clusterId, sourceNodeId)
      setContent(result.raw_config)
      setStep(2)
    } catch (e: any) {
      setError(e.response?.data?.message ?? 'Failed to read config from node')
    } finally {
      setLoading(false)
    }
  }

  async function doSync() {
    setSyncing(true)
    setError('')
    try {
      const result = await configsApi.syncConfig(clusterId, {
        source_node_id: sourceNodeId,
        destination_node_ids: Array.from(destIds),
        content,
        restart_after: restartAfter,
      })
      setPushResult(result)
    } catch (e: any) {
      setError(e.response?.data?.message ?? 'Sync failed')
    } finally {
      setSyncing(false)
    }
  }

  const destNodes = nodes.filter((n) => n.node_id !== sourceNodeId)

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50">
      <div className="bg-gray-900 border border-gray-700 rounded-xl w-full max-w-2xl p-6">
        <h3 className="text-lg font-semibold mb-1">Sync Config</h3>
        <p className="text-xs text-gray-500 mb-4">
          Read the live configuration.py from a source node, review and edit it, then push to destinations.
        </p>

        {pushResult ? (
          <div className="space-y-2">
            <p className="text-sm font-medium mb-3">Push results:</p>
            {pushResult.nodes.map((n) => (
              <div key={n.node_id} className="flex items-center justify-between text-sm py-1.5 border-b border-gray-800 last:border-0">
                <span className="text-gray-300">{n.hostname}</span>
                <span className={
                  n.status === 'dispatched' ? 'text-emerald-400' :
                  n.status === 'offline' ? 'text-red-400' : 'text-amber-400'
                }>
                  {n.status}{n.error ? `: ${n.error}` : ''}
                </span>
              </div>
            ))}
            <button
              onClick={onClose}
              className="w-full mt-4 bg-gray-800 hover:bg-gray-700 rounded-lg py-2 text-sm"
            >
              Close
            </button>
          </div>
        ) : step === 1 ? (
          <div className="space-y-4">
            <div>
              <label className="block text-sm text-gray-400 mb-1">Source Node</label>
              <select
                className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm"
                value={sourceNodeId}
                onChange={(e) => {
                  setSourceNodeId(e.target.value)
                  setDestIds(new Set(nodes.filter((n) => n.node_id !== e.target.value).map((n) => n.node_id)))
                }}
              >
                {connectedNodes.map((n) => (
                  <option key={n.node_id} value={n.node_id}>{n.hostname}</option>
                ))}
              </select>
            </div>
            <div>
              <label className="block text-sm text-gray-400 mb-2">Destination Nodes</label>
              <div className="space-y-1.5">
                {destNodes.map((n) => (
                  <label key={n.node_id} className="flex items-center gap-2 cursor-pointer">
                    <input
                      type="checkbox"
                      checked={destIds.has(n.node_id)}
                      onChange={(e) => {
                        const next = new Set(destIds)
                        if (e.target.checked) next.add(n.node_id)
                        else next.delete(n.node_id)
                        setDestIds(next)
                      }}
                      className="rounded"
                    />
                    <span className="text-sm">{n.hostname}</span>
                    {n.agent_status !== 'connected' && (
                      <span className="text-xs text-red-400">(offline)</span>
                    )}
                  </label>
                ))}
              </div>
            </div>
            <label className="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                checked={restartAfter}
                onChange={(e) => setRestartAfter(e.target.checked)}
                className="rounded"
              />
              <span className="text-sm text-gray-300">Restart NetBox after sync</span>
            </label>
            {error && <p className="text-sm text-red-400">{error}</p>}
            <div className="flex gap-3">
              <button
                onClick={fetchLiveConfig}
                disabled={loading || !sourceNodeId || destIds.size === 0}
                className="flex-1 bg-blue-600 hover:bg-blue-500 disabled:opacity-50 rounded-lg py-2 text-sm font-medium"
              >
                {loading ? 'Reading config…' : 'Next →'}
              </button>
              <button
                onClick={onClose}
                className="flex-1 bg-gray-800 hover:bg-gray-700 rounded-lg py-2 text-sm"
              >
                Cancel
              </button>
            </div>
          </div>
        ) : (
          <div className="space-y-4">
            <div>
              <p className="text-xs text-gray-500 mb-1">
                Pushing to: {destNodes.filter((n) => destIds.has(n.node_id)).map((n) => n.hostname).join(', ')}
                {restartAfter && ' · Will restart NetBox'}
              </p>
              <textarea
                className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-xs font-mono h-72 resize-y"
                value={content}
                onChange={(e) => setContent(e.target.value)}
                spellCheck={false}
              />
            </div>
            {error && <p className="text-sm text-red-400">{error}</p>}
            <div className="flex gap-3">
              <button
                onClick={doSync}
                disabled={syncing || !content}
                className="flex-1 bg-blue-600 hover:bg-blue-500 disabled:opacity-50 rounded-lg py-2 text-sm font-medium"
              >
                {syncing ? 'Syncing…' : 'Sync'}
              </button>
              <button
                onClick={() => setStep(1)}
                className="bg-gray-800 hover:bg-gray-700 rounded-lg py-2 px-4 text-sm"
              >
                ← Back
              </button>
              <button
                onClick={onClose}
                className="bg-gray-800 hover:bg-gray-700 rounded-lg py-2 px-4 text-sm"
              >
                Cancel
              </button>
            </div>
          </div>
        )}
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

// ── Witness row ───────────────────────────────────────────────────────────────

function WitnessRow({ clusterId }: { clusterId: string }) {
  const { data } = useQuery({
    queryKey: ['patroni-topology', clusterId],
    queryFn: () => patroniApi.topology(clusterId),
    refetchInterval: 15_000,
  })
  if (!data?.witness_addr) return null
  return (
    <p className="text-xs text-gray-500">
      Witness: <span className="font-mono">{data.witness_addr}</span>
    </p>
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
    <label className="flex items-start justify-between gap-4 py-3 border-b border-gray-800 cursor-pointer">
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

function FailoverCard({ cluster, nodes, onOpenSyncModal }: { cluster: Cluster; nodes?: Node[]; onOpenSyncModal: () => void }) {
  const qc = useQueryClient()

  const [autoFailover, setAutoFailover] = useState(cluster.auto_failover)
  const [autoFailback, setAutoFailback] = useState(cluster.auto_failback)
  const [appTierAlwaysAvailable, setAppTierAlwaysAvailable] = useState(cluster.app_tier_always_available)
  const [failoverOnMaintenance, setFailoverOnMaintenance] = useState(cluster.failover_on_maintenance)
  const [delaySecs, setDelaySecs] = useState(String(cluster.failover_delay_secs || 30))
  const [failbackMultiplier, setFailbackMultiplier] = useState(String(cluster.failback_multiplier || 3))
  const [sentinelMaster, setSentinelMaster] = useState(cluster.redis_sentinel_master || 'netbox')
  const [saveBackup, setSaveBackup] = useState(true)
  const [primaryNodeId, setPrimaryNodeId] = useState('')

  const [showWarning, setShowWarning] = useState(false)
  const [showNoConfigWarning, setShowNoConfigWarning] = useState(false)
  const [result, setResult] = useState<ConfigureFailoverResult | null>(null)

  const { data: configData } = useQuery({
    queryKey: ['config', cluster.id],
    queryFn: () => configsApi.getOrCreate(cluster.id),
  })
  const hasConfigTemplate = !!configData && !configData.config?.is_default

  const [patroniPushResult, setPatroniPushResult] = useState<PushResult[] | null>(null)
  const [sentinelPushResult, setSentinelPushResult] = useState<PushResult[] | null>(null)
  const [sentinelRestart, setSentinelRestart] = useState(false)

  useEffect(() => {
    setAutoFailover(cluster.auto_failover)
    setAutoFailback(cluster.auto_failback)
    setAppTierAlwaysAvailable(cluster.app_tier_always_available)
    setFailoverOnMaintenance(cluster.failover_on_maintenance)
    setDelaySecs(String(cluster.failover_delay_secs || 30))
    setFailbackMultiplier(String(cluster.failback_multiplier || 3))
    setSentinelMaster(cluster.redis_sentinel_master || 'netbox')
  }, [
    cluster.auto_failover,
    cluster.auto_failback,
    cluster.app_tier_always_available,
    cluster.failover_on_maintenance,
    cluster.failover_delay_secs,
    cluster.failback_multiplier,
    cluster.redis_sentinel_master,
  ])

  const configure = useMutation({
    mutationFn: () =>
      clustersApi.configureFailover(cluster.id, {
        auto_failover: autoFailover,
        auto_failback: autoFailback,
        app_tier_always_available: appTierAlwaysAvailable,
        failover_on_maintenance: failoverOnMaintenance,
        failover_delay_secs: Math.max(10, parseInt(delaySecs, 10) || 30),
        failback_multiplier: Math.max(1, parseInt(failbackMultiplier, 10) || 3),
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

  const pushPatroni = useMutation({
    mutationFn: () => patroniApi.pushPatroniConfig(cluster.id),
    onSuccess: (data) => setPatroniPushResult(data.nodes),
  })

  const pushSentinel = useMutation({
    mutationFn: () => patroniApi.pushSentinelConfig(cluster.id, sentinelRestart),
    onSuccess: (data) => setSentinelPushResult(data.nodes),
  })

  const isActiveStandby = cluster.mode === 'active_standby'
  const isPending = configure.isPending

  const computedFailbackDelay =
    Math.max(10, parseInt(delaySecs, 10) || 30) *
    Math.max(1, parseInt(failbackMultiplier, 10) || 3)

  function handleConfigureClick() {
    if (!hasConfigTemplate) {
      setShowNoConfigWarning(true)
    } else if (cluster.patroni_configured) {
      setShowWarning(true)
    } else {
      configure.mutate()
    }
  }

  return (
    <div className="bg-gray-900 border border-gray-800 rounded-xl p-6">
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

      {/* App tier is always available */}
      <FailoverToggle
        label="App tier is always available"
        description="All nodes run NetBox pointed at the current Patroni primary. A reverse proxy health check steers traffic to healthy nodes automatically. Redis Sentinel handles Redis HA."
        checked={appTierAlwaysAvailable}
        onChange={setAppTierAlwaysAvailable}
        disabled={!isActiveStandby || isPending}
      />
      {appTierAlwaysAvailable && (
        <div className="flex items-center justify-between py-3 pl-10 border-b border-gray-800">
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

      {/* Automatic failover */}
      <FailoverToggle
        label="Automatic failover"
        description="When the active node disconnects, Conductor starts NetBox on the highest-priority standby after the delay below."
        checked={autoFailover}
        onChange={setAutoFailover}
        disabled={!isActiveStandby || isPending}
      />
      {/* Failover delay — sub-item */}
      <div className="flex items-center justify-between py-3 pl-10 border-b border-gray-800">
        <div>
          <p className="text-sm font-medium text-gray-200">Failover delay</p>
          <p className="text-xs text-gray-500 mt-0.5">
            Seconds Conductor waits for a disconnected node to reconnect before triggering failover. Minimum 10s.
          </p>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <input
            type="number"
            min={10}
            max={300}
            value={delaySecs}
            onChange={(e) => setDelaySecs(e.target.value)}
            disabled={!isActiveStandby || !autoFailover || isPending}
            className="w-20 text-sm text-right bg-gray-800 border border-gray-700 rounded px-2 py-1 text-gray-200 focus:outline-none focus:border-gray-500 disabled:opacity-40"
          />
          <span className="text-xs text-gray-500">seconds</span>
        </div>
      </div>
      {/* Failover on maintenance mode — sub-item */}
      <div className="pl-10">
        <FailoverToggle
          label="Failover on maintenance mode"
          description="When a node is put into maintenance mode while running NetBox, Conductor immediately moves it to the next candidate."
          checked={failoverOnMaintenance}
          onChange={setFailoverOnMaintenance}
          disabled={!isActiveStandby || !autoFailover || isPending}
        />
      </div>

      {/* Automatic failback */}
      <FailoverToggle
        label="Automatic failback"
        description="When a higher-priority node reconnects and is healthy, Conductor moves NetBox back to it automatically."
        checked={autoFailback}
        onChange={setAutoFailback}
        disabled={!isActiveStandby || !autoFailover || isPending}
      />
      {/* Failback multiplier — sub-item */}
      <div className="flex items-center justify-between py-3 pl-10 border-b border-gray-800">
        <div>
          <p className="text-sm font-medium text-gray-200">Failback multiplier</p>
          <p className="text-xs text-gray-500 mt-0.5">
            Failback delay = failover delay &times; this value. A reconnected node must stay connected
            for this long before Conductor moves services back to it.
          </p>
          <p className="text-xs text-gray-600 mt-0.5">
            = {computedFailbackDelay}s at current delay
          </p>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <input
            type="number"
            min={1}
            max={20}
            value={failbackMultiplier}
            onChange={(e) => setFailbackMultiplier(e.target.value)}
            disabled={!isActiveStandby || !autoFailback || isPending}
            className="w-20 text-sm text-right bg-gray-800 border border-gray-700 rounded px-2 py-1 text-gray-200 focus:outline-none focus:border-gray-500 disabled:opacity-40"
          />
          <span className="text-xs text-gray-500">&times; delay</span>
        </div>
      </div>

      {/* Configure section */}
      <div className="border-t border-gray-800 mt-2 pt-4">
        <p className="text-xs font-medium text-gray-500 uppercase tracking-wide mb-3">Configure</p>

        {/* Standby-mode notice */}
        {!appTierAlwaysAvailable && isActiveStandby && (
          <p className="text-xs text-amber-500/80 bg-amber-950/30 border border-amber-900/40 rounded px-3 py-2 mb-3">
            Standby nodes will return database errors for write operations. Only the active
            (Patroni-primary) node serves traffic. Clients must be pointed at the active node directly.
          </p>
        )}

        {/* Backup checkbox */}
        {isActiveStandby && (
          <label className="flex items-start gap-3 py-3 border-b border-gray-800 cursor-pointer select-none">
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

        {/* Primary node selector */}
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


        {/* Push results */}
        {(patroniPushResult || sentinelPushResult) && (
          <div className="mt-3 space-y-3">
            {patroniPushResult && (
              <div>
                <p className="text-xs text-gray-500 mb-1">Patroni config push</p>
                <PushResultList results={patroniPushResult} />
              </div>
            )}
            {sentinelPushResult && (
              <div>
                <p className="text-xs text-gray-500 mb-1">Sentinel config push</p>
                <PushResultList results={sentinelPushResult} />
              </div>
            )}
          </div>
        )}

        {/* Action row */}
        <div className="flex flex-wrap items-center justify-between gap-3 mt-4 pt-4 border-t border-gray-800">
          <div>
            {configure.isError && (
              <p className="text-xs text-red-400">
                {(configure.error as { response?: { data?: { message?: string } } })?.response?.data?.message ?? 'Configuration failed — check node connectivity and try again'}
              </p>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-2 ml-auto">
            <button
              onClick={() => pushPatroni.mutate()}
              disabled={pushPatroni.isPending}
              className="text-sm bg-gray-700 hover:bg-gray-600 disabled:opacity-40 px-4 py-1.5 rounded-lg transition-colors"
            >
              {pushPatroni.isPending ? 'Pushing…' : 'Push Patroni Config'}
            </button>
            <label className="flex items-center gap-1.5 text-xs text-gray-400 cursor-pointer select-none">
              <input
                type="checkbox"
                checked={sentinelRestart}
                onChange={(e) => setSentinelRestart(e.target.checked)}
                className="accent-blue-500"
              />
              Restart Sentinel
            </label>
            <button
              onClick={() => pushSentinel.mutate()}
              disabled={pushSentinel.isPending}
              className="text-sm bg-gray-700 hover:bg-gray-600 disabled:opacity-40 px-4 py-1.5 rounded-lg transition-colors"
            >
              {pushSentinel.isPending ? 'Pushing…' : 'Push Sentinel Config'}
            </button>
            <button
              onClick={handleConfigureClick}
              disabled={!isActiveStandby || isPending}
              className="text-sm bg-blue-600 hover:bg-blue-500 disabled:opacity-40 disabled:cursor-not-allowed px-5 py-1.5 rounded-lg transition-colors font-medium"
            >
              {isPending ? 'Configuring…' : 'Configure Failover'}
            </button>
          </div>
        </div>
      </div>

      {/* Configure Failover result modal */}
      {result && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
          <div className="bg-gray-900 border border-gray-700 rounded-xl p-6 max-w-md w-full shadow-2xl">
            <h4 className="font-semibold text-emerald-400 mb-3">Configuration dispatched</h4>
            <div className="text-sm space-y-1.5 mb-4">
              <p className="text-gray-400">Primary: <span className="text-gray-200">{result.primary_node}</span></p>
              {result.witness_addr && (
                <p className="text-gray-400">Witness: <span className="text-gray-200">{result.witness_addr}</span></p>
              )}
              {result.backup_task && (
                <p className="text-gray-400">
                  Backup task: <span className="text-gray-200 font-mono text-xs">{result.backup_task.task_id}</span>
                  {' '}on {result.backup_task.hostname}
                </p>
              )}
              <p className="text-gray-400">
                Patroni tasks: <span className="text-gray-200">{result.patroni_tasks.length}</span>
                {result.sentinel_tasks.length > 0 && (
                  <>, Sentinel tasks: <span className="text-gray-200">{result.sentinel_tasks.length}</span></>
                )}
              </p>
            </div>
            {result.warnings.length > 0 && (
              <ul className="text-amber-400 text-sm space-y-0.5 mb-4">
                {result.warnings.map((w, i) => <li key={i}>⚠ {w}</li>)}
              </ul>
            )}
            <div className="flex justify-end gap-3">
              <button
                onClick={() => setResult(null)}
                className="text-sm px-4 py-1.5 rounded-lg bg-gray-800 hover:bg-gray-700 text-gray-300 transition-colors"
              >
                Dismiss
              </button>
              <Link
                to={`?tab=history&sub=cluster-logs`}
                onClick={() => setResult(null)}
                className="text-sm px-4 py-1.5 rounded-lg bg-blue-600 hover:bg-blue-500 text-white transition-colors font-medium"
              >
                Follow →
              </Link>
            </div>
          </div>
        </div>
      )}

      {/* No config template warning modal */}
      {showNoConfigWarning && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
          <div className="bg-gray-900 border border-gray-700 rounded-xl p-6 max-w-md w-full shadow-2xl">
            <h4 className="font-semibold text-gray-100 mb-2">No configuration synced</h4>
            <p className="text-sm text-gray-400 mb-4">
              No <code className="text-gray-300">configuration.py</code> template has been saved for
              this cluster. Configure Failover will generate credentials but won't be able to push an
              updated config to nodes — run the Sync Config utility first to capture the live config.
            </p>
            <div className="flex justify-end gap-3">
              <button
                onClick={() => {
                  setShowNoConfigWarning(false)
                  if (cluster.patroni_configured) {
                    setShowWarning(true)
                  } else {
                    configure.mutate()
                  }
                }}
                className="text-sm px-4 py-1.5 rounded-lg bg-red-700 hover:bg-red-600 text-white transition-colors font-medium"
              >
                Proceed anyway
              </button>
              <button
                onClick={() => {
                  setShowNoConfigWarning(false)
                  onOpenSyncModal()
                }}
                className="text-sm px-4 py-1.5 rounded-lg bg-blue-600 hover:bg-blue-500 text-white transition-colors font-medium"
              >
                Sync Config
              </button>
            </div>
          </div>
        </div>
      )}

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

// ── File sync card (was: Media Sync) ─────────────────────────────────────────

function FileSyncCard({ cluster }: { cluster: Cluster }) {
  const qc = useQueryClient()

  const serverFolders = cluster.extra_sync_folders ?? []
  const [rows, setRows] = useState<string[]>(() =>
    serverFolders.length > 0 ? serverFolders : ['']
  )
  const [dirty, setDirty] = useState(false)
  const [syncResult, setSyncResult] = useState<ClusterSyncResult | null>(null)
  const [syncError, setSyncError] = useState<string | null>(null)

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
    <div className="bg-gray-900 border border-gray-800 rounded-xl p-6">
      <div className="flex items-center justify-between mb-4">
        <h3 className="font-medium">File Sync</h3>
        <button
          onClick={() => { setSyncResult(null); setSyncError(null); syncNow.mutate() }}
          disabled={!cluster.media_sync_enabled || syncNow.isPending}
          className="text-sm bg-blue-600 hover:bg-blue-500 disabled:opacity-40 disabled:cursor-not-allowed px-4 py-1.5 rounded-lg transition-colors"
          title={!cluster.media_sync_enabled ? 'Enable file sync first' : 'Pull from active node, push to others'}
        >
          {syncNow.isPending ? 'Syncing…' : 'Sync Now'}
        </button>
      </div>

      <p className="text-xs text-gray-500 mb-4">
        When triggered, the conductor pulls from the most recently active app-tier or hyperconverged
        node and pushes to all other connected nodes of the same type.
      </p>

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

// ── Failover history tab ──────────────────────────────────────────────────────

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

// ── Cluster logs tab ──────────────────────────────────────────────────────────

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

function ClusterLogsTab({ clusterId }: { clusterId: string }) {
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

// ── Node logs tab ─────────────────────────────────────────────────────────────

function NodeLogView({
  clusterId,
  nodeId,
  source,
  filter,
}: {
  clusterId: string
  nodeId: string
  source: 'agent' | 'netbox'
  filter: string
}) {
  const { data, isLoading, refetch } = useQuery({
    queryKey: ['node-logs', clusterId, nodeId, source],
    queryFn: () => nodesApi.getLogs(clusterId, nodeId, 200, source),
    refetchInterval: 15_000,
  })

  const lines = data?.lines ?? []
  const filtered = filter
    ? lines.filter((l) => l.toLowerCase().includes(filter.toLowerCase()))
    : lines

  return (
    <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
      <div className="flex items-center justify-between px-4 py-2 border-b border-gray-800">
        <span className="text-xs text-gray-500 font-mono">{data?.path ?? '…'}</span>
        <button onClick={() => refetch()} className="text-xs text-gray-500 hover:text-gray-300">
          Refresh
        </button>
      </div>
      {isLoading ? (
        <p className="px-4 py-3 text-gray-500 text-sm">Loading…</p>
      ) : filtered.length === 0 ? (
        <p className="px-4 py-3 text-gray-500 text-sm">
          No log entries{filter ? ' matching filter' : ''}.
        </p>
      ) : (
        <div className="max-h-[32rem] overflow-y-auto">
          {filtered.map((line, i) => (
            <div
              key={i}
              className="px-4 py-0.5 font-mono text-xs text-gray-300 hover:bg-gray-800/30 break-all"
            >
              {line}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function NodeLogsTab({ clusterId, nodes }: { clusterId: string; nodes: Node[] }) {
  const [selectedNodeIds, setSelectedNodeIds] = useState<string[]>(() => nodes.map((n) => n.id))
  const [source, setSource] = useState<'agent' | 'netbox'>('agent')
  const [filter, setFilter] = useState('')
  const [activeNodeId, setActiveNodeId] = useState(nodes[0]?.id ?? '')

  const selectedNodes = nodes.filter((n) => selectedNodeIds.includes(n.id))

  useEffect(() => {
    if (!selectedNodeIds.includes(activeNodeId) && selectedNodes.length > 0) {
      setActiveNodeId(selectedNodes[0].id)
    }
  }, [selectedNodeIds, activeNodeId, selectedNodes])

  function toggleNode(nodeId: string) {
    setSelectedNodeIds((prev) =>
      prev.includes(nodeId) ? prev.filter((id) => id !== nodeId) : [...prev, nodeId]
    )
  }

  if (nodes.length === 0) {
    return <p className="text-gray-500 text-sm">No nodes in this cluster.</p>
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-4">
        <div className="flex items-center gap-2">
          <span className="text-xs text-gray-500">Source:</span>
          <select
            value={source}
            onChange={(e) => setSource(e.target.value as 'agent' | 'netbox')}
            className="text-xs bg-gray-800 border border-gray-700 rounded px-2 py-1 text-gray-300 focus:outline-none"
          >
            <option value="agent">Agent</option>
            <option value="netbox">NetBox</option>
          </select>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <span className="text-xs text-gray-500">Nodes:</span>
          {nodes.map((n) => (
            <label
              key={n.id}
              className="flex items-center gap-1.5 text-xs cursor-pointer text-gray-300"
            >
              <input
                type="checkbox"
                checked={selectedNodeIds.includes(n.id)}
                onChange={() => toggleNode(n.id)}
                className="accent-blue-500"
              />
              {n.hostname}
            </label>
          ))}
        </div>
        <input
          type="text"
          placeholder="Filter lines…"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          className="flex-1 min-w-32 text-xs bg-gray-800 border border-gray-700 rounded px-2 py-1 text-gray-200 placeholder-gray-600 focus:outline-none focus:border-gray-500"
        />
      </div>

      {selectedNodes.length === 0 ? (
        <p className="text-gray-500 text-sm">Select at least one node to view logs.</p>
      ) : (
        <>
          <div className="border-b border-gray-800 flex">
            {selectedNodes.map((n) => (
              <button
                key={n.id}
                onClick={() => setActiveNodeId(n.id)}
                className={`px-4 py-2 text-sm border-b-2 -mb-px transition-colors ${
                  n.id === activeNodeId
                    ? 'border-blue-500 text-white'
                    : 'border-transparent text-gray-400 hover:text-white'
                }`}
              >
                {n.hostname}
              </button>
            ))}
          </div>
          {selectedNodes.map(
            (n) =>
              n.id === activeNodeId && (
                <NodeLogView
                  key={n.id}
                  clusterId={clusterId}
                  nodeId={n.id}
                  source={source}
                  filter={filter}
                />
              )
          )}
        </>
      )}
    </div>
  )
}

// ── Database events tab ───────────────────────────────────────────────────────

function DatabaseEventsTab({ clusterId }: { clusterId: string }) {
  const { data: history, isLoading } = useQuery({
    queryKey: ['patroni-history', clusterId],
    queryFn: () => patroniApi.history(clusterId),
    refetchInterval: 30_000,
  })

  return (
    <div>
      {isLoading ? (
        <p className="text-gray-500 text-sm">Loading…</p>
      ) : !history?.history.length ? (
        <p className="text-gray-500 text-sm">No database events yet.</p>
      ) : (
        <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
          <div className="divide-y divide-gray-800">
            {history.history.map((row) => (
              <div
                key={row.task_id}
                className="flex items-center justify-between text-xs px-4 py-2 hover:bg-gray-800/40"
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
                  <span
                    className={
                      row.status === 'success'
                        ? 'text-emerald-400'
                        : row.status === 'failure'
                        ? 'text-red-400'
                        : ''
                    }
                  >
                    {row.status}
                  </span>
                  <span>{new Date(row.queued_at).toLocaleString()}</span>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

// ── Audit tab ─────────────────────────────────────────────────────────────────

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
        <h3 className="font-medium">API History</h3>
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

// ── Sub-tab bar ───────────────────────────────────────────────────────────────

function SubTabBar({
  tabs,
  active,
  onChange,
}: {
  tabs: { id: string; label: string }[]
  active: string
  onChange: (id: string) => void
}) {
  return (
    <div className="flex gap-1 mb-6 flex-wrap">
      {tabs.map((t) => (
        <button
          key={t.id}
          onClick={() => onChange(t.id)}
          className={`px-3 py-1.5 text-xs rounded-md transition-colors ${
            t.id === active
              ? 'bg-gray-700 text-white'
              : 'text-gray-400 hover:text-white hover:bg-gray-800'
          }`}
        >
          {t.label}
        </button>
      ))}
    </div>
  )
}

// ── Cluster actions dropdown ──────────────────────────────────────────────────

function ClusterActionsDropdown({ clusterId, onAddNode }: { clusterId: string; onAddNode: () => void }) {
  const [open, setOpen] = useState(false)
  const [showSwitchoverModal, setShowSwitchoverModal] = useState(false)
  const [showFailoverModal, setShowFailoverModal] = useState(false)
  const [switchoverCandidate, setSwitchoverCandidate] = useState('')
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as HTMLElement)) setOpen(false)
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  const { data: topology, refetch: refetchTopo } = useQuery({
    queryKey: ['patroni-topology', clusterId],
    queryFn: () => patroniApi.topology(clusterId),
    enabled: showSwitchoverModal || showFailoverModal,
  })

  const primaryNode = topology?.nodes.find(
    (n) => n.patroni_role === 'primary' || n.patroni_role === 'master'
  )
  const replicaNodes =
    topology?.nodes.filter(
      (n) => n.patroni_role !== 'primary' && n.patroni_role !== 'master'
    ) ?? []

  const switchover = useMutation({
    mutationFn: () => patroniApi.switchover(clusterId, switchoverCandidate || undefined),
    onSuccess: () => {
      setShowSwitchoverModal(false)
      refetchTopo()
    },
  })

  const failover = useMutation({
    mutationFn: () => patroniApi.failover(clusterId, primaryNode?.hostname ?? ''),
    onSuccess: () => {
      setShowFailoverModal(false)
      refetchTopo()
    },
  })

  return (
    <>
      <div className="relative" ref={ref}>
        <button
          onClick={() => setOpen((v) => !v)}
          className="text-sm text-gray-300 hover:text-white border border-gray-700 hover:border-gray-500 px-3 py-1.5 rounded-lg transition-colors flex items-center gap-1.5"
        >
          Cluster Actions
          <span className="text-gray-500 text-xs">▾</span>
        </button>
        {open && (
          <div className="absolute right-0 top-full mt-1 bg-gray-900 border border-gray-700 rounded-lg shadow-xl z-10 min-w-56 py-1">
            <button
              onClick={() => { setOpen(false); onAddNode() }}
              className="w-full text-left px-4 py-2 text-sm text-gray-200 hover:bg-gray-800 transition-colors"
            >
              Add Node
            </button>
            <div className="border-t border-gray-800 my-1" />
            <button
              disabled
              className="w-full text-left px-4 py-2 text-sm text-gray-500 cursor-not-allowed opacity-50"
            >
              Cluster Maintenance Mode
            </button>
            <div className="border-t border-gray-800 my-1" />
            <button
              onClick={() => { setOpen(false); setShowSwitchoverModal(true) }}
              className="w-full text-left px-4 py-2 text-sm text-gray-200 hover:bg-gray-800 transition-colors"
            >
              Initiate Graceful DB Switchover
            </button>
            <button
              onClick={() => { setOpen(false); setShowFailoverModal(true) }}
              className="w-full text-left px-4 py-2 text-sm text-red-400 hover:bg-gray-800 transition-colors"
            >
              Initiate Database Failover
            </button>
          </div>
        )}
      </div>

      {/* Switchover modal */}
      {showSwitchoverModal && (
        <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4">
          <div className="bg-gray-900 border border-gray-700 rounded-xl p-6 w-full max-w-md">
            <h3 className="font-semibold mb-1">Graceful DB Switchover</h3>
            <p className="text-sm text-gray-400 mb-4">
              Trigger a graceful Patroni switchover. The current primary will step down and a
              replica will be promoted.
            </p>
            {replicaNodes.length > 1 && (
              <div className="mb-4">
                <label className="block text-xs text-gray-400 mb-1">Candidate (optional)</label>
                <select
                  value={switchoverCandidate}
                  onChange={(e) => setSwitchoverCandidate(e.target.value)}
                  className="w-full text-sm bg-gray-800 border border-gray-700 rounded px-2 py-1 text-gray-200 focus:outline-none focus:border-gray-500"
                >
                  <option value="">Auto (highest-priority replica)</option>
                  {replicaNodes.map((n) => (
                    <option key={n.node_id} value={n.hostname}>
                      {n.hostname}
                    </option>
                  ))}
                </select>
              </div>
            )}
            {switchover.isSuccess && (
              <p className="text-xs text-emerald-400 mb-3">Switchover dispatched.</p>
            )}
            {switchover.isError && (
              <p className="text-xs text-red-400 mb-3">
                {(switchover.error as any)?.response?.data?.message ?? 'Switchover failed'}
              </p>
            )}
            <div className="flex justify-end gap-3">
              <button
                onClick={() => setShowSwitchoverModal(false)}
                className="text-sm px-4 py-1.5 rounded-lg bg-gray-800 hover:bg-gray-700 text-gray-300"
              >
                Cancel
              </button>
              <button
                onClick={() => switchover.mutate()}
                disabled={switchover.isPending}
                className="text-sm px-4 py-1.5 rounded-lg bg-amber-600 hover:bg-amber-500 disabled:opacity-40 text-white font-medium"
              >
                {switchover.isPending ? 'Switching…' : 'Initiate Switchover'}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* DB Failover modal */}
      {showFailoverModal && (
        <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4">
          <div className="bg-gray-900 border border-gray-700 rounded-xl p-6 w-full max-w-md">
            <h3 className="font-semibold text-red-400 mb-1">Force Database Failover</h3>
            <p className="text-sm text-gray-400 mb-4">
              Force an immediate Patroni failover. Use only when the primary is unresponsive and
              switchover is not possible.
              {primaryNode && (
                <>
                  {' '}Current primary:{' '}
                  <span className="font-mono text-gray-200">{primaryNode.hostname}</span>.
                </>
              )}
            </p>
            {failover.isSuccess && (
              <p className="text-xs text-emerald-400 mb-3">Failover dispatched.</p>
            )}
            {failover.isError && (
              <p className="text-xs text-red-400 mb-3">
                {(failover.error as any)?.response?.data?.message ?? 'Failover failed'}
              </p>
            )}
            <div className="flex justify-end gap-3">
              <button
                onClick={() => setShowFailoverModal(false)}
                className="text-sm px-4 py-1.5 rounded-lg bg-gray-800 hover:bg-gray-700 text-gray-300"
              >
                Cancel
              </button>
              <button
                onClick={() => failover.mutate()}
                disabled={failover.isPending || !primaryNode}
                className="text-sm px-4 py-1.5 rounded-lg bg-red-700 hover:bg-red-600 disabled:opacity-40 text-white font-medium"
              >
                {failover.isPending ? 'Failing over…' : 'Force Failover'}
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}

// ── Tab / sub-tab type definitions ────────────────────────────────────────────

type Tab = 'overview' | 'settings' | 'history'

const SETTINGS_TABS = [
  { id: 'general', label: 'General' },
  { id: 'credentials', label: 'Credentials' },
  { id: 'failover', label: 'Failover' },
]

const HISTORY_TABS = [
  { id: 'events', label: 'Events' },
  { id: 'api-history', label: 'API History' },
  { id: 'cluster-logs', label: 'Cluster Logs' },
  { id: 'node-logs', label: 'Node Logs' },
  { id: 'db-events', label: 'Database Events' },
]

// ── Main page ─────────────────────────────────────────────────────────────────

export default function ClusterDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const qc = useQueryClient()

  const [searchParams, setSearchParams] = useSearchParams()
  const tab = (searchParams.get('tab') ?? 'overview') as Tab
  const rawSub = searchParams.get('sub') ?? ''
  const settingsSub = rawSub || 'general'
  const historySub = rawSub || 'events'

  function goToTab(t: Tab) {
    const defaults: Partial<Record<Tab, string>> = { settings: 'general', history: 'events' }
    const sub = defaults[t]
    setSearchParams(sub ? { tab: t, sub } : { tab: t })
  }

  function goToSub(s: string) {
    setSearchParams({ tab, sub: s })
  }

  const [showWizard, setShowWizard] = useState(false)
  const [editingNode, setEditingNode] = useState<Node | null>(null)
  const [showDeleteDialog, setShowDeleteDialog] = useState(false)

  const [generatedCreds, setGeneratedCreds] = useState<GeneratedCredential[] | null>(null)
  const [showImportWizard, setShowImportWizard] = useState(false)
  const [generateConfirmStep, setGenerateConfirmStep] = useState(0) // 0=off 1=confirm
  const [showSyncModal, setShowSyncModal] = useState(false)

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
    mutationFn: (missingOnly: boolean) => credentialsApi.generateCredentials(id!, missingOnly),
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
        <button
          onClick={() => goToTab('overview')}
          className="text-white hover:text-gray-300 transition-colors"
        >
          {cluster.name}
        </button>
      </div>

      {/* Header */}
      <div className="flex items-start justify-between mb-6">
        <div>
          <button
            onClick={() => goToTab('overview')}
            className="text-2xl font-semibold hover:text-gray-300 transition-colors text-left"
          >
            {cluster.name}
          </button>
          {cluster.description && (
            <p className="text-sm text-gray-400 mt-0.5">{cluster.description}</p>
          )}
          <div className="flex items-center gap-3 mt-1 text-sm text-gray-400 flex-wrap">
            {cluster.vip && (
              <>
                <span className="font-mono text-xs">{cluster.vip}</span>
                <span>·</span>
              </>
            )}
            <span>Patroni scope: <code className="font-mono text-xs">{cluster.patroni_scope}</code></span>
            <span>·</span>
            <span>NetBox {netboxVersion}</span>
            <span>·</span>
            <span>Created {new Date(cluster.created_at).toLocaleDateString()}</span>
            <span>·</span>
            <span className={cluster.auto_failover ? 'text-emerald-400' : 'text-gray-500'}>
              Auto-failover {cluster.auto_failover ? 'on' : 'off'}
            </span>
          </div>
        </div>
        <ClusterActionsDropdown clusterId={id!} onAddNode={() => setShowWizard(true)} />
      </div>

      {/* Stats row */}
      <div className="grid grid-cols-3 gap-4 mb-6">
        {[
          { label: 'Nodes', value: nodes?.length ?? '—' },
          { label: 'Connected', value: connectedCount },
          { label: 'Mode', value: cluster.mode === 'active_standby' ? 'Active/Standby' : 'HA' },
        ].map((s) => (
          <div key={s.label} className="bg-gray-900 border border-gray-800 rounded-xl p-4">
            <p className="text-xs text-gray-500 mb-1">{s.label}</p>
            <p className="text-2xl font-semibold">{s.value}</p>
          </div>
        ))}
      </div>

      {/* Top-level tabs */}
      <div className="border-b border-gray-800 mb-6">
        {(['overview', 'settings', 'history'] as Tab[]).map((t) => (
          <button
            key={t}
            onClick={() => goToTab(t)}
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

      {/* ── Overview tab ── */}
      {tab === 'overview' && (
        <div className="space-y-6">
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
                    <th className="text-left px-6 py-3 font-medium">Core</th>
                    <th className="text-left px-6 py-3 font-medium">App Tier</th>
                    <th className="text-left px-6 py-3 font-medium">DB Tier</th>
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
          {id && <WitnessRow clusterId={id} />}
        </div>
      )}

      {/* ── Settings tab ── */}
      {tab === 'settings' && (
        <div>
          <SubTabBar tabs={SETTINGS_TABS} active={settingsSub} onChange={goToSub} />

          {settingsSub === 'general' && (
            <div className="space-y-6">
              {/* NetBox Configuration */}
              <div className="bg-gray-900 border border-gray-800 rounded-xl p-6">
                <h3 className="font-medium mb-1">NetBox Configuration</h3>
                <p className="text-xs text-gray-500 mb-4">
                  Edit and push{' '}
                  <code className="text-xs bg-gray-800 px-1.5 py-0.5 rounded font-mono">configuration.py</code>{' '}
                  to all nodes in this cluster.
                </p>
                <div className="flex items-center gap-3 flex-wrap">
                  <Link
                    to={`/clusters/${id}/config`}
                    className="inline-block bg-blue-600 hover:bg-blue-500 text-sm font-medium px-4 py-2 rounded-lg transition-colors"
                  >
                    Open Config Editor →
                  </Link>
                  <button
                    onClick={() => setShowSyncModal(true)}
                    disabled={!nodes || nodes.filter((n) => n.agent_status === 'connected').length === 0}
                    className="bg-gray-800 hover:bg-gray-700 disabled:opacity-40 text-sm font-medium px-4 py-2 rounded-lg transition-colors"
                  >
                    Sync Config
                  </button>
                </div>
              </div>

              {/* Backup Retention */}
              {id && <RetentionCard clusterId={id} />}

              {/* File Sync */}
              <FileSyncCard cluster={cluster} />

              {/* Delete cluster */}
              <div className="border-t border-gray-800 pt-6">
                <h3 className="font-medium text-red-400 mb-1">Danger Zone</h3>
                <p className="text-sm text-gray-500 mb-4">
                  Permanently delete this cluster, all nodes, credentials, configuration, and history.
                  This cannot be undone.
                </p>
                <button
                  onClick={() => setShowDeleteDialog(true)}
                  className="text-sm text-red-500 hover:text-red-400 border border-red-900 hover:border-red-700 px-4 py-2 rounded-lg transition-colors"
                >
                  Delete Cluster
                </button>
              </div>
            </div>
          )}

          {settingsSub === 'credentials' && (
            <div className="bg-gray-900 border border-gray-800 rounded-xl p-6">
              <div className="flex items-center justify-between mb-1">
                <h3 className="font-medium">Credentials</h3>
                <div className="flex items-center gap-2">
                  <button
                    onClick={() => setShowImportWizard(true)}
                    className="text-xs bg-gray-800 hover:bg-gray-700 px-3 py-1 rounded-lg"
                  >
                    Import From Existing
                  </button>
                  {generateConfirmStep === 0 ? (
                    <button
                      onClick={() => {
                        if (credentials && credentials.length > 0) {
                          setGenerateConfirmStep(1)
                        } else {
                          generateCreds.mutate(false)
                        }
                      }}
                      disabled={generateCreds.isPending}
                      className="text-xs bg-gray-800 hover:bg-gray-700 disabled:opacity-40 px-3 py-1 rounded-lg"
                    >
                      {generateCreds.isPending ? 'Generating…' : 'Auto-generate'}
                    </button>
                  ) : (
                    <div className="flex items-center gap-2">
                      <span className="text-xs text-yellow-400">Overwrite existing credentials?</span>
                      <button
                        onClick={() => { generateCreds.mutate(false); setGenerateConfirmStep(0) }}
                        disabled={generateCreds.isPending}
                        className="text-xs text-gray-400 hover:text-gray-300 disabled:opacity-40"
                      >
                        Yes, overwrite all
                      </button>
                      <button
                        onClick={() => { generateCreds.mutate(true); setGenerateConfirmStep(0) }}
                        disabled={generateCreds.isPending}
                        className="text-xs bg-gray-800 hover:bg-gray-700 disabled:opacity-40 px-2 py-1 rounded"
                      >
                        No, generate missing only
                      </button>
                    </div>
                  )}
                </div>
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
          )}

          {settingsSub === 'failover' && (
            <FailoverCard cluster={cluster} nodes={nodes} onOpenSyncModal={() => setShowSyncModal(true)} />
          )}
        </div>
      )}

      {/* ── History tab ── */}
      {tab === 'history' && id && (
        <div>
          <SubTabBar tabs={HISTORY_TABS} active={historySub} onChange={goToSub} />

          {historySub === 'events' && (
            <FailoverHistoryTab events={failoverEvents ?? []} />
          )}

          {historySub === 'api-history' && (
            <AuditTab clusterId={id} />
          )}

          {historySub === 'cluster-logs' && (
            <ClusterLogsTab clusterId={id} />
          )}

          {historySub === 'node-logs' && (
            <NodeLogsTab clusterId={id} nodes={nodes ?? []} />
          )}

          {historySub === 'db-events' && (
            <DatabaseEventsTab clusterId={id} />
          )}
        </div>
      )}

      {/* Modals */}
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

      {showImportWizard && nodes && (
        <ImportFromExistingModal
          clusterId={id!}
          nodes={nodes.map((n) => ({ node_id: n.id, hostname: n.hostname, agent_status: n.agent_status }))}
          onClose={() => setShowImportWizard(false)}
        />
      )}

      {showSyncModal && nodes && (
        <SyncConfigModal
          clusterId={id!}
          nodes={nodes.map((n) => ({ node_id: n.id, hostname: n.hostname, agent_status: n.agent_status }))}
          onClose={() => setShowSyncModal(false)}
        />
      )}
    </Layout>
  )
}
