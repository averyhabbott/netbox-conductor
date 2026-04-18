import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { clustersApi } from '../api/clusters'
import type { CreateClusterBody } from '../api/clusters'
import Layout from '../components/Layout'

function StatusDot({ status }: { status: string }) {
  const colors: Record<string, string> = {
    connected: 'bg-emerald-500',
    disconnected: 'bg-red-500',
    unknown: 'bg-gray-500',
  }
  return (
    <span
      className={`inline-block w-2 h-2 rounded-full ${colors[status] ?? 'bg-gray-500'}`}
    />
  )
}

interface CreateClusterModalProps {
  onClose: () => void
}

function CreateClusterModal({ onClose }: CreateClusterModalProps) {
  const qc = useQueryClient()
  const [form, setForm] = useState<CreateClusterBody>({
    name: '',
    description: '',
    mode: 'active_standby',
    patroni_scope: '',
    netbox_version: '',
  })
  const [error, setError] = useState('')

  const create = useMutation({
    mutationFn: (body: CreateClusterBody) => clustersApi.create(body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['clusters'] })
      onClose()
    },
    onError: (e: any) => {
      setError(e.response?.data?.message ?? 'Failed to create cluster')
    },
  })

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50">
      <div className="bg-gray-900 border border-gray-700 rounded-xl w-full max-w-md p-6">
        <h3 className="text-lg font-semibold mb-4">New Cluster</h3>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            create.mutate({
              ...form,
              patroni_scope: form.patroni_scope || form.name,
            })
          }}
          className="space-y-4"
        >
          <div>
            <label className="block text-sm text-gray-400 mb-1">Cluster Name</label>
            <input
              className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
              placeholder="prod-cluster-a"
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              required
            />
          </div>
          <div>
            <label className="block text-sm text-gray-400 mb-1">
              Description <span className="text-gray-600">(optional)</span>
            </label>
            <input
              className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
              placeholder="e.g. Production cluster for network operations"
              value={form.description}
              onChange={(e) => setForm({ ...form, description: e.target.value })}
              maxLength={255}
            />
          </div>
          <div>
            <label className="block text-sm text-gray-400 mb-1">Mode</label>
            <select
              className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
              value={form.mode}
              onChange={(e) =>
                setForm({ ...form, mode: e.target.value as CreateClusterBody['mode'] })
              }
            >
              <option value="active_standby">Active / Standby</option>
              <option value="ha">HA (3+ nodes)</option>
            </select>
          </div>
          <div>
            <label className="block text-sm text-gray-400 mb-1">
              Patroni Scope{' '}
              <span className="text-gray-600">(defaults to cluster name)</span>
            </label>
            <input
              className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
              placeholder={form.name || 'prod-cluster-a'}
              value={form.patroni_scope}
              onChange={(e) => setForm({ ...form, patroni_scope: e.target.value })}
            />
          </div>
          <div>
            <label className="block text-sm text-gray-400 mb-1">
              NetBox Version <span className="text-gray-600">(updated automatically from agent heartbeat)</span>
            </label>
            <input
              className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
              placeholder="e.g. v4.5.7"
              value={form.netbox_version}
              onChange={(e) => setForm({ ...form, netbox_version: e.target.value })}
            />
          </div>
          {error && <p className="text-sm text-red-400">{error}</p>}
          <div className="flex gap-3 pt-2">
            <button
              type="submit"
              disabled={create.isPending}
              className="flex-1 bg-blue-600 hover:bg-blue-500 disabled:opacity-50 rounded-lg py-2 text-sm font-medium transition-colors"
            >
              {create.isPending ? 'Creating…' : 'Create Cluster'}
            </button>
            <button
              type="button"
              onClick={onClose}
              className="flex-1 bg-gray-800 hover:bg-gray-700 rounded-lg py-2 text-sm transition-colors"
            >
              Cancel
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

export default function ClusterList() {
  const navigate = useNavigate()
  const [showCreate, setShowCreate] = useState(false)
  const { data: clusters, isLoading } = useQuery({
    queryKey: ['clusters'],
    queryFn: clustersApi.list,
    refetchInterval: 30_000,
  })

  return (
    <Layout>
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-2xl font-semibold">Clusters</h2>
        <button
          onClick={() => setShowCreate(true)}
          className="bg-blue-600 hover:bg-blue-500 text-sm font-medium px-4 py-2 rounded-lg transition-colors"
        >
          + New Cluster
        </button>
      </div>

      {isLoading ? (
        <div className="text-gray-500 text-sm">Loading…</div>
      ) : clusters && clusters.length > 0 ? (
        <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-gray-800 text-gray-400">
                <th className="text-left px-6 py-3 font-medium">Name</th>
                <th className="text-left px-6 py-3 font-medium">Description</th>
                <th className="text-left px-6 py-3 font-medium">Mode</th>
                <th className="text-left px-6 py-3 font-medium">Patroni Scope</th>
                <th className="text-left px-6 py-3 font-medium">Auto Failover</th>
                <th className="text-left px-6 py-3 font-medium">NetBox</th>
              </tr>
            </thead>
            <tbody>
              {clusters.map((cluster) => (
                <tr
                  key={cluster.id}
                  onClick={() => navigate(`/clusters/${cluster.id}`)}
                  className="border-b border-gray-800 last:border-0 hover:bg-gray-800/40 cursor-pointer"
                >
                  <td className="px-6 py-4 font-medium">{cluster.name}</td>
                  <td className="px-6 py-4 text-gray-400 text-sm">
                    {cluster.description || <span className="text-gray-600">—</span>}
                  </td>
                  <td className="px-6 py-4 text-gray-300">
                    {cluster.mode === 'active_standby' ? 'Active / Standby' : 'HA'}
                  </td>
                  <td className="px-6 py-4 text-gray-400 font-mono text-xs">
                    {cluster.patroni_scope}
                  </td>
                  <td className="px-6 py-4">
                    <StatusDot status={cluster.auto_failover ? 'connected' : 'unknown'} />
                    <span className="ml-2 text-gray-300">
                      {cluster.auto_failover ? 'On' : 'Off'}
                    </span>
                  </td>
                  <td className="px-6 py-4 text-gray-400">{cluster.netbox_version}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="bg-gray-900 border border-gray-800 rounded-xl p-12 text-center">
          <p className="text-gray-400 mb-4">No clusters configured yet.</p>
          <button
            onClick={() => setShowCreate(true)}
            className="bg-blue-600 hover:bg-blue-500 text-sm font-medium px-4 py-2 rounded-lg transition-colors"
          >
            Create your first cluster
          </button>
        </div>
      )}

      {showCreate && <CreateClusterModal onClose={() => setShowCreate(false)} />}
    </Layout>
  )
}
