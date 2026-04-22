import { useState, useRef, useEffect } from 'react'
import { useParams, Link, useNavigate, useSearchParams } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { clustersApi } from '../api/clusters'
import type { Cluster, ClusterSyncResult, ConfigureFailoverResult, BackupTarget, BackupTargetType, CreateBackupTargetBody } from '../api/clusters'
import { nodesApi } from '../api/nodes'
import type { Node, EditNodeBody } from '../api/nodes'
import { credentialsApi, credentialLabels, secretOnlyKinds } from '../api/credentials'
import type { Credential, CredentialKind, GeneratedCredential } from '../api/credentials'
import { configsApi } from '../api/configs'
import type { ReadNodeConfigResult } from '../api/configs'
import { patroniApi } from '../api/patroni'
import type { PushResult } from '../api/patroni'
import { alertsApi } from '../api/alerts'
import { eventsApi } from '../api/events'
import type { Event } from '../api/events'
import EventsTable from '../components/EventsTable'
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
              autoFocus
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
    redis_tasks_password: 'Redis Password',
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

function FailoverHistoryTab({ events }: { events: Event[] }) {
  if (events.length === 0) {
    return (
      <div className="text-center py-16 text-gray-500 text-sm">
        No failover events recorded yet. Events appear here when automatic
        failover, failback, or maintenance-triggered moves occur.
      </div>
    )
  }

  const codeLabel: Record<string, string> = {
    'NBC-HA-001': 'Failover',
    'NBC-HA-002': 'Failover Complete',
    'NBC-HA-003': 'Failover Failed',
    'NBC-HA-004': 'Failback',
    'NBC-HA-005': 'Failback Complete',
    'NBC-HA-006': 'Role Changed',
    'NBC-HA-007': 'Maint. Failover',
  }
  const sevColor: Record<string, string> = {
    info:     'text-blue-400',
    warn:     'text-amber-400',
    error:    'text-red-400',
    critical: 'text-red-300 font-semibold',
  }

  return (
    <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-gray-800 text-gray-400">
            <th className="text-left px-6 py-3 font-medium">Time</th>
            <th className="text-left px-6 py-3 font-medium">Type</th>
            <th className="text-left px-6 py-3 font-medium">Node</th>
            <th className="text-left px-6 py-3 font-medium">Details</th>
          </tr>
        </thead>
        <tbody>
          {events.map((ev) => (
            <tr key={ev.id} className="border-b border-gray-800 last:border-0 hover:bg-gray-800/30">
              <td className="px-6 py-3 text-gray-400 whitespace-nowrap text-xs font-mono">
                {new Date(ev.occurred_at).toLocaleString()}
              </td>
              <td className="px-6 py-3">
                <span className={`font-medium ${sevColor[ev.severity] ?? 'text-gray-300'}`}>
                  {codeLabel[ev.code] ?? ev.code}
                </span>
              </td>
              <td className="px-6 py-3 text-gray-300 text-xs">
                {ev.node_name ?? '—'}
              </td>
              <td className="px-6 py-3 text-gray-400 text-xs">
                {ev.message}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// ── Cluster logs tab ──────────────────────────────────────────────────────────

function ClusterLogsTab() {
  const { data, isLoading, refetch } = useQuery({
    queryKey: ['system-logs'],
    queryFn: () => alertsApi.systemLogs(300),
    refetchInterval: 15_000,
  })

  const lines = data?.lines ?? []

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="font-medium">Conductor Logs</h3>
        <button onClick={() => refetch()} className="text-xs text-gray-500 hover:text-gray-300">Refresh</button>
      </div>
      {isLoading ? (
        <p className="text-gray-500 text-sm">Loading…</p>
      ) : (
        <div className="bg-gray-950 border border-gray-800 rounded-xl overflow-hidden">
          <div className="max-h-[32rem] overflow-y-auto p-4 font-mono text-xs text-gray-400 space-y-0.5">
            {lines.length === 0 ? (
              <p className="text-gray-600">No log lines available.</p>
            ) : (
              lines.map((line, i) => <div key={i} className="break-all">{line}</div>)
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function ClusterEventsTab({ clusterId }: { clusterId: string }) {
  const [category, setCategory] = useState('')
  const { data: events = [], isLoading, refetch } = useQuery({
    queryKey: ['cluster-events', clusterId, category],
    queryFn: () => eventsApi.listForCluster(clusterId, { category: category || undefined, limit: 200 }),
    refetchInterval: 15_000,
  })

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <h3 className="font-medium">Cluster Events</h3>
          <select value={category} onChange={(e) => setCategory(e.target.value)}
            className="text-xs bg-gray-800 border border-gray-700 rounded px-2 py-1 text-gray-300 focus:outline-none">
            <option value="">All categories</option>
            {['cluster','service','ha','config','agent'].map((c) => (
              <option key={c} value={c}>{c}</option>
            ))}
          </select>
        </div>
        <button onClick={() => refetch()} className="text-xs text-gray-500 hover:text-gray-300">Refresh</button>
      </div>
      <div className="bg-gray-900 border border-gray-800 rounded-xl p-4">
        <EventsTable events={events} loading={isLoading} showNode />
      </div>
    </div>
  )
}

// ── Node logs tab ─────────────────────────────────────────────────────────────

function NodeLogView({
  clusterId,
  nodeId,
  source,
  filter,
  hideHeartbeats,
}: {
  clusterId: string
  nodeId: string
  source: 'agent' | 'netbox'
  filter: string
  hideHeartbeats: boolean
}) {
  const { data, isLoading, refetch } = useQuery({
    queryKey: ['node-logs', clusterId, nodeId, source],
    queryFn: () => nodesApi.getLogs(clusterId, nodeId, 200, source),
    refetchInterval: 15_000,
  })

  const lines = data?.lines ?? []
  const filtered = lines
    .filter((l) => !hideHeartbeats || !l.includes('msg=heartbeat') && !l.includes('"heartbeat"'))
    .filter((l) => !filter || l.toLowerCase().includes(filter.toLowerCase()))

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
  const [hideHeartbeats, setHideHeartbeats] = useState(true)
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
        <label className="flex items-center gap-1.5 text-xs cursor-pointer text-gray-400 whitespace-nowrap">
          <input
            type="checkbox"
            checked={hideHeartbeats}
            onChange={(e) => setHideHeartbeats(e.target.checked)}
            className="accent-blue-500"
          />
          Filter heartbeats
        </label>
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
                  hideHeartbeats={hideHeartbeats}
                />
              )
          )}
        </>
      )}
    </div>
  )
}

// ── Database events tab ───────────────────────────────────────────────────────

function DatabaseEventRow({ row }: { row: import('../api/patroni').HistoryRow }) {
  const [expanded, setExpanded] = useState(false)

  const req = row.request_payload as Record<string, unknown> | undefined
  const res = row.response_payload as Record<string, unknown> | undefined

  const cmdLabel = row.task_type === 'exec.run' && req
    ? [req.command as string, ...((req.args as string[]) ?? [])].join(' ')
    : null

  const hasDetail = !!(res?.output || res?.error)

  return (
    <div className="border-b border-gray-800 last:border-0">
      <div
        className={`flex items-center justify-between text-xs px-4 py-2 ${hasDetail ? 'cursor-pointer hover:bg-gray-800/40' : 'hover:bg-gray-800/40'}`}
        onClick={() => hasDetail && setExpanded((v) => !v)}
      >
        <div className="flex items-center gap-3 min-w-0">
          <span
            className={`w-1.5 h-1.5 rounded-full flex-shrink-0 ${
              row.status === 'success'
                ? 'bg-emerald-400'
                : row.status === 'failure' || row.status === 'timeout'
                ? 'bg-red-400'
                : 'bg-amber-400'
            }`}
          />
          <span className="font-mono text-gray-400 flex-shrink-0">{row.task_type}</span>
          {cmdLabel && (
            <span className="font-mono text-gray-500 truncate" title={cmdLabel}>{cmdLabel}</span>
          )}
          <span className="text-gray-600 flex-shrink-0">{row.hostname}</span>
        </div>
        <div className="flex items-center gap-3 text-gray-500 flex-shrink-0">
          {hasDetail && (
            <span className="text-gray-600">{expanded ? '▲' : '▼'}</span>
          )}
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
      {expanded && hasDetail && (
        <div className="px-4 pb-3 font-mono text-xs text-gray-400 whitespace-pre-wrap bg-gray-950/60">
          {res?.output ? (
            <div className="text-gray-300">{String(res.output)}</div>
          ) : null}
          {res?.error ? (
            <div className="text-red-400">{String(res.error)}</div>
          ) : null}
        </div>
      )}
    </div>
  )
}

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
              <DatabaseEventRow key={row.task_id} row={row} />
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
                    {(l.target_name || l.target_id) ? (
                      <span className="ml-1 text-gray-300">
                        {l.target_name ?? String(l.target_id).slice(0, 8) + '…'}
                      </span>
                    ) : null}
                  </td>
                  <td className="px-4 py-2 text-xs text-gray-400">
                    {l.actor_username
                      ? l.actor_username
                      : l.actor_node_name
                      ? 'agent:' + l.actor_node_name
                      : l.actor_user_id
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

// ── Backups tab ───────────────────────────────────────────────────────────────

const TARGET_TYPE_LABELS: Record<BackupTargetType, string> = {
  posix: 'Local Disk',
  s3: 'Amazon S3',
  gcs: 'Google Cloud',
  azure: 'Azure Blob',
  sftp: 'SFTP Server',
}

const STORAGE_TYPE_HINTS: Record<BackupTargetType, string> = {
  posix: 'Use a network-mounted path (NFS/SMB) to share backups across nodes',
  s3: 'Supports AWS S3 and S3-compatible services (MinIO, Wasabi, Backblaze B2)',
  gcs: 'Google Cloud Storage bucket',
  azure: 'Azure Blob Storage container',
  sftp: 'SFTP server remote path',
}

function AddStorageModal({
  clusterId,
  onClose,
  existing,
  dbNodes = [],
}: {
  clusterId: string
  onClose: () => void
  existing?: BackupTarget
  dbNodes?: Node[]
}) {
  const qc = useQueryClient()
  const [type, setType] = useState<BackupTargetType>(existing?.target_type ?? 'posix')
  const [label, setLabel] = useState(existing?.label ?? '')
  const [posixPath, setPosixPath] = useState(existing?.posix_path ?? '')
  const [s3Bucket, setS3Bucket] = useState(existing?.s3_bucket ?? '')
  const [s3Region, setS3Region] = useState(existing?.s3_region ?? '')
  const [s3Endpoint, setS3Endpoint] = useState(existing?.s3_endpoint ?? '')
  const [s3KeyId, setS3KeyId] = useState('')
  const [s3Secret, setS3Secret] = useState('')
  const [gcsBucket, setGcsBucket] = useState(existing?.gcs_bucket ?? '')
  const [gcsKey, setGcsKey] = useState('')
  const [azureAccount, setAzureAccount] = useState(existing?.azure_account ?? '')
  const [azureContainer, setAzureContainer] = useState(existing?.azure_container ?? '')
  const [azureKey, setAzureKey] = useState('')
  const [sftpHost, setSftpHost] = useState(existing?.sftp_host ?? '')
  const [sftpPort, setSftpPort] = useState(existing?.sftp_port ?? 22)
  const [sftpUser, setSftpUser] = useState(existing?.sftp_user ?? '')
  const [sftpKey, setSftpKey] = useState('')
  const [sftpPath, setSftpPath] = useState(existing?.sftp_path ?? '')
  const [recoveryDays, setRecoveryDays] = useState(existing?.recovery_days ?? 14)
  const [syncEnabled, setSyncEnabled] = useState((existing?.sync_to_nodes?.length ?? 0) > 0)
  const [syncToNodes, setSyncToNodes] = useState<string[]>(existing?.sync_to_nodes ?? [])
  const [error, setError] = useState('')

  const [testStatus, setTestStatus] = useState<'idle' | 'running' | 'ok' | 'fail'>('idle')
  const [showScriptModal, setShowScriptModal] = useState(false)
  const [errorPopup, setErrorPopup] = useState<string | null>(null)
  const pollAbortRef = useRef<AbortController | null>(null)

  useEffect(() => () => { pollAbortRef.current?.abort() }, [])

  const pollTask = async (taskId: string): Promise<{ ok: boolean; message: string }> => {
    const ac = new AbortController()
    pollAbortRef.current?.abort()
    pollAbortRef.current = ac
    const deadline = Date.now() + 30_000
    while (Date.now() < deadline) {
      if (ac.signal.aborted) return { ok: false, message: 'cancelled' }
      await new Promise<void>((r) => setTimeout(r, 1500))
      if (ac.signal.aborted) return { ok: false, message: 'cancelled' }
      try {
        const t = await clustersApi.getTask(taskId)
        if (t.Status === 'success') return { ok: true, message: '' }
        if (t.Status === 'failure' || t.Status === 'timeout') {
          const p = t.ResponsePayload as any
          const msg = p?.output?.trim() || p?.error || 'Command failed'
          return { ok: false, message: typeof msg === 'string' ? msg.trim() : JSON.stringify(msg) }
        }
      } catch { /* keep polling */ }
    }
    return { ok: false, message: 'timed out waiting for result' }
  }

  const handleTestPath = async () => {
    const effectivePath = posixPath || '/var/lib/postgresql/backups'
    setTestStatus('running')
    try {
      const { task_id } = await clustersApi.testBackupPath(clusterId, effectivePath)
      const result = await pollTask(task_id)
      setTestStatus(result.ok ? 'ok' : 'fail')
      if (!result.ok) setErrorPopup(result.message)
    } catch (e: any) {
      setTestStatus('fail')
      const rawMsg: string = e?.response?.data?.message ?? e?.message ?? 'Request failed'
      setErrorPopup(e?.response?.status === 409 ? 'Node unreachable — the agent may be temporarily disconnected.' : rawMsg)
    }
  }

  const save = useMutation({
    mutationFn: () => {
      const body: CreateBackupTargetBody = {
        label,
        target_type: type,
        recovery_days: recoveryDays,
        posix_path: type === 'posix' ? (posixPath || '/var/lib/postgresql/backups') : undefined,
        sync_to_nodes: type === 'posix' && syncEnabled ? syncToNodes : [],
        s3_bucket: type === 's3' ? s3Bucket : undefined,
        s3_region: type === 's3' ? s3Region : undefined,
        s3_endpoint: type === 's3' && s3Endpoint ? s3Endpoint : undefined,
        s3_key_id: type === 's3' && s3KeyId ? s3KeyId : undefined,
        s3_secret: type === 's3' && s3Secret ? s3Secret : undefined,
        gcs_bucket: type === 'gcs' ? gcsBucket : undefined,
        gcs_key: type === 'gcs' && gcsKey ? gcsKey : undefined,
        azure_account: type === 'azure' ? azureAccount : undefined,
        azure_container: type === 'azure' ? azureContainer : undefined,
        azure_key: type === 'azure' && azureKey ? azureKey : undefined,
        sftp_host: type === 'sftp' ? sftpHost : undefined,
        sftp_port: type === 'sftp' ? sftpPort : undefined,
        sftp_user: type === 'sftp' ? sftpUser : undefined,
        sftp_private_key: type === 'sftp' && sftpKey ? sftpKey : undefined,
        sftp_path: type === 'sftp' ? sftpPath : undefined,
      }
      if (existing) {
        return clustersApi.updateBackupTarget(clusterId, existing.id, body)
      }
      return clustersApi.createBackupTarget(clusterId, body)
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['backup-config', clusterId] })
      onClose()
    },
    onError: (e: any) => setError(e?.response?.data?.message ?? 'Save failed'),
  })

  const field = (label: string, el: React.ReactNode) => (
    <div key={label}>
      <label className="block text-xs text-gray-400 mb-1">{label}</label>
      {el}
    </div>
  )
  const inp = (val: string, set: (v: string) => void, placeholder?: string, type?: string, autoFocus?: boolean) => (
    <input
      autoFocus={autoFocus}
      className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-white focus:outline-none focus:border-blue-500"
      value={val}
      onChange={(e) => set(e.target.value)}
      placeholder={placeholder}
      type={type}
    />
  )

  return (
    <>
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4">
      <div className="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-lg max-h-[90vh] overflow-y-auto">
        <div className="flex items-center justify-between px-6 py-4 border-b border-gray-800">
          <h2 className="font-semibold">{existing ? 'Edit Storage Location' : 'Add Storage Location'}</h2>
          <button onClick={onClose} className="text-gray-500 hover:text-white">✕</button>
        </div>
        <div className="px-6 py-4 space-y-4">
          {field('Display name', inp(label, setLabel, 'e.g. Primary backup storage', undefined, true))}

          {field('Storage type',
            <select
              className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-white focus:outline-none focus:border-blue-500"
              value={type}
              onChange={(e) => setType(e.target.value as BackupTargetType)}
              disabled={!!existing}
            >
              {(Object.keys(TARGET_TYPE_LABELS) as BackupTargetType[]).map((t) => (
                <option key={t} value={t}>{TARGET_TYPE_LABELS[t]}</option>
              ))}
            </select>
          )}
          <p className="text-xs text-gray-500 -mt-2">{STORAGE_TYPE_HINTS[type]}</p>

          {type === 'posix' && (
            <div className="space-y-2">
              {field('Path', inp(posixPath, setPosixPath, '/var/lib/postgresql/backups'))}
              <p className="text-xs text-gray-500 -mt-1">The <span className="font-mono text-gray-400">postgres</span> user and group must have write access to this path (<span className="font-mono text-gray-400">chown postgres:postgres</span>, <span className="font-mono text-gray-400">chmod 770</span>).</p>
              <div className="space-y-1.5">
                <div className="flex items-center gap-2 flex-wrap">
                  <button
                    type="button"
                    onClick={handleTestPath}
                    disabled={testStatus === 'running'}
                    className="text-xs px-2.5 py-1 border border-gray-700 hover:border-gray-500 rounded text-gray-400 hover:text-white disabled:opacity-50"
                  >
                    {testStatus === 'running' ? 'Testing…' : 'Test path'}
                  </button>
                  <button
                    type="button"
                    onClick={() => setShowScriptModal(true)}
                    className="text-xs px-2.5 py-1 border border-gray-700 hover:border-gray-500 rounded text-gray-400 hover:text-white"
                  >
                    Setup script
                  </button>
                </div>
                {testStatus === 'ok' && <p className="text-xs text-emerald-400">✓ Path is writable</p>}
                {testStatus === 'fail' && <p className="text-xs text-red-400">✗ Path test failed — run the setup script on each node</p>}
              </div>
              {dbNodes.length > 1 && (
                <div className="border-t border-gray-800 pt-3 space-y-2">
                  <label className="flex items-center gap-2 cursor-pointer">
                    <input
                      type="checkbox"
                      checked={syncEnabled}
                      onChange={(e) => setSyncEnabled(e.target.checked)}
                      className="w-3.5 h-3.5 accent-blue-500"
                    />
                    <span className="text-xs text-gray-300">Sync backups to other nodes after each backup</span>
                  </label>
                  {syncEnabled && (
                    <div className="pl-5 space-y-1">
                      {dbNodes.map((n) => (
                        <label key={n.id} className="flex items-center gap-2 cursor-pointer">
                          <input
                            type="checkbox"
                            checked={syncToNodes.includes(n.id)}
                            onChange={(e) =>
                              setSyncToNodes((prev) =>
                                e.target.checked ? [...prev, n.id] : prev.filter((id) => id !== n.id),
                              )
                            }
                            className="w-3.5 h-3.5 accent-blue-500"
                          />
                          <span className="text-xs text-gray-400">{n.hostname}</span>
                        </label>
                      ))}
                    </div>
                  )}
                </div>
              )}
            </div>
          )}

          {type === 's3' && <>
            {field('Bucket', inp(s3Bucket, setS3Bucket, 'my-backup-bucket'))}
            {field('Region', inp(s3Region, setS3Region, 'us-east-1'))}
            {field('Custom endpoint (leave blank for AWS)', inp(s3Endpoint, setS3Endpoint, 'https://s3.example.com'))}
            {field(`Access key ID${existing?.s3_key_id_set ? ' (leave blank to keep existing)' : ''}`, inp(s3KeyId, setS3KeyId, 'AKIAIOSFODNN7EXAMPLE'))}
            {field(`Secret key${existing?.s3_secret_set ? ' (leave blank to keep existing)' : ''}`, inp(s3Secret, setS3Secret, '', 'password'))}
          </>}

          {type === 'gcs' && <>
            {field('Bucket', inp(gcsBucket, setGcsBucket, 'my-backup-bucket'))}
            {field(`Service account JSON key${existing?.gcs_key_set ? ' (leave blank to keep existing)' : ''}`,
              <textarea
                className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-white focus:outline-none focus:border-blue-500 font-mono h-24 resize-none"
                value={gcsKey}
                onChange={(e) => setGcsKey(e.target.value)}
                placeholder='{"type":"service_account",...}'
              />
            )}
          </>}

          {type === 'azure' && <>
            {field('Storage account', inp(azureAccount, setAzureAccount, 'mystorageaccount'))}
            {field('Container', inp(azureContainer, setAzureContainer, 'backups'))}
            {field(`Access key${existing?.azure_key_set ? ' (leave blank to keep existing)' : ''}`, inp(azureKey, setAzureKey, '', 'password'))}
          </>}

          {type === 'sftp' && <>
            {field('Host', inp(sftpHost, setSftpHost, 'backup.example.com'))}
            {field('Port', <input
              className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-white focus:outline-none focus:border-blue-500"
              type="number"
              value={sftpPort}
              onChange={(e) => setSftpPort(Number(e.target.value))}
            />)}
            {field('Username', inp(sftpUser, setSftpUser, 'backup'))}
            {field('Remote path', inp(sftpPath, setSftpPath, '/backups/netbox'))}
            {field(`Private key${existing?.sftp_private_key_set ? ' (leave blank to keep existing)' : ''}`,
              <textarea
                className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-white focus:outline-none focus:border-blue-500 font-mono h-24 resize-none"
                value={sftpKey}
                onChange={(e) => setSftpKey(e.target.value)}
                placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
              />
            )}
          </>}

          <div className="border-t border-gray-800 pt-4">
            <p className="text-xs font-medium text-gray-400 mb-3">Recovery window</p>
            <div>
              <label className="text-xs text-gray-500 block mb-1">How far back do you want to be able to restore? (days)</label>
              <div className="flex gap-2 items-center">
                <input type="number" min={7} className="w-28 bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-white focus:outline-none focus:border-blue-500"
                  value={recoveryDays} onChange={(e) => setRecoveryDays(Number(e.target.value))} />
                <div className="flex gap-1">
                  {[7, 14, 30, 60, 90].map((d) => (
                    <button key={d} type="button"
                      onClick={() => setRecoveryDays(d)}
                      className={`text-xs px-2 py-1 rounded border ${recoveryDays === d ? 'bg-blue-600 border-blue-500 text-white' : 'bg-gray-800 border-gray-700 text-gray-400 hover:text-white'}`}
                    >{d}d</button>
                  ))}
                </div>
              </div>
            </div>
          </div>

          {error && <p className="text-sm text-red-400">{error}</p>}
        </div>
        <div className="flex justify-end gap-3 px-6 py-4 border-t border-gray-800">
          <button onClick={onClose} className="px-4 py-2 text-sm text-gray-400 hover:text-white">Cancel</button>
          <button
            onClick={() => save.mutate()}
            disabled={save.isPending || !label}
            className="px-4 py-2 text-sm bg-blue-600 hover:bg-blue-500 disabled:opacity-50 rounded-lg"
          >
            {save.isPending ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>
    </div>
    {showScriptModal && (
      <ProvisionScriptModal path={posixPath} onClose={() => setShowScriptModal(false)} />
    )}
    {errorPopup && (
      <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-[70] p-4">
        <div className="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-sm p-6 space-y-4">
          <p className="text-sm text-red-400 whitespace-pre-wrap">{errorPopup}</p>
          <div className="flex justify-end">
            <button
              onClick={() => setErrorPopup(null)}
              className="px-4 py-2 text-sm bg-gray-700 hover:bg-gray-600 rounded-lg"
            >
              OK
            </button>
          </div>
        </div>
      </div>
    )}
    </>
  )
}

function ProvisionScriptModal({ path, onClose }: { path: string; onClose: () => void }) {
  const script = `BACKUP_DIR="${path}"

sudo mkdir -p "$BACKUP_DIR"
sudo chown postgres:postgres "$BACKUP_DIR"
sudo chmod 770 "$BACKUP_DIR"
echo "Backup directory ready: $BACKUP_DIR"`

  const [copied, setCopied] = useState(false)

  return (
    <div className="fixed inset-0 bg-black/70 flex items-center justify-center z-[60] p-4">
      <div className="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-lg">
        <div className="flex items-center justify-between px-6 py-4 border-b border-gray-800">
          <h2 className="font-semibold text-sm">Manual Setup Script</h2>
          <button onClick={onClose} className="text-gray-500 hover:text-white">✕</button>
        </div>
        <div className="px-6 py-4 space-y-3">
          <p className="text-sm text-gray-400">
            SSH into each node and run these commands:
          </p>
          <div className="relative">
            <pre className="bg-gray-950 border border-gray-700 rounded-lg p-4 text-xs text-gray-200 font-mono overflow-x-auto whitespace-pre">{script}</pre>
            <button
              onClick={() => {
                navigator.clipboard.writeText(script)
                setCopied(true)
                setTimeout(() => setCopied(false), 2000)
              }}
              className="absolute top-2 right-2 px-2 py-0.5 text-xs bg-gray-700 hover:bg-gray-600 rounded"
            >
              {copied ? 'Copied!' : 'Copy'}
            </button>
          </div>
        </div>
        <div className="flex justify-end px-6 py-4 border-t border-gray-800">
          <button onClick={onClose} className="px-4 py-2 text-sm text-gray-400 hover:text-white">Close</button>
        </div>
      </div>
    </div>
  )
}

function RestoreModal({
  clusterId,
  oldest,
  newest,
  onClose,
}: {
  clusterId: string
  oldest: string
  newest: string
  onClose: () => void
}) {
  const qc = useQueryClient()
  const oldestMs = new Date(oldest).getTime()
  const newestMs = new Date(newest).getTime()
  const rangeMs = newestMs - oldestMs

  const [sliderVal, setSliderVal] = useState(newestMs)
  const [confirmed, setConfirmed] = useState(false)
  const [result, setResult] = useState<string | null>(null)

  const targetDate = new Date(sliderVal)
  const targetRFC3339 = targetDate.toISOString()

  const restore = useMutation({
    mutationFn: () => clustersApi.clusterRestore(clusterId, { target_time: targetRFC3339 }),
    onSuccess: () => {
      setResult('Restore initiated. Track progress in the Database tab → DB Events.')
      qc.invalidateQueries({ queryKey: ['backup-config', clusterId] })
    },
    onError: (e: any) => setResult('Failed: ' + (e?.response?.data?.message ?? e.message)),
  })

  return (
    <div className="fixed inset-0 bg-black/70 flex items-center justify-center z-50 p-4">
      <div className="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-lg">
        <div className="flex items-center justify-between px-6 py-4 border-b border-gray-800">
          <h2 className="font-semibold">Restore Database</h2>
          <button onClick={onClose} className="text-gray-500 hover:text-white">✕</button>
        </div>
        <div className="px-6 py-5 space-y-5">
          {result ? (
            <p className="text-sm text-gray-300">{result}</p>
          ) : (
            <>
              <div className="bg-amber-900/30 border border-amber-800/50 rounded-lg p-3 text-sm text-amber-300">
                This will take your database offline briefly and replace it with a version from the date
                and time you select. Any data created after that point will be permanently lost.
              </div>

              <div>
                <div className="flex justify-between text-xs text-gray-500 mb-1">
                  <span>{new Date(oldest).toLocaleString()}</span>
                  <span>{new Date(newest).toLocaleString()}</span>
                </div>
                <input
                  type="range"
                  className="w-full accent-blue-500"
                  min={oldestMs}
                  max={newestMs}
                  step={Math.max(60_000, Math.floor(rangeMs / 1000))}
                  value={sliderVal}
                  onChange={(e) => setSliderVal(Number(e.target.value))}
                />
                <p className="text-center text-sm text-white mt-1">
                  {targetDate.toLocaleString()}
                  <span className="text-gray-500 text-xs ml-2">({targetRFC3339})</span>
                </p>
              </div>

              <label className="flex items-start gap-2 text-sm text-gray-300 cursor-pointer">
                <input
                  type="checkbox"
                  className="mt-0.5"
                  checked={confirmed}
                  onChange={(e) => setConfirmed(e.target.checked)}
                />
                I understand this will permanently replace the current database.
              </label>
            </>
          )}
        </div>
        <div className="flex justify-end gap-3 px-6 py-4 border-t border-gray-800">
          <button onClick={onClose} className="px-4 py-2 text-sm text-gray-400 hover:text-white">
            {result ? 'Close' : 'Cancel'}
          </button>
          {!result && (
            <button
              onClick={() => restore.mutate()}
              disabled={!confirmed || restore.isPending}
              className="px-4 py-2 text-sm bg-red-700 hover:bg-red-600 disabled:opacity-50 rounded-lg"
            >
              {restore.isPending ? 'Restoring…' : 'Restore Database'}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

function BackupsTab({ clusterId }: { clusterId: string }) {
  const qc = useQueryClient()
  const [showAddStorage, setShowAddStorage] = useState(false)
  const [editingTarget, setEditingTarget] = useState<BackupTarget | null>(null)
  const [showRestoreModal, setShowRestoreModal] = useState(false)

  const { data: clusterNodes } = useQuery({
    queryKey: ['nodes', clusterId],
    queryFn: () => nodesApi.list(clusterId),
  })
  const dbNodes = (clusterNodes ?? []).filter(
    (n) => n.role === 'hyperconverged' || n.role === 'db_only',
  )

  const { data: config, isLoading } = useQuery({
    queryKey: ['backup-config', clusterId],
    queryFn: () => clustersApi.getBackupConfig(clusterId),
    // Poll every 5 s while Configure Backups is in progress (background task),
    // then relax to 30 s once the stanza is ready.
    refetchInterval: (q) =>
      q.state.data?.schedule?.stanza_initialized ? 30_000 : 5_000,
  })

  const deleteTarget = useMutation({
    mutationFn: (tid: string) => clustersApi.deleteBackupTarget(clusterId, tid),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['backup-config', clusterId] }),
  })

  const [enableError, setEnableError] = useState<string | null>(null)
  const [configuringInProgress, setConfiguringInProgress] = useState(false)
  const enableBackups = useMutation({
    mutationFn: () => clustersApi.enableBackups(clusterId),
    onSuccess: () => {
      setEnableError(null)
      // Keep configuringInProgress true — the 5 s polling will flip it off
      // when stanza_initialized becomes true (background task on server).
      qc.invalidateQueries({ queryKey: ['backup-config', clusterId] })
    },
    onError: (e: any) => {
      setConfiguringInProgress(false)
      setEnableError(e?.response?.data?.message ?? e?.message ?? 'Request failed')
    },
  })

  const pushConfig = useMutation({
    mutationFn: () => clustersApi.pushBackupConfig(clusterId),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['backup-config', clusterId] }),
  })


  const [scheduleEnabled, setScheduleEnabled] = useState(config?.schedule?.enabled ?? false)
  const [fullCron, setFullCron] = useState(config?.schedule?.full_backup_cron ?? '0 1 * * 0')
  const [diffCron, setDiffCron] = useState(config?.schedule?.diff_backup_cron ?? '0 1 * * 1-6')
  const [incrHrs, setIncrHrs] = useState(config?.schedule?.incr_backup_interval_hrs ?? 1)

  const saveSchedule = useMutation({
    mutationFn: () =>
      clustersApi.putBackupSchedule(clusterId, {
        enabled: scheduleEnabled,
        full_backup_cron: fullCron,
        diff_backup_cron: diffCron,
        incr_backup_interval_hrs: incrHrs,
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['backup-config', clusterId] }),
  })

  const runBackup = useMutation({
    mutationFn: () => clustersApi.runBackup(clusterId, 'full'),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['backup-config', clusterId] }),
  })

  const refreshCatalog = useMutation({
    mutationFn: () => clustersApi.getBackupCatalog(clusterId),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['backup-config', clusterId] }),
  })

  useEffect(() => {
    if (config?.schedule) {
      setScheduleEnabled(config.schedule.enabled)
      setFullCron(config.schedule.full_backup_cron)
      setDiffCron(config.schedule.diff_backup_cron)
      setIncrHrs(config.schedule.incr_backup_interval_hrs)
    }
  }, [config?.schedule?.cluster_id])

  const schedule = config?.schedule
  const targets = config?.targets ?? []
  const stanzaReady = schedule?.stanza_initialized ?? false

  // Clear in-progress flag once the server background task finishes.
  useEffect(() => {
    if (stanzaReady) setConfiguringInProgress(false)
  }, [stanzaReady])

  if (isLoading) return <p className="text-gray-500 text-sm">Loading…</p>

  const catalogBackups = config?.cached_catalog?.backups ?? []
  const oldest = config?.cached_catalog?.oldest_restore_point
  const newest = config?.cached_catalog?.newest_restore_point

  return (
    <div className="space-y-6">
      {/* Storage locations card */}
      <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
        <div className="flex items-center justify-between px-5 py-4 border-b border-gray-800">
          <div>
            <h3 className="font-medium text-sm">Storage Locations</h3>
            <p className="text-xs text-gray-500 mt-0.5">Where backups are stored. Up to 4 locations.</p>
          </div>
          <div className="flex gap-2">
            {targets.length < 4 && (
              <button
                onClick={() => setShowAddStorage(true)}
                className="text-xs px-3 py-1.5 bg-blue-600 hover:bg-blue-500 rounded-lg"
              >
                Add location
              </button>
            )}
          </div>
        </div>

        {targets.length === 0 ? (
          <div className="px-5 py-8 text-center text-gray-500 text-sm">
            No storage locations configured. Add one to enable backups.
          </div>
        ) : (
          <div className="divide-y divide-gray-800">
            {targets.map((t) => (
              <div key={t.id} className="flex items-center justify-between px-5 py-3">
                <div>
                  <span className="text-sm text-white">{t.label}</span>
                  <span className="ml-2 text-xs px-2 py-0.5 bg-gray-800 border border-gray-700 rounded text-gray-400">
                    {TARGET_TYPE_LABELS[t.target_type]}
                  </span>
                  <p className="text-xs text-gray-500 mt-0.5">
                    {t.recovery_days}-day recovery window
                  </p>
                </div>
                <div className="flex gap-2">
                  <button
                    onClick={() => setEditingTarget(t)}
                    className="text-xs text-gray-500 hover:text-white"
                  >
                    Edit
                  </button>
                  <button
                    onClick={() => { if (confirm(`Remove "${t.label}"?`)) deleteTarget.mutate(t.id) }}
                    className="text-xs text-red-500 hover:text-red-400"
                  >
                    Remove
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}

        {targets.length > 0 &&
          targets.every((t) => t.target_type === 'posix') &&
          targets.every((t) => (t.sync_to_nodes?.length ?? 0) === 0) && (
          <div className="px-5 py-2 border-t border-gray-800 bg-amber-900/20">
            <p className="text-xs text-amber-400">
              Local disk backups can only restore this specific server. Enable sync or add a cloud storage location to restore from any node.
            </p>
          </div>
        )}

        {targets.length > 0 && !stanzaReady && (
          <div className="px-5 py-3 border-t border-gray-800">
            <div className="flex items-center justify-between">
              <p className="text-xs text-amber-400">
                {configuringInProgress
                  ? 'Configuring — pushing pgBackRest and Patroni config, then initializing backup storage. This may take a minute.'
                  : 'Backups not yet configured on nodes.'}
              </p>
              <button
                onClick={() => { setEnableError(null); setConfiguringInProgress(true); enableBackups.mutate() }}
                disabled={configuringInProgress}
                className="ml-4 flex-shrink-0 text-xs px-3 py-1.5 bg-emerald-700 hover:bg-emerald-600 disabled:opacity-50 rounded-lg"
              >
                {configuringInProgress ? 'Configuring…' : 'Configure Backups'}
              </button>
            </div>
            {enableError && (
              <p className="mt-2 text-xs text-red-400">{enableError}</p>
            )}
          </div>
        )}

        {stanzaReady && (
          <div className="px-5 py-3 border-t border-gray-800">
            <div className="flex items-center justify-between">
              <span className="text-xs text-emerald-400">Backups active</span>
              <button
                onClick={() => pushConfig.mutate()}
                disabled={pushConfig.isPending}
                className="ml-4 flex-shrink-0 text-xs px-3 py-1.5 bg-gray-700 hover:bg-gray-600 disabled:opacity-50 rounded-lg"
              >
                {pushConfig.isPending ? 'Applying…' : 'Apply Config to Nodes'}
              </button>
            </div>
          </div>
        )}
      </div>

      {/* Schedule card */}
      <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
        <div className="px-5 py-4 border-b border-gray-800 flex items-center justify-between">
          <div className="flex items-center gap-2">
            <h3 className="font-medium text-sm">Automatic Backups</h3>
            <a
              href="https://crontab.guru"
              target="_blank"
              rel="noopener noreferrer"
              className="text-xs text-gray-500 hover:text-gray-300"
            >
              cron help ↗
            </a>
          </div>
          <label className="flex items-center gap-2 cursor-pointer">
            <span className="text-xs text-gray-400">Enabled</span>
            <input
              type="checkbox"
              checked={scheduleEnabled}
              onChange={(e) => setScheduleEnabled(e.target.checked)}
              className="accent-blue-500"
            />
          </label>
        </div>
        <div className="px-5 py-4 space-y-4">
          <div className="grid grid-cols-3 gap-4">
            <div>
              <label className="text-xs text-gray-400 block mb-1">Full backup schedule</label>
              <input
                className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm font-mono text-white focus:outline-none focus:border-blue-500"
                value={fullCron}
                onChange={(e) => setFullCron(e.target.value)}
                placeholder="0 1 * * 0"
              />
              <p className="text-xs text-gray-600 mt-0.5">cron — default: weekly Sunday 1am</p>
            </div>
            <div>
              <label className="text-xs text-gray-400 block mb-1">Daily snapshot schedule</label>
              <input
                className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm font-mono text-white focus:outline-none focus:border-blue-500"
                value={diffCron}
                onChange={(e) => setDiffCron(e.target.value)}
                placeholder="0 1 * * 1-6"
              />
              <p className="text-xs text-gray-600 mt-0.5">cron — default: Mon–Sat 1am</p>
            </div>
            <div>
              <label className="text-xs text-gray-400 block mb-1">Log snapshots — every N hours</label>
              <input
                type="number"
                min={1}
                max={24}
                className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-white focus:outline-none focus:border-blue-500"
                value={incrHrs}
                onChange={(e) => setIncrHrs(Number(e.target.value))}
              />
              <p className="text-xs text-gray-600 mt-0.5">captures incremental changes</p>
            </div>
          </div>
          <div className="flex justify-end">
            <button
              onClick={() => saveSchedule.mutate()}
              disabled={saveSchedule.isPending}
              className="text-sm px-4 py-1.5 bg-blue-600 hover:bg-blue-500 disabled:opacity-50 rounded-lg"
            >
              {saveSchedule.isPending ? 'Saving…' : 'Save schedule'}
            </button>
          </div>
        </div>
      </div>

      {/* Backup history / catalog card */}
      <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
        <div className="px-5 py-4 border-b border-gray-800 flex items-center justify-between">
          <h3 className="font-medium text-sm">Backup History</h3>
          <div className="flex gap-2">
            <button
              onClick={() => refreshCatalog.mutate()}
              disabled={!stanzaReady || refreshCatalog.isPending}
              className="text-xs text-gray-500 hover:text-white disabled:opacity-40"
            >
              {refreshCatalog.isPending ? 'Refreshing…' : 'Refresh'}
            </button>
            <button
              onClick={() => runBackup.mutate()}
              disabled={!stanzaReady || runBackup.isPending}
              className="text-xs px-3 py-1.5 border border-gray-700 hover:border-gray-500 rounded-lg text-gray-400 hover:text-white disabled:opacity-40"
            >
              {runBackup.isPending ? 'Starting…' : 'Run full backup now'}
            </button>
            <button
              onClick={() => setShowRestoreModal(true)}
              disabled={!oldest || !newest}
              className="text-xs px-3 py-1.5 bg-red-900/60 hover:bg-red-900 border border-red-800 rounded-lg text-red-300 disabled:opacity-40"
            >
              Restore to a previous point
            </button>
          </div>
        </div>

        {!stanzaReady ? (
          <div className="px-5 py-6 text-center text-gray-500 text-sm">
            No backup history found. Activate backups and run your first full backup to enable point-in-time restore.
          </div>
        ) : (
          <div className="px-5 py-4 space-y-3">
            {oldest && newest ? (
              <div className="flex gap-8 text-sm">
                <div>
                  <p className="text-xs text-gray-500">Oldest available restore point</p>
                  <p className="text-white">{new Date(oldest).toLocaleString()}</p>
                </div>
                <div>
                  <p className="text-xs text-gray-500">Most recent restore point</p>
                  <p className="text-white">{new Date(newest).toLocaleString()}</p>
                </div>
              </div>
            ) : (
              <p className="text-sm text-gray-500">Click Refresh to load available restore points from nodes.</p>
            )}

            {catalogBackups.length > 0 && (
              <div className="border-t border-gray-800 pt-3">
                <p className="text-xs text-gray-500 mb-2">Completed backups</p>
                <div className="space-y-1">
                  {catalogBackups.slice(0, 8).map((b) => (
                    <div key={b.label} className="flex items-center justify-between text-xs">
                      <div className="flex items-center gap-2">
                        <span className="w-1.5 h-1.5 rounded-full flex-shrink-0 bg-emerald-400" />
                        <span className="text-gray-400">
                          {b.type === 'full' ? 'Full backup' : b.type === 'diff' ? 'Daily snapshot' : 'Log snapshot'}
                        </span>
                      </div>
                      <span className="text-gray-500">{new Date(b.started_at).toLocaleString()}</span>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </div>
        )}
      </div>

      {(showAddStorage || editingTarget) && (
        <AddStorageModal
          clusterId={clusterId}
          existing={editingTarget ?? undefined}
          dbNodes={dbNodes}
          onClose={() => { setShowAddStorage(false); setEditingTarget(null) }}
        />
      )}

      {showRestoreModal && oldest && newest && (
        <RestoreModal
          clusterId={clusterId}
          oldest={oldest}
          newest={newest}
          onClose={() => setShowRestoreModal(false)}
        />
      )}
    </div>
  )
}

// ── Tab / sub-tab type definitions ────────────────────────────────────────────

type Tab = 'overview' | 'settings' | 'history'

const SETTINGS_TABS = [
  { id: 'general', label: 'General' },
  { id: 'credentials', label: 'Credentials' },
  { id: 'failover', label: 'Failover' },
  { id: 'backup', label: 'Backup' },
]

const HISTORY_TABS = [
  { id: 'events', label: 'Events' },
  { id: 'ha-events', label: 'HA Events' },
  { id: 'api-history', label: 'API History' },
  { id: 'cluster-logs', label: 'Conductor Logs' },
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
        {([
          { id: 'overview', label: 'Overview' },
          { id: 'settings', label: 'Settings' },
          { id: 'history', label: 'History' },
        ] as { id: Tab; label: string }[]).map((t) => (
          <button
            key={t.id}
            onClick={() => goToTab(t.id)}
            className={`px-4 py-2 text-sm border-b-2 -mb-px transition-colors ${
              tab === t.id
                ? 'border-blue-500 text-white'
                : 'border-transparent text-gray-400 hover:text-white'
            }`}
          >
            {t.label}
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

          {settingsSub === 'backup' && id && (
            <BackupsTab clusterId={id} />
          )}
        </div>
      )}

      {/* ── History tab ── */}
      {tab === 'history' && id && (
        <div>
          <SubTabBar tabs={HISTORY_TABS} active={historySub} onChange={goToSub} />

          {historySub === 'events' && (
            <ClusterEventsTab clusterId={id} />
          )}

          {historySub === 'ha-events' && (
            <FailoverHistoryTab events={failoverEvents ?? []} />
          )}

          {historySub === 'api-history' && (
            <AuditTab clusterId={id} />
          )}

          {historySub === 'cluster-logs' && (
            <ClusterLogsTab />
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
          existingNodes={nodes ?? []}
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
