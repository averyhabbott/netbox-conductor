import { useState, useRef, useEffect } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { clustersApi } from '../api/clusters'
import { nodesApi } from '../api/nodes'
import client from '../api/client'
import Layout from '../components/Layout'
import { useAuthStore } from '../store/auth'

interface LogsData {
  lines: string[]
  path: string
  node: string
}

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

// Log level ordering (lower index = lower priority)
const LOG_LEVELS = ['debug', 'info', 'warn', 'error'] as const
type LogLevel = typeof LOG_LEVELS[number]

// filterLogLines returns only lines whose level is >= minLevel.
// Handles slog text format: `level=INFO ...` and `level=WARN ...`
// Lines with no recognisable level are kept when minLevel is debug/info,
// excluded at warn/error (they're typically continuation lines or blanks).
function filterLogLines(lines: string[], minLevel: LogLevel): string[] {
  const minIdx = LOG_LEVELS.indexOf(minLevel)
  return lines.filter((line) => {
    const m = line.match(/\blevel=([A-Za-z]+)/i)
    if (!m) {
      // Keep unrecognised lines for low thresholds only
      return minIdx <= 1
    }
    const lvl = m[1].toLowerCase() as LogLevel
    const idx = LOG_LEVELS.indexOf(lvl)
    if (idx === -1) return minIdx <= 1
    return idx >= minIdx
  })
}

// ── Sparkline ─────────────────────────────────────────────────────────────────

const MAX_SAMPLES = 24 // ~6 minutes at 15s refetch

interface Sample {
  ts: number
  netbox: boolean | null
  rq: boolean | null
  lag: number | null // patroni xlog lag seconds, if available
}

function useSampleHistory(node: import('../api/nodes').Node | undefined) {
  const historyRef = useRef<Sample[]>([])

  useEffect(() => {
    if (!node) return
    const lag = (() => {
      try {
        const s = node.patroni_state as any
        return typeof s?.xlog?.received_location === 'number' ? s.xlog.received_location : null
      } catch { return null }
    })()
    historyRef.current = [
      ...historyRef.current,
      { ts: Date.now(), netbox: node.netbox_running ?? null, rq: node.rq_running ?? null, lag },
    ].slice(-MAX_SAMPLES)
  }, [node])

  return historyRef.current
}

interface SparklineProps {
  samples: (boolean | null | number)[]
  color?: string
  height?: number
  width?: number
}

function Sparkline({ samples, color = '#34d399', height = 24, width = 80 }: SparklineProps) {
  if (samples.length < 2) {
    return <span className="text-xs text-gray-600">collecting…</span>
  }

  const nums = samples.map((v) => {
    if (v === null || v === undefined) return 0.5
    if (typeof v === 'boolean') return v ? 1 : 0
    return v
  })

  const min = Math.min(...nums)
  const max = Math.max(...nums)
  const range = max - min || 1

  const pts = nums.map((v, i) => {
    const x = (i / (nums.length - 1)) * width
    const y = height - ((v - min) / range) * (height - 4) - 2
    return `${x},${y}`
  })

  return (
    <svg width={width} height={height} className="inline-block align-middle">
      <polyline
        points={pts.join(' ')}
        fill="none"
        stroke={color}
        strokeWidth={1.5}
        strokeLinejoin="round"
        strokeLinecap="round"
      />
    </svg>
  )
}

// ── Status row ────────────────────────────────────────────────────────────────

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

// ── DB Restore modal ──────────────────────────────────────────────────────────

function DBRestoreModal({
  clusterId,
  nodeId,
  hostname,
  onClose,
}: {
  clusterId: string
  nodeId: string
  hostname: string
  onClose: () => void
}) {
  const [method, setMethod] = useState<'reinitialize' | 'pitr'>('reinitialize')
  const [targetTime, setTargetTime] = useState('')
  const [restoreCmd, setRestoreCmd] = useState('')
  const [result, setResult] = useState<string | null>(null)

  const restore = useMutation({
    mutationFn: () =>
      client
        .post(`/clusters/${clusterId}/nodes/${nodeId}/db-restore`, {
          method,
          target_time: targetTime || undefined,
          restore_cmd: restoreCmd || undefined,
        })
        .then((r) => r.data as { task_id: string }),
    onSuccess: (data) => setResult(data.task_id),
  })

  return (
    <div className="fixed inset-0 bg-black/70 flex items-center justify-center z-50 p-4">
      <div className="bg-gray-900 border border-gray-700 rounded-xl p-6 w-full max-w-md">
        <h3 className="text-lg font-semibold text-red-400 mb-1">DB Restore / PITR</h3>
        <p className="text-xs text-gray-400 mb-4">
          Target: <span className="font-mono text-white">{hostname}</span>
        </p>

        {result ? (
          <div className="space-y-4">
            <div className="bg-emerald-900/30 border border-emerald-700 rounded px-3 py-2 text-sm text-emerald-300">
              Restore dispatched — task <span className="font-mono">{result.slice(0, 8)}…</span>
            </div>
            <button
              onClick={onClose}
              className="w-full py-2 text-sm bg-gray-700 hover:bg-gray-600 rounded-lg"
            >
              Close
            </button>
          </div>
        ) : (
          <div className="space-y-4">
            <div>
              <label className="block text-xs text-gray-400 mb-1">Method</label>
              <select
                value={method}
                onChange={(e) => setMethod(e.target.value as 'reinitialize' | 'pitr')}
                className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
              >
                <option value="reinitialize">Reinitialize (clone from primary)</option>
                <option value="pitr">Point-in-Time Recovery (pgBackRest / custom)</option>
              </select>
            </div>

            {method === 'pitr' && (
              <>
                <div>
                  <label className="block text-xs text-gray-400 mb-1">
                    Target time (RFC3339, e.g. 2024-01-15T14:30:00Z)
                  </label>
                  <input
                    className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm font-mono focus:outline-none focus:border-blue-500"
                    value={targetTime}
                    onChange={(e) => setTargetTime(e.target.value)}
                    placeholder="2024-01-15T14:30:00Z"
                  />
                </div>
                <div>
                  <label className="block text-xs text-gray-400 mb-1">
                    Custom restore command (optional — overrides pgBackRest default)
                  </label>
                  <input
                    className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm font-mono focus:outline-none focus:border-blue-500"
                    value={restoreCmd}
                    onChange={(e) => setRestoreCmd(e.target.value)}
                    placeholder="pgbackrest --stanza=main restore …"
                  />
                </div>
              </>
            )}

            {restore.isError && (
              <p className="text-xs text-red-400">
                {(restore.error as any)?.response?.data?.message ?? 'Restore failed'}
              </p>
            )}

            <div className="flex gap-3 justify-end pt-2">
              <button
                onClick={onClose}
                className="px-4 py-2 text-sm text-gray-400 hover:text-white"
              >
                Cancel
              </button>
              <button
                onClick={() => restore.mutate()}
                disabled={restore.isPending || (method === 'pitr' && !targetTime)}
                className="px-4 py-2 text-sm bg-red-700 hover:bg-red-600 disabled:opacity-40 rounded-lg"
              >
                {restore.isPending ? 'Dispatching…' : 'Run restore'}
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}


// ── RemoveNodeDialog ───────────────────────────────────────────────────────────

type RemoveMode = 'decommission' | 'force_remove'

function RemoveNodeDialog({
  hostname,
  agentStatus,
  onConfirm,
  onCancel,
  isPending,
}: {
  hostname: string
  agentStatus: string
  onConfirm: () => void
  onCancel: () => void
  isPending: boolean
}) {
  const [dialogStep, setDialogStep] = useState<1 | 2>(1)
  const [mode, setMode] = useState<RemoveMode | null>(null)
  const [hostnameInput, setHostnameInput] = useState('')
  const [copied, setCopied] = useState(false)

  const cleanupCommands = [
    'sudo systemctl stop netbox-agent',
    'sudo systemctl disable netbox-agent',
    'sudo rm /etc/systemd/system/netbox-agent.service',
    'sudo rm /usr/local/bin/netbox-agent',
    'sudo rm -rf /etc/netbox-agent/',
  ].join('\n')

  const copyCleanup = async () => {
    await navigator.clipboard.writeText(cleanupCommands)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  return (
    <div className="fixed inset-0 bg-black/70 flex items-center justify-center z-50 p-4">
      <div className="bg-gray-900 border border-gray-700 rounded-xl w-full max-w-lg">
        {/* Header */}
        <div className="flex items-center justify-between px-6 py-4 border-b border-gray-800">
          <div>
            <h3 className="font-semibold text-red-400">
              {dialogStep === 1
                ? 'Remove Node'
                : mode === 'decommission'
                ? 'Decommission Node'
                : 'Force Remove Node'}
            </h3>
            <p className="text-xs text-gray-500 font-mono mt-0.5">{hostname}</p>
          </div>
          {/* Step indicator */}
          <div className="flex items-center gap-2">
            {([1, 2] as const).map((s) => (
              <div
                key={s}
                className={`w-6 h-6 rounded-full flex items-center justify-center text-xs font-medium ${
                  dialogStep === s
                    ? 'bg-red-700 text-white'
                    : dialogStep > s
                    ? 'bg-gray-600 text-white'
                    : 'bg-gray-800 text-gray-500'
                }`}
              >
                {dialogStep > s ? '✓' : s}
              </div>
            ))}
          </div>
        </div>

        <div className="px-6 py-5 space-y-4">
          {/* ── Step 1: Choose mode ── */}
          {dialogStep === 1 && (
            <>
              <p className="text-sm text-gray-400">Choose how to remove this node:</p>
              <div className="grid grid-cols-2 gap-3">
                {/* Decommission */}
                <button
                  type="button"
                  onClick={() => setMode('decommission')}
                  className={`text-left p-4 rounded-lg border transition-colors ${
                    mode === 'decommission'
                      ? 'border-red-600 bg-red-900/20'
                      : 'border-gray-700 bg-gray-800 hover:border-gray-600'
                  }`}
                >
                  <p className="text-sm font-medium text-gray-200">Decommission</p>
                  <p className="text-xs text-gray-400 mt-1">
                    Full removal with agent cleanup guidance. Use when permanently retiring this node.
                  </p>
                  <p className="text-xs text-emerald-500 mt-2">Recommended</p>
                </button>
                {/* Force Remove */}
                <button
                  type="button"
                  onClick={() => setMode('force_remove')}
                  className={`text-left p-4 rounded-lg border transition-colors ${
                    mode === 'force_remove'
                      ? 'border-amber-600 bg-amber-900/20'
                      : 'border-gray-700 bg-gray-800 hover:border-gray-600'
                  }`}
                >
                  <p className="text-sm font-medium text-gray-200">Force Remove</p>
                  <p className="text-xs text-gray-400 mt-1">
                    Removes node from the conductor only. Agent process on the host is not stopped.
                  </p>
                  <p className="text-xs text-amber-500 mt-2">Use when node is already gone</p>
                </button>
              </div>
              <div className="flex justify-end gap-3 pt-1">
                <button
                  type="button"
                  onClick={onCancel}
                  className="text-sm text-gray-400 hover:text-gray-200 px-4 py-2"
                >
                  Cancel
                </button>
                <button
                  type="button"
                  onClick={() => setDialogStep(2)}
                  disabled={mode === null}
                  className="bg-red-700 hover:bg-red-600 disabled:opacity-40 text-sm px-4 py-2 rounded-lg transition-colors"
                >
                  Continue →
                </button>
              </div>
            </>
          )}

          {/* ── Step 2: Confirm ── */}
          {dialogStep === 2 && mode && (
            <>
              {/* Irreversibility banner */}
              <div className="bg-red-900/30 border border-red-800 rounded-lg px-4 py-3">
                <p className="text-sm font-semibold text-red-400">
                  This action is permanent and cannot be undone.
                </p>
                <p className="text-xs text-red-300/80 mt-1">
                  You chose{' '}
                  <span className="font-semibold">
                    {mode === 'decommission' ? 'Decommission' : 'Force Remove'}
                  </span>
                  . All node records, tokens, task history, and config overrides will be deleted
                  from the conductor.
                </p>
              </div>

              {mode === 'decommission' && (
                <>
                  {/* Agent connection status note */}
                  <p className="text-sm text-gray-300">
                    {agentStatus === 'connected'
                      ? 'The agent is currently connected and will be disconnected immediately. Its token will be invalidated.'
                      : 'The agent process may still be running on the host — run the cleanup commands below after decommissioning.'}
                  </p>

                  {/* Manual cleanup */}
                  <div>
                    <p className="text-xs text-gray-400 mb-1">
                      Run on <span className="font-mono text-gray-300">{hostname}</span> after decommissioning:
                    </p>
                    <div className="relative">
                      <pre className="bg-gray-950 border border-gray-800 rounded-lg p-3 text-xs font-mono text-gray-300 whitespace-pre">
{cleanupCommands}
                      </pre>
                      <button
                        type="button"
                        onClick={copyCleanup}
                        className="absolute top-2 right-2 bg-gray-800 hover:bg-gray-700 text-xs px-2 py-1 rounded transition-colors"
                      >
                        {copied ? '✓ Copied' : 'Copy'}
                      </button>
                    </div>
                  </div>

                  {/* Hostname confirmation */}
                  <div>
                    <p className="text-sm text-gray-400 mb-1">
                      Type <span className="font-mono text-white">{hostname}</span> to confirm:
                    </p>
                    <input
                      className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm font-mono focus:outline-none focus:border-red-600"
                      value={hostnameInput}
                      onChange={(e) => setHostnameInput(e.target.value)}
                      placeholder={hostname}
                      autoFocus
                    />
                  </div>
                </>
              )}

              {mode === 'force_remove' && (
                <p className="text-sm text-gray-300">
                  The agent process on <span className="font-mono text-gray-100">{hostname}</span> will
                  not be stopped. If the agent is still running, it will attempt to reconnect but its
                  token will be invalidated and reconnection will fail.
                </p>
              )}

              <div className="flex justify-end gap-3 pt-1">
                <button
                  type="button"
                  onClick={() => { setDialogStep(1); setHostnameInput('') }}
                  className="text-sm text-gray-400 hover:text-gray-200 px-4 py-2"
                >
                  ← Back
                </button>
                <button
                  type="button"
                  onClick={onConfirm}
                  disabled={
                    isPending ||
                    (mode === 'decommission' && hostnameInput !== hostname)
                  }
                  className={`disabled:opacity-40 text-sm px-4 py-2 rounded-lg transition-colors ${
                    mode === 'decommission'
                      ? 'bg-red-700 hover:bg-red-600'
                      : 'bg-amber-700 hover:bg-amber-600'
                  }`}
                >
                  {isPending
                    ? 'Removing…'
                    : mode === 'decommission'
                    ? 'Decommission Node'
                    : 'Remove from Conductor'}
                </button>
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  )
}

export default function NodeDetail() {
  const { id, nid } = useParams<{ id: string; nid: string }>()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const userRole = useAuthStore((s) => s.user?.role)
  const [actionResult, setActionResult] = useState<{ success: boolean; message: string } | null>(null)
  const [showDBRestore, setShowDBRestore] = useState(false)
  const [showRemoveNode, setShowRemoveNode] = useState(false)
  const [showNetboxMenu, setShowNetboxMenu] = useState(false)
  const netboxMenuRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!showNetboxMenu) return
    const handler = (e: MouseEvent) => {
      if (netboxMenuRef.current && !netboxMenuRef.current.contains(e.target as HTMLElement)) {
        setShowNetboxMenu(false)
      }
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [showNetboxMenu])

  const removeNode = useMutation({
    mutationFn: () => nodesApi.delete(id!, nid!),
    onSuccess: () => navigate(`/clusters/${id}`),
  })

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

  const toggleMaintenance = useMutation({
    mutationFn: (enabled: boolean) => nodesApi.setMaintenance(id!, nid!, enabled),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['node', nid] }),
  })

  const [envDownloading, setEnvDownloading] = useState(false)
  const downloadEnv = async () => {
    setEnvDownloading(true)
    try {
      await nodesApi.downloadAgentEnv(id!, nid!, node?.hostname ?? nid!)
    } finally {
      setEnvDownloading(false)
    }
  }

  const [showEnvMenu, setShowEnvMenu] = useState(false)
  const envMenuRef = useRef<HTMLDivElement>(null)
  const [envContent, setEnvContent] = useState<string | null>(null)
  const [envViewing, setEnvViewing] = useState(false)
  const [envCopied, setEnvCopied] = useState(false)

  useEffect(() => {
    if (!showEnvMenu) return
    const handler = (e: MouseEvent) => {
      if (envMenuRef.current && !envMenuRef.current.contains(e.target as HTMLElement)) {
        setShowEnvMenu(false)
      }
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [showEnvMenu])

  const viewEnv = async () => {
    setShowEnvMenu(false)
    setEnvViewing(true)
    try {
      const text = await nodesApi.fetchAgentEnvText(id!, nid!)
      setEnvContent(text)
    } catch {
      setEnvContent('Failed to fetch agent .env file.')
    } finally {
      setEnvViewing(false)
    }
  }

  const { data: tasksData } = useQuery({
    queryKey: ['node-tasks', nid],
    queryFn: () =>
      client
        .get<{ tasks: TaskRow[] }>(`/clusters/${id}/nodes/${nid}/tasks?limit=20`)
        .then((r) => r.data),
    enabled: !!id && !!nid,
    refetchInterval: 10_000,
  })

  const [logSource, setLogSource] = useState<'agent' | 'netbox'>('agent')
  const [netboxLogName, setNetboxLogName] = useState<string>('')
  const [logLevel, setLogLevel] = useState<'debug' | 'info' | 'warn' | 'error'>('info')

  const { data: netboxLogNamesData } = useQuery({
    queryKey: ['netbox-log-names', nid],
    queryFn: () => nodesApi.getNetboxLogNames(id!, nid!),
    enabled: !!id && !!nid && logSource === 'netbox',
    refetchInterval: 30_000,
  })

  const availableLogNames = netboxLogNamesData?.names ?? []
  // Auto-select first available name when the list changes and nothing is selected
  const effectiveLogName = netboxLogName || availableLogNames[0] || ''

  const { data: logsData, refetch: refetchLogs, isFetching: logsFetching } = useQuery<LogsData>({
    queryKey: ['node-logs', nid, logSource, effectiveLogName],
    queryFn: () => nodesApi.getLogs(id!, nid!, 200, logSource, logSource === 'netbox' ? effectiveLogName || undefined : undefined),
    enabled: !!id && !!nid,
    refetchInterval: 30_000,
  })

  const history = useSampleHistory(node)

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
        <div className="flex gap-2 flex-wrap items-center">
          <div className="relative" ref={envMenuRef}>
            <button
              onClick={() => setShowEnvMenu((v) => !v)}
              disabled={envDownloading || envViewing}
              className="bg-gray-800 hover:bg-gray-700 disabled:opacity-40 text-sm px-3 py-1.5 rounded-lg transition-colors"
            >
              {envViewing ? 'Loading…' : envDownloading ? 'Generating…' : 'Agent .env ▾'}
            </button>
            {showEnvMenu && (
              <div className="absolute right-0 top-full mt-1 bg-gray-800 border border-gray-700 rounded-lg shadow-xl z-10 min-w-max">
                <button
                  onClick={viewEnv}
                  className="block w-full text-left px-4 py-2 text-sm hover:bg-gray-700 rounded-t-lg transition-colors"
                >
                  View
                </button>
                <button
                  onClick={() => { setShowEnvMenu(false); downloadEnv() }}
                  className="block w-full text-left px-4 py-2 text-sm hover:bg-gray-700 rounded-b-lg transition-colors"
                >
                  Download
                </button>
              </div>
            )}
          </div>
          {userRole === 'admin' && (
            <button
              onClick={() => setShowDBRestore(true)}
              className="bg-amber-900/40 hover:bg-amber-900/70 border border-amber-800 text-amber-400 hover:text-amber-300 text-sm px-3 py-1.5 rounded-lg transition-colors"
            >
              DB Restore
            </button>
          )}
          {/* NetBox service actions dropdown */}
          <div className="relative" ref={netboxMenuRef}>
            <button
              onClick={() => setShowNetboxMenu((v) => !v)}
              disabled={serviceAction.isPending || restartRQ.isPending || node.agent_status !== 'connected'}
              className="bg-gray-800 hover:bg-gray-700 disabled:opacity-40 text-sm px-3 py-1.5 rounded-lg transition-colors"
            >
              NetBox ▾
            </button>
            {showNetboxMenu && (
              <div className="absolute right-0 top-full mt-1 bg-gray-800 border border-gray-700 rounded-lg shadow-xl z-10 min-w-max">
                {serviceActions.map(({ label, action }) => (
                  <button
                    key={action}
                    onClick={() => { setShowNetboxMenu(false); serviceAction.mutate(action) }}
                    className="block w-full text-left px-4 py-2 text-sm hover:bg-gray-700 first:rounded-t-lg transition-colors"
                  >
                    {label}
                  </button>
                ))}
                <button
                  onClick={() => { setShowNetboxMenu(false); restartRQ.mutate() }}
                  className="block w-full text-left px-4 py-2 text-sm hover:bg-gray-700 rounded-b-lg transition-colors"
                >
                  Restart RQ
                </button>
              </div>
            )}
          </div>
          {userRole === 'admin' && (
            <button
              onClick={() => setShowRemoveNode(true)}
              className="bg-red-900/40 hover:bg-red-900/70 border border-red-800 text-red-400 hover:text-red-300 text-sm px-3 py-1.5 rounded-lg transition-colors"
            >
              Remove Node
            </button>
          )}
        </div>
      </div>

      {showRemoveNode && (
        <RemoveNodeDialog
          hostname={node.hostname}
          agentStatus={node.agent_status}
          onConfirm={() => removeNode.mutate()}
          onCancel={() => setShowRemoveNode(false)}
          isPending={removeNode.isPending}
        />
      )}

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
          <div className="flex items-center justify-between py-3 border-b border-gray-800">
            <span className="text-sm text-gray-400">NetBox</span>
            <div className="flex items-center gap-3">
              <Sparkline samples={history.map((s) => s.netbox)} color={node.netbox_running ? '#34d399' : '#f87171'} />
              <span className={`text-sm font-medium ${node.netbox_running ? 'text-emerald-400' : node.netbox_running === false ? 'text-red-400' : 'text-gray-600'}`}>
                {node.netbox_running ? 'Running' : node.netbox_running === false ? 'Stopped' : '—'}
              </span>
            </div>
          </div>
          <div className="flex items-center justify-between py-3 border-b border-gray-800">
            <span className="text-sm text-gray-400">NetBox-RQ</span>
            <div className="flex items-center gap-3">
              <Sparkline samples={history.map((s) => s.rq)} color={node.rq_running ? '#34d399' : '#f87171'} />
              <span className={`text-sm font-medium ${node.rq_running ? 'text-emerald-400' : node.rq_running === false ? 'text-red-400' : 'text-gray-600'}`}>
                {node.rq_running ? 'Running' : node.rq_running === false ? 'Stopped' : '—'}
              </span>
            </div>
          </div>

          {/* Maintenance mode toggle */}
          <div className="flex items-center justify-between py-3 border-t border-gray-800 mt-1">
            <div>
              <p className="text-sm text-gray-400">Maintenance Mode</p>
              <p className="text-xs text-gray-600 mt-0.5">
                Suppresses auto-start and excludes node from failover target selection
              </p>
            </div>
            <button
              onClick={() => toggleMaintenance.mutate(!node.maintenance_mode)}
              disabled={toggleMaintenance.isPending}
              className={`relative w-10 h-6 rounded-full transition-colors disabled:opacity-40 ${
                node.maintenance_mode ? 'bg-amber-600' : 'bg-gray-700'
              }`}
            >
              <span
                className={`absolute top-1 w-4 h-4 bg-white rounded-full shadow transition-all ${
                  node.maintenance_mode ? 'left-5' : 'left-1'
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

        {/* Logs */}
        <div className="bg-gray-900 border border-gray-800 rounded-xl p-6 md:col-span-2">
          <div className="flex items-center justify-between mb-4">
            <div>
              <div className="flex items-center gap-2 flex-wrap">
                <h3 className="font-medium">Logs</h3>
                <div className="flex text-xs border border-gray-700 rounded-lg overflow-hidden">
                  {(['agent', 'netbox'] as const).map((src) => (
                    <button
                      key={src}
                      onClick={() => setLogSource(src)}
                      className={`px-2.5 py-1 transition-colors ${
                        logSource === src
                          ? 'bg-blue-600 text-white'
                          : 'text-gray-400 hover:text-white'
                      }`}
                    >
                      {src === 'agent' ? 'Agent' : 'NetBox'}
                    </button>
                  ))}
                </div>
                <select
                  value={logLevel}
                  onChange={(e) => setLogLevel(e.target.value as typeof logLevel)}
                  className="bg-gray-800 border border-gray-700 rounded px-2 py-0.5 text-xs focus:outline-none focus:border-blue-500"
                  title="Minimum log level to display"
                >
                  <option value="debug">debug+</option>
                  <option value="info">info+</option>
                  <option value="warn">warn+</option>
                  <option value="error">error only</option>
                </select>
              </div>
              {logSource === 'netbox' && availableLogNames.length > 1 && (
                <select
                  value={effectiveLogName}
                  onChange={(e) => setNetboxLogName(e.target.value)}
                  className="mt-1.5 bg-gray-800 border border-gray-700 rounded px-2 py-1 text-xs focus:outline-none focus:border-blue-500"
                >
                  {availableLogNames.map((n) => (
                    <option key={n} value={n}>{n}</option>
                  ))}
                </select>
              )}
              {logsData?.path && (
                <p className="text-xs text-gray-600 mt-0.5 font-mono">{logsData.path}</p>
              )}
            </div>
            <button
              onClick={() => refetchLogs()}
              disabled={logsFetching}
              className="text-xs bg-gray-800 hover:bg-gray-700 disabled:opacity-40 px-3 py-1.5 rounded-lg transition-colors"
            >
              {logsFetching ? 'Refreshing…' : 'Refresh'}
            </button>
          </div>
          {!logsData?.lines?.length ? (
            <p className="text-sm text-gray-500">
              {logSource === 'netbox'
                ? 'No NetBox log entries yet. The agent discovers log files from the LOGGING section in configuration.py and forwards them automatically. Set NETBOX_LOG_PATH as a fallback if no LOGGING section is configured.'
                : 'No log entries yet. Logs appear once the agent connects and the log file is created on the Conductor node.'}
            </p>
          ) : (
            <pre className="text-xs font-mono text-gray-400 bg-gray-950 rounded-lg p-4 overflow-auto max-h-96 leading-5">
              {filterLogLines(logsData.lines, logLevel).join('\n')}
            </pre>
          )}
        </div>
      </div>

      {showDBRestore && id && nid && node && (
        <DBRestoreModal
          clusterId={id}
          nodeId={nid}
          hostname={node.hostname}
          onClose={() => setShowDBRestore(false)}
        />
      )}

      {envContent !== null && (
        <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4">
          <div className="bg-gray-900 border border-gray-700 rounded-xl shadow-2xl w-full max-w-2xl flex flex-col max-h-[80vh]">
            <div className="flex items-center justify-between px-6 py-4 border-b border-gray-800 flex-shrink-0">
              <h3 className="font-medium">Agent .env — {node.hostname}</h3>
              <div className="flex items-center gap-2">
                <button
                  onClick={() => {
                    navigator.clipboard.writeText(envContent)
                    setEnvCopied(true)
                    setTimeout(() => setEnvCopied(false), 2000)
                  }}
                  className="text-xs bg-gray-800 hover:bg-gray-700 px-3 py-1.5 rounded-lg transition-colors"
                >
                  {envCopied ? '✓ Copied' : 'Copy'}
                </button>
                <button
                  onClick={() => setEnvContent(null)}
                  className="text-gray-400 hover:text-white transition-colors text-lg leading-none"
                >
                  ✕
                </button>
              </div>
            </div>
            <pre className="text-xs font-mono text-gray-300 p-6 overflow-auto flex-1 leading-5 whitespace-pre">
              {envContent}
            </pre>
          </div>
        </div>
      )}
    </Layout>
  )
}
