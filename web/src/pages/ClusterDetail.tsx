import { useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { clustersApi } from '../api/clusters'
import { nodesApi } from '../api/nodes'
import type { Node } from '../api/nodes'
import { credentialsApi, credentialLabels } from '../api/credentials'
import type { Credential, CredentialKind } from '../api/credentials'
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
        <div className="font-medium">{node.hostname}</div>
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

type Tab = 'nodes' | 'configuration' | 'settings'

export default function ClusterDetail() {
  const { id } = useParams<{ id: string }>()
  const [tab, setTab] = useState<Tab>('nodes')
  const [showWizard, setShowWizard] = useState(false)

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
        {(['nodes', 'configuration', 'settings'] as Tab[]).map((t) => (
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
            <button
              onClick={() => setShowWizard(true)}
              className="bg-blue-600 hover:bg-blue-500 text-sm font-medium px-4 py-2 rounded-lg transition-colors"
            >
              + Add Node
            </button>
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
            <h3 className="font-medium mb-4">Credentials</h3>
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
    </Layout>
  )
}
