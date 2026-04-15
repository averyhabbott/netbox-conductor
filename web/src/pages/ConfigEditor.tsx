import { useState, useEffect } from 'react'
import { useParams, Link } from 'react-router-dom'
import { useQuery, useMutation } from '@tanstack/react-query'
import { clustersApi } from '../api/clusters'
import { nodesApi } from '../api/nodes'
import { configsApi } from '../api/configs'
import type { PushNodeResult } from '../api/configs'
import Layout from '../components/Layout'

function PushStatusBadge({ status }: { status?: string }) {
  if (!status) return null
  const styles: Record<string, string> = {
    success: 'bg-emerald-900/50 text-emerald-400 border-emerald-800',
    partial: 'bg-yellow-900/50 text-yellow-400 border-yellow-800',
    failed: 'bg-red-900/50 text-red-400 border-red-800',
    in_progress: 'bg-blue-900/50 text-blue-400 border-blue-800',
    pending: 'bg-gray-800 text-gray-400 border-gray-700',
  }
  return (
    <span
      className={`text-xs px-2 py-0.5 rounded border ${styles[status] ?? styles.pending}`}
    >
      {status}
    </span>
  )
}

export default function ConfigEditor() {
  const { id } = useParams<{ id: string }>()
  const [template, setTemplate] = useState('')
  const [selectedNode, setSelectedNode] = useState<string>('')
  const [previewContent, setPreviewContent] = useState<string | null>(null)
  const [pushResults, setPushResults] = useState<PushNodeResult[] | null>(null)
  const [restartAfter, setRestartAfter] = useState(false)
  const [dirty, setDirty] = useState(false)

  const { data: cluster } = useQuery({
    queryKey: ['cluster', id],
    queryFn: () => clustersApi.get(id!),
    enabled: !!id,
  })

  const { data: nodes } = useQuery({
    queryKey: ['nodes', id],
    queryFn: () => nodesApi.list(id!),
    enabled: !!id,
  })

  const { data: configData, refetch: refetchConfig } = useQuery({
    queryKey: ['config', id],
    queryFn: () => configsApi.getOrCreate(id!),
    enabled: !!id,
  })

  // Seed textarea when config loads (only once)
  useEffect(() => {
    if (configData && !dirty) {
      setTemplate(configData.config.config_template)
    }
  }, [configData, dirty])

  const saveMut = useMutation({
    mutationFn: () => configsApi.save(id!, template),
    onSuccess: () => {
      setDirty(false)
      refetchConfig()
    },
  })

  const previewMut = useMutation({
    mutationFn: () =>
      configsApi.preview(id!, selectedNode || undefined, dirty ? template : undefined),
    onSuccess: (data) => setPreviewContent(data.content),
  })

  const pushMut = useMutation({
    mutationFn: async () => {
      // Save first if dirty
      if (dirty) {
        await configsApi.save(id!, template)
        setDirty(false)
      }
      const latest = await configsApi.getOrCreate(id!)
      return configsApi.push(id!, latest.config.version, restartAfter)
    },
    onSuccess: (data) => {
      setPushResults(data.nodes)
      refetchConfig()
    },
  })

  const config = configData?.config

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
        <span className="text-white">Configuration</span>
      </div>

      <div className="flex items-start justify-between mb-6">
        <div>
          <h2 className="text-2xl font-semibold">Configuration</h2>
          <div className="flex items-center gap-3 mt-1 text-sm text-gray-400">
            {config && (
              <>
                <span>v{config.version}</span>
                {config.pushed_at && (
                  <>
                    <span>·</span>
                    <span>Pushed {new Date(config.pushed_at).toLocaleString()}</span>
                  </>
                )}
                <span>·</span>
                <PushStatusBadge status={config.push_status} />
              </>
            )}
          </div>
        </div>

        <div className="flex items-center gap-3">
          <label className="flex items-center gap-2 text-sm text-gray-400 cursor-pointer">
            <input
              type="checkbox"
              checked={restartAfter}
              onChange={(e) => setRestartAfter(e.target.checked)}
              className="rounded"
            />
            Restart NetBox after push
          </label>
          <button
            onClick={() => previewMut.mutate()}
            disabled={previewMut.isPending}
            className="bg-gray-800 hover:bg-gray-700 text-sm px-4 py-2 rounded-lg transition-colors"
          >
            {previewMut.isPending ? 'Rendering…' : 'Preview'}
          </button>
          <button
            onClick={() => saveMut.mutate()}
            disabled={saveMut.isPending || !dirty}
            className="bg-gray-700 hover:bg-gray-600 disabled:opacity-40 text-sm px-4 py-2 rounded-lg transition-colors"
          >
            {saveMut.isPending ? 'Saving…' : 'Save'}
          </button>
          <button
            onClick={() => pushMut.mutate()}
            disabled={pushMut.isPending}
            className="bg-blue-600 hover:bg-blue-500 disabled:opacity-50 text-sm font-medium px-4 py-2 rounded-lg transition-colors"
          >
            {pushMut.isPending ? 'Pushing…' : 'Push to All Nodes'}
          </button>
        </div>
      </div>

      <div className="grid grid-cols-1 xl:grid-cols-2 gap-6">
        {/* Template editor */}
        <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden flex flex-col">
          <div className="flex items-center justify-between px-4 py-3 border-b border-gray-800">
            <h3 className="text-sm font-medium">Template</h3>
            {dirty && <span className="text-xs text-yellow-400">unsaved changes</span>}
          </div>
          <textarea
            className="flex-1 bg-transparent font-mono text-xs text-gray-300 p-4 resize-none focus:outline-none min-h-[520px]"
            value={template}
            onChange={(e) => {
              setTemplate(e.target.value)
              setDirty(true)
              setPreviewContent(null)
            }}
            spellCheck={false}
          />
        </div>

        {/* Preview panel */}
        <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden flex flex-col">
          <div className="flex items-center justify-between px-4 py-3 border-b border-gray-800">
            <h3 className="text-sm font-medium">Preview</h3>
            <select
              className="bg-gray-800 border border-gray-700 rounded px-2 py-1 text-xs text-gray-300 focus:outline-none"
              value={selectedNode}
              onChange={(e) => {
                setSelectedNode(e.target.value)
                setPreviewContent(null)
              }}
            >
              <option value="">First available node</option>
              {nodes?.map((n) => (
                <option key={n.id} value={n.id}>
                  {n.hostname}
                </option>
              ))}
            </select>
          </div>
          {previewContent ? (
            <pre className="flex-1 overflow-auto p-4 text-xs font-mono text-gray-300 min-h-[520px]">
              {previewContent}
            </pre>
          ) : (
            <div className="flex-1 flex items-center justify-center text-gray-600 text-sm min-h-[520px]">
              Click Preview to render the template with live cluster credentials
            </div>
          )}
        </div>
      </div>

      {/* Push results */}
      {pushResults && (
        <div className="mt-6 bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
          <div className="px-6 py-4 border-b border-gray-800">
            <h3 className="font-medium text-sm">Push Results</h3>
          </div>
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-gray-800 text-gray-400 text-xs">
                <th className="text-left px-6 py-3 font-medium">Node</th>
                <th className="text-left px-6 py-3 font-medium">Status</th>
                <th className="text-left px-6 py-3 font-medium">Task ID</th>
                <th className="text-left px-6 py-3 font-medium">Detail</th>
              </tr>
            </thead>
            <tbody>
              {pushResults.map((r) => (
                <tr key={r.node_id} className="border-b border-gray-800 last:border-0">
                  <td className="px-6 py-3 font-medium">{r.hostname}</td>
                  <td className="px-6 py-3">
                    <span
                      className={`text-xs ${
                        r.status === 'dispatched'
                          ? 'text-emerald-400'
                          : r.status === 'offline'
                          ? 'text-yellow-400'
                          : 'text-red-400'
                      }`}
                    >
                      {r.status}
                    </span>
                  </td>
                  <td className="px-6 py-3 font-mono text-xs text-gray-500">
                    {r.task_id ? r.task_id.slice(0, 8) + '…' : '—'}
                  </td>
                  <td className="px-6 py-3 text-xs text-gray-500">{r.error ?? ''}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Save / push errors */}
      {saveMut.isError && (
        <p className="mt-4 text-sm text-red-400">Save failed — check the template syntax.</p>
      )}
      {pushMut.isError && (
        <p className="mt-4 text-sm text-red-400">Push failed — check server logs.</p>
      )}
    </Layout>
  )
}
