import { useEffect, useState } from 'react'
import {
  alertsApi,
  type AlertRule, type AlertRuleBody,
  type AlertTransport, type AlertTransportBody,
  type AlertSchedule,
  type ActiveAlertState,
  type AlertFireLog,
} from '../api/alerts'
import { clustersApi, type Cluster } from '../api/clusters'
import { nodesApi, type Node } from '../api/nodes'
import Layout from '../components/Layout'

type Tab = 'active' | 'history' | 'rules' | 'transports' | 'schedules'

function TabBtn({ label, active, onClick }: { label: string; active: boolean; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      className={`px-4 py-2 text-sm font-medium border-b-2 transition-colors ${
        active ? 'border-blue-500 text-white' : 'border-transparent text-gray-400 hover:text-white'
      }`}
    >
      {label}
    </button>
  )
}

// ─── Shared style tokens ──────────────────────────────────────────────────────

const SEV: Record<string, string> = {
  debug:    'text-gray-400',
  info:     'text-blue-400',
  warn:     'text-amber-400',
  error:    'text-red-400',
  critical: 'text-red-300 font-semibold',
}

const SEV_BG: Record<string, string> = {
  debug:    'bg-gray-800 text-gray-400',
  info:     'bg-blue-900/50 text-blue-300',
  warn:     'bg-amber-900/50 text-amber-300',
  error:    'bg-red-900/50 text-red-300',
  critical: 'bg-red-900 text-red-200 font-semibold',
}

const inp = 'w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-white focus:outline-none focus:border-blue-500'
const sel = 'w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-gray-300 focus:outline-none focus:border-blue-500'

// ─── Fire mode labels ─────────────────────────────────────────────────────────

const FIRE_MODES = ['once', 're_alert', 'every_occurrence'] as const

const FIRE_MODE_LABELS: Record<string, string> = {
  once:             'Alert once — stays active until resolved',
  re_alert:         'Re-alert on interval',
  every_occurrence: 'Alert on every matching event',
}

const FIRE_MODE_SHORT: Record<string, string> = {
  once:             'Once',
  re_alert:         'Re-alert',
  every_occurrence: 'Every occurrence',
}

// ─── Section card wrapper ─────────────────────────────────────────────────────

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="bg-gray-900 border border-gray-800 rounded-lg p-5 space-y-4">
      <h4 className="text-xs font-semibold text-gray-500 uppercase tracking-wider">{title}</h4>
      {children}
    </div>
  )
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div>
      <label className="block text-xs text-gray-400 mb-1">{label}</label>
      {children}
      {hint && <p className="text-xs text-gray-600 mt-1">{hint}</p>}
    </div>
  )
}

// ─── Active Alerts ────────────────────────────────────────────────────────────

function ActiveTab() {
  const [states, setStates] = useState<ActiveAlertState[]>([])
  const [loading, setLoading] = useState(true)
  const [expanded, setExpanded] = useState<string | null>(null)

  async function load() {
    setLoading(true)
    try { setStates(await alertsApi.listActive()) } finally { setLoading(false) }
  }

  useEffect(() => {
    load()
    const id = setInterval(load, 15000)
    return () => clearInterval(id)
  }, [])

  async function ack(id: string) { await alertsApi.acknowledge(id); load() }
  async function resolve(id: string) { await alertsApi.resolve(id); load() }

  if (loading) return <p className="text-gray-500 text-sm py-8 text-center">Loading…</p>
  if (states.length === 0) return <p className="text-gray-600 text-sm py-8 text-center">No active alerts.</p>

  return (
    <div className="space-y-2">
      {states.map((s) => {
        const isExpanded = expanded === s.id
        return (
          <div key={s.id} className="bg-gray-900 border border-gray-700 rounded-lg overflow-hidden">
            {/* Summary row */}
            <button
              type="button"
              onClick={() => setExpanded(isExpanded ? null : s.id)}
              className="w-full text-left px-4 py-3 flex items-center gap-3 hover:bg-gray-800/50 transition-colors"
            >
              <span className={`text-xs px-1.5 py-0.5 rounded font-medium shrink-0 ${
                s.state === 'active'       ? 'bg-red-900 text-red-300' :
                s.state === 'acknowledged' ? 'bg-amber-900 text-amber-300' :
                                             'bg-gray-800 text-gray-400'
              }`}>{s.state}</span>
              <span className="text-white text-sm font-medium flex-1 truncate">
                {s.rule_name ?? `Unknown rule`}
              </span>
              {s.escalated && (
                <span className="text-xs bg-orange-900 text-orange-300 px-1.5 py-0.5 rounded shrink-0">escalated</span>
              )}
              <div className="text-gray-500 text-xs shrink-0 space-x-3">
                {s.cluster_name && <span>{s.cluster_name}</span>}
                {s.node_name    && <span>› {s.node_name}</span>}
                {s.re_alert_count > 0 && <span>· re-alerted {s.re_alert_count}×</span>}
              </div>
              <span className="text-gray-600 text-xs shrink-0">{isExpanded ? '▲' : '▼'}</span>
            </button>

            {/* Detail panel */}
            {isExpanded && (
              <div className="border-t border-gray-800 px-4 py-4 space-y-4">
                <div className="grid grid-cols-2 gap-x-8 gap-y-2 text-xs">
                  <div className="text-gray-500">Rule</div>
                  <div className="text-gray-300">{s.rule_name ?? s.rule_id}</div>

                  {s.cluster_name && <>
                    <div className="text-gray-500">Cluster</div>
                    <div className="text-gray-300">{s.cluster_name}</div>
                  </>}
                  {s.node_name && <>
                    <div className="text-gray-500">Node</div>
                    <div className="text-gray-300">{s.node_name}</div>
                  </>}

                  <div className="text-gray-500">First fired</div>
                  <div className="text-gray-300">{new Date(s.first_fired_at).toLocaleString()}</div>

                  {s.last_alerted_at && <>
                    <div className="text-gray-500">Last alerted</div>
                    <div className="text-gray-300">{new Date(s.last_alerted_at).toLocaleString()}</div>
                  </>}

                  <div className="text-gray-500">Re-alert count</div>
                  <div className="text-gray-300">{s.re_alert_count}</div>

                  <div className="text-gray-500">Escalated</div>
                  <div className="text-gray-300">{s.escalated ? 'Yes' : 'No'}</div>
                </div>

                {(s.state === 'active' || s.state === 'acknowledged') && (
                  <div className="flex gap-2 pt-1">
                    {s.state === 'active' && (
                      <button onClick={() => ack(s.id)}
                        className="px-3 py-1.5 text-sm bg-amber-700 hover:bg-amber-600 text-white rounded transition-colors">
                        Acknowledge
                      </button>
                    )}
                    <button onClick={() => resolve(s.id)}
                      className="px-3 py-1.5 text-sm bg-gray-700 hover:bg-gray-600 text-gray-200 rounded transition-colors">
                      Resolve
                    </button>
                  </div>
                )}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

// ─── Alert History ────────────────────────────────────────────────────────────

function HistoryTab() {
  const [entries, setEntries] = useState<AlertFireLog[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    alertsApi.listHistory()
      .then(setEntries)
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  if (loading) return <p className="text-gray-500 text-sm py-8 text-center">Loading…</p>
  if (entries.length === 0) return <p className="text-gray-600 text-sm py-8 text-center">No alert history in the last 30 days.</p>

  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-xs text-gray-500 border-b border-gray-800">
            <th className="pb-2 pr-4 font-medium">Time</th>
            <th className="pb-2 pr-4 font-medium">Rule</th>
            <th className="pb-2 pr-4 font-medium">Transport</th>
            <th className="pb-2 pr-4 font-medium">Severity</th>
            <th className="pb-2 pr-4 font-medium">Code</th>
            <th className="pb-2 pr-4 font-medium">Cluster / Node</th>
            <th className="pb-2 font-medium">Message</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-gray-800/60">
          {entries.map((e) => (
            <tr key={e.id} className="text-gray-300 hover:bg-gray-800/30 transition-colors">
              <td className="py-2 pr-4 text-xs text-gray-500 whitespace-nowrap">
                {new Date(e.fired_at).toLocaleString()}
              </td>
              <td className="py-2 pr-4 font-medium text-white whitespace-nowrap">
                {e.rule_name}
                {e.is_resolve && (
                  <span className="ml-1.5 text-xs bg-emerald-900/50 text-emerald-400 px-1 py-0.5 rounded">resolved</span>
                )}
              </td>
              <td className="py-2 pr-4 text-xs whitespace-nowrap">
                <span className="text-gray-300">{e.transport_name}</span>
                <span className="text-gray-600 ml-1">({e.transport_type})</span>
              </td>
              <td className="py-2 pr-4">
                <span className={`text-xs px-1.5 py-0.5 rounded ${SEV_BG[e.event_severity] ?? 'bg-gray-800 text-gray-400'}`}>
                  {e.event_severity}
                </span>
              </td>
              <td className="py-2 pr-4 text-xs font-mono text-gray-400 whitespace-nowrap">{e.event_code}</td>
              <td className="py-2 pr-4 text-xs text-gray-400 whitespace-nowrap">
                {e.cluster_name ?? (e.cluster_id ? e.cluster_id.slice(0, 8) : '—')}
                {(e.node_name || e.node_id) && (
                  <span className="text-gray-600"> › {e.node_name ?? e.node_id?.slice(0, 8)}</span>
                )}
              </td>
              <td className="py-2 text-xs text-gray-400 max-w-xs truncate" title={e.event_message}>
                {e.event_message}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// ─── Transports ───────────────────────────────────────────────────────────────

const TRANSPORT_TYPES: { value: AlertTransportBody['type']; label: string }[] = [
  { value: 'webhook',       label: 'Webhook (HTTP POST)' },
  { value: 'email',         label: 'Email (SMTP)' },
  { value: 'slack_webhook', label: 'Slack — Incoming Webhook' },
  { value: 'slack_bot',     label: 'Slack — Bot Token' },
]

function TInput(props: React.InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input {...props}
      className={`w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-white focus:outline-none focus:border-blue-500 ${props.className ?? ''}`} />
  )
}

function TransportForm({
  initial,
  onSave,
  onCancel,
}: {
  initial?: AlertTransport
  onSave: (b: AlertTransportBody) => Promise<void>
  onCancel: () => void
}) {
  const cfg = (initial?.config ?? {}) as Record<string, unknown>

  const [name, setName]       = useState(initial?.name ?? '')
  const [type, setType]       = useState<AlertTransportBody['type']>(initial?.type ?? 'webhook')
  const [enabled, setEnabled] = useState(initial?.enabled ?? true)
  const [saving, setSaving]   = useState(false)
  const [err, setErr]         = useState('')

  const [whUrl,          setWhUrl]          = useState((cfg.url as string)           ?? '')
  const [whMethod,       setWhMethod]       = useState((cfg.method as string)        ?? 'POST')
  const [whBodyTemplate, setWhBodyTemplate] = useState((cfg.body_template as string) ?? '')

  const isEdit = !!initial
  const [emailTo,   setEmailTo]   = useState(
    Array.isArray(cfg.to) ? (cfg.to as string[]).join(', ') : ((cfg.to as string) ?? '')
  )
  const [smtpHost,  setSmtpHost]  = useState((cfg.smtp_host as string)  ?? '')
  const [smtpPort,  setSmtpPort]  = useState((cfg.smtp_port as number)  ?? 587)
  const [smtpTLS,   setSmtpTLS]   = useState((cfg.smtp_tls  as boolean) ?? true)
  const [smtpFrom,  setSmtpFrom]  = useState((cfg.smtp_from as string)  ?? '')
  const [smtpUser,  setSmtpUser]  = useState((cfg.smtp_user as string)  ?? '')
  const [smtpPass,  setSmtpPass]  = useState('')

  const [slackWhUrl, setSlackWhUrl] = useState((cfg.url as string) ?? '')
  const [slackToken,   setSlackToken]   = useState('')
  const [slackChannel, setSlackChannel] = useState((cfg.channel as string) ?? '')

  function buildConfig(): Record<string, unknown> {
    switch (type) {
      case 'webhook':
        return { url: whUrl, method: whMethod, body_template: whBodyTemplate || undefined }
      case 'email': {
        const c: Record<string, unknown> = {
          smtp_host: smtpHost, smtp_port: smtpPort, smtp_tls: smtpTLS,
          smtp_from: smtpFrom, smtp_user: smtpUser,
          to: emailTo.split(',').map((s) => s.trim()).filter(Boolean),
        }
        if (smtpPass) {
          c.smtp_pass_enc = smtpPass
        } else if (isEdit && cfg.smtp_pass_enc) {
          c.smtp_pass_enc = cfg.smtp_pass_enc
        }
        return c
      }
      case 'slack_webhook':
        return { url: slackWhUrl }
      case 'slack_bot': {
        const c: Record<string, unknown> = { channel: slackChannel }
        if (slackToken) {
          c.token_enc = slackToken
        } else if (isEdit && cfg.token_enc) {
          c.token_enc = cfg.token_enc
        }
        return c
      }
    }
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setErr('')
    setSaving(true)
    try { await onSave({ name, type, config: buildConfig(), enabled }) }
    catch (ex: unknown) { setErr(ex instanceof Error ? ex.message : 'Save failed') }
    finally { setSaving(false) }
  }

  return (
    <form onSubmit={submit} className="space-y-5">
      <div className="grid grid-cols-2 gap-4">
        <Field label="Name">
          <TInput value={name} onChange={(e) => setName(e.target.value)} required />
        </Field>
        <Field label="Type">
          <select value={type} onChange={(e) => setType(e.target.value as AlertTransportBody['type'])} className={sel}>
            {TRANSPORT_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
          </select>
        </Field>
      </div>

      {type === 'webhook' && (
        <div className="space-y-3 bg-gray-800/40 border border-gray-700 rounded-lg p-4">
          <Field label="URL">
            <TInput value={whUrl} onChange={(e) => setWhUrl(e.target.value)} required placeholder="https://…" />
          </Field>
          <div className="grid grid-cols-3 gap-3">
            <Field label="Method">
              <select value={whMethod} onChange={(e) => setWhMethod(e.target.value)} className={sel}>
                {['POST','PUT','PATCH'].map((m) => <option key={m}>{m}</option>)}
              </select>
            </Field>
            <div className="col-span-2">
              <Field label="Body template (optional — leave blank for default JSON)">
                <TInput value={whBodyTemplate} onChange={(e) => setWhBodyTemplate(e.target.value)}
                  placeholder='{"text":"{{.Message}}"}' />
              </Field>
            </div>
          </div>
        </div>
      )}

      {type === 'email' && (
        <div className="space-y-3 bg-gray-800/40 border border-gray-700 rounded-lg p-4">
          <Field label="To (comma-separated)">
            <TInput value={emailTo} onChange={(e) => setEmailTo(e.target.value)} required placeholder="ops@example.com" />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="SMTP host">
              <TInput value={smtpHost} onChange={(e) => setSmtpHost(e.target.value)} required />
            </Field>
            <Field label="From address">
              <TInput value={smtpFrom} onChange={(e) => setSmtpFrom(e.target.value)} required />
            </Field>
          </div>
          <div className="grid grid-cols-3 gap-3">
            <Field label="Port">
              <TInput type="number" value={smtpPort} onChange={(e) => setSmtpPort(Number(e.target.value))} required />
            </Field>
            <Field label="Username (optional)">
              <TInput value={smtpUser} onChange={(e) => setSmtpUser(e.target.value)} />
            </Field>
            <Field label={isEdit ? 'Password (leave blank to keep current)' : 'Password (optional)'}>
              <TInput type="password" value={smtpPass} onChange={(e) => setSmtpPass(e.target.value)}
                autoComplete="new-password" />
            </Field>
          </div>
          <label className="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
            <input type="checkbox" checked={smtpTLS} onChange={(e) => setSmtpTLS(e.target.checked)} className="accent-blue-500" />
            Use TLS
          </label>
        </div>
      )}

      {type === 'slack_webhook' && (
        <div className="space-y-3 bg-gray-800/40 border border-gray-700 rounded-lg p-4">
          <Field label="Webhook URL">
            <TInput value={slackWhUrl} onChange={(e) => setSlackWhUrl(e.target.value)} required placeholder="https://hooks.slack.com/…" />
          </Field>
        </div>
      )}

      {type === 'slack_bot' && (
        <div className="space-y-3 bg-gray-800/40 border border-gray-700 rounded-lg p-4">
          <Field label={isEdit ? 'Bot token (leave blank to keep current)' : 'Bot token'}>
            <TInput type="password" value={slackToken} onChange={(e) => setSlackToken(e.target.value)}
              autoComplete="new-password" placeholder="xoxb-…" />
          </Field>
          <Field label="Channel">
            <TInput value={slackChannel} onChange={(e) => setSlackChannel(e.target.value)} required placeholder="#alerts" />
          </Field>
        </div>
      )}

      <label className="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} className="accent-blue-500" />
        Enabled
      </label>

      {err && <p className="text-red-400 text-sm">{err}</p>}
      <div className="flex gap-2">
        <button type="submit" disabled={saving}
          className="px-4 py-1.5 text-sm bg-blue-600 hover:bg-blue-500 disabled:opacity-50 text-white rounded transition-colors">
          {saving ? 'Saving…' : 'Save Transport'}
        </button>
        <button type="button" onClick={onCancel}
          className="px-4 py-1.5 text-sm bg-gray-800 hover:bg-gray-700 text-gray-300 rounded transition-colors">
          Cancel
        </button>
      </div>
    </form>
  )
}

function TransportsTab() {
  const [transports, setTransports] = useState<AlertTransport[]>([])
  const [loading, setLoading] = useState(true)
  const [adding, setAdding] = useState(false)
  const [editing, setEditing] = useState<string | null>(null)
  const [testing, setTesting] = useState<string | null>(null)

  async function load() {
    setLoading(true)
    try { setTransports(await alertsApi.listTransports()) } finally { setLoading(false) }
  }
  useEffect(() => { load() }, [])

  async function save(body: AlertTransportBody, id?: string) {
    if (id) await alertsApi.updateTransport(id, body)
    else await alertsApi.createTransport(body)
    setAdding(false); setEditing(null); load()
  }

  async function del(id: string) {
    if (!confirm('Delete this transport?')) return
    await alertsApi.deleteTransport(id); load()
  }

  async function test(id: string) {
    setTesting(id)
    try {
      await alertsApi.testTransport(id)
      alert('Test notification sent successfully.')
    } catch (ex: unknown) {
      alert('Test failed: ' + (ex instanceof Error ? ex.message : String(ex)))
    } finally { setTesting(null) }
  }

  return (
    <div className="space-y-4">
      {!adding && (
        <button onClick={() => setAdding(true)}
          className="px-3 py-1.5 text-sm bg-blue-600 hover:bg-blue-500 text-white rounded transition-colors">
          + Add Transport
        </button>
      )}
      {adding && (
        <div className="bg-gray-900 border border-gray-700 rounded-lg p-5">
          <h3 className="text-sm font-medium text-white mb-4">New Transport</h3>
          <TransportForm onSave={(b) => save(b)} onCancel={() => setAdding(false)} />
        </div>
      )}
      {loading ? <p className="text-gray-500 text-sm py-4 text-center">Loading…</p> : (
        <div className="space-y-2">
          {transports.map((t) => (
            <div key={t.id}>
              {editing === t.id ? (
                <div className="bg-gray-900 border border-gray-700 rounded-lg p-5">
                  <TransportForm initial={t} onSave={(b) => save(b, t.id)} onCancel={() => setEditing(null)} />
                </div>
              ) : (
                <div className="bg-gray-900 border border-gray-700 rounded-lg px-4 py-3 flex items-center justify-between gap-4">
                  <div>
                    <span className="text-white text-sm font-medium">{t.name}</span>
                    <span className="text-gray-500 text-xs ml-2">
                      {TRANSPORT_TYPES.find((x) => x.value === t.type)?.label ?? t.type}
                    </span>
                    {!t.enabled && <span className="text-gray-600 text-xs ml-2">disabled</span>}
                  </div>
                  <div className="flex gap-2 shrink-0">
                    <button onClick={() => test(t.id)} disabled={testing === t.id}
                      className="px-2.5 py-1 text-xs bg-gray-800 hover:bg-gray-700 border border-gray-700 text-gray-300 rounded transition-colors disabled:opacity-50">
                      {testing === t.id ? 'Testing…' : 'Test'}
                    </button>
                    <button onClick={() => setEditing(t.id)}
                      className="px-2.5 py-1 text-xs bg-gray-800 hover:bg-gray-700 border border-gray-700 text-gray-300 rounded transition-colors">
                      Edit
                    </button>
                    <button onClick={() => del(t.id)}
                      className="px-2.5 py-1 text-xs bg-red-900/50 hover:bg-red-900 border border-red-900 text-red-400 rounded transition-colors">
                      Delete
                    </button>
                  </div>
                </div>
              )}
            </div>
          ))}
          {transports.length === 0 && <p className="text-gray-600 text-sm py-4 text-center">No transports configured.</p>}
        </div>
      )}
    </div>
  )
}

// ─── Rules ────────────────────────────────────────────────────────────────────

const CATEGORIES = ['cluster', 'service', 'ha', 'config', 'agent']
const SEVERITIES = ['debug', 'info', 'warn', 'error', 'critical']

const METRIC_FIELDS: { value: string; label: string }[] = [
  { value: 'disk_used_pct',         label: 'Disk used %' },
  { value: 'mem_used_pct',          label: 'Memory used %' },
  { value: 'load_avg_1',            label: 'Load avg (1 min)' },
  { value: 'load_avg_5',            label: 'Load avg (5 min)' },
  { value: 'replication_lag_bytes', label: 'Replication lag (bytes)' },
]
const METRIC_OPERATORS = ['>', '>=', '<', '<=', '==']

function RuleForm({
  initial,
  transports,
  schedules,
  onSave,
  onCancel,
}: {
  initial?: Partial<AlertRule>
  transports: AlertTransport[]
  schedules: AlertSchedule[]
  onSave: (b: AlertRuleBody) => Promise<void>
  onCancel: () => void
}) {
  // Identification
  const [name,        setName]        = useState(initial?.name ?? '')
  const [description, setDescription] = useState(initial?.description ?? '')
  const [enabled,     setEnabled]     = useState(initial?.enabled ?? true)

  // Matching
  const [categories,  setCategories]  = useState<string[]>(initial?.categories ?? [])
  const [codes,       setCodes]       = useState((initial?.codes ?? []).join(', '))
  const [minSeverity, setMinSeverity] = useState(initial?.min_severity ?? 'warn')
  const [msgRegex,    setMsgRegex]    = useState(initial?.message_regex ?? '')
  const [scheduleId,  setScheduleId]  = useState(initial?.schedule_id ?? '')
  const [clusterId,   setClusterId]   = useState(initial?.cluster_id ?? '')
  const [nodeId,      setNodeId]      = useState(initial?.node_id ?? '')
  const [clusters,    setClusters]    = useState<Cluster[]>([])
  const [nodes,       setNodes]       = useState<Node[]>([])
  const [metricField,    setMetricField]    = useState(initial?.metric_field ?? '')
  const [metricOperator, setMetricOperator] = useState(initial?.metric_operator ?? '>')
  const [metricValue,    setMetricValue]    = useState(
    initial?.metric_value != null ? String(initial.metric_value) : ''
  )

  // Action
  const [selectedTransports, setSelectedTransports] = useState<string[]>(initial?.transport_ids ?? [])
  const [fireMode,      setFireMode]      = useState<AlertRuleBody['fire_mode']>(initial?.fire_mode ?? 'once')
  const [reAlertMins,   setReAlertMins]   = useState(String(initial?.re_alert_mins ?? ''))
  const [maxReAlerts,   setMaxReAlerts]   = useState(String(initial?.max_re_alerts ?? ''))
  const [notifyOnClear, setNotifyOnClear] = useState(initial?.notify_on_clear ?? true)
  const [escalateAfterMins,   setEscalateAfterMins]   = useState(
    initial?.escalate_after_mins != null ? String(initial.escalate_after_mins) : ''
  )
  const [escalateTransportId, setEscalateTransportId] = useState(initial?.escalate_transport_id ?? '')

  const [saving, setSaving] = useState(false)
  const [err,    setErr]    = useState('')

  useEffect(() => { clustersApi.list().then(setClusters).catch(() => {}) }, [])
  useEffect(() => {
    if (!clusterId) { setNodes([]); return }
    nodesApi.list(clusterId).then(setNodes).catch(() => {})
  }, [clusterId])

  function toggleCat(c: string) {
    setCategories((prev) => prev.includes(c) ? prev.filter((x) => x !== c) : [...prev, c])
  }
  function toggleTransport(id: string) {
    setSelectedTransports((prev) => prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id])
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setErr('')
    const body: AlertRuleBody = {
      name, description, enabled,
      categories,
      codes: codes.split(',').map((s) => s.trim()).filter(Boolean),
      min_severity: minSeverity,
      message_regex: msgRegex || undefined,
      fire_mode: fireMode,
      re_alert_mins:  reAlertMins  ? Number(reAlertMins)  : null,
      max_re_alerts:  maxReAlerts  ? Number(maxReAlerts)  : null,
      notify_on_clear: notifyOnClear,
      schedule_id: scheduleId || null,
      transport_ids: selectedTransports,
      cluster_id: clusterId || null,
      node_id:    nodeId    || null,
      metric_field:    metricField || undefined,
      metric_operator: metricField ? metricOperator : undefined,
      metric_value:    metricField && metricValue !== '' ? Number(metricValue) : undefined,
      escalate_after_mins:   escalateAfterMins   ? Number(escalateAfterMins)   : null,
      escalate_transport_id: escalateTransportId || null,
    }
    setSaving(true)
    try { await onSave(body) } catch (ex: unknown) {
      setErr(ex instanceof Error ? ex.message : 'Save failed')
    } finally { setSaving(false) }
  }

  return (
    <form onSubmit={submit} className="space-y-4">

      {/* ── Identification ──────────────────────────────────────────────────── */}
      <Section title="Identification">
        <div className="grid grid-cols-3 gap-4 items-end">
          <div className="col-span-2">
            <Field label="Name">
              <input value={name} onChange={(e) => setName(e.target.value)} required className={inp} />
            </Field>
          </div>
          <label className="flex items-center gap-2 text-sm text-gray-300 cursor-pointer pb-1.5">
            <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)}
              className="accent-blue-500" />
            Enabled
          </label>
        </div>
        <Field label="Description (optional)">
          <input value={description} onChange={(e) => setDescription(e.target.value)} className={inp}
            placeholder="What does this rule monitor?" />
        </Field>
      </Section>

      {/* ── Matching ────────────────────────────────────────────────────────── */}
      <Section title="Matching">
        <div className="grid grid-cols-2 gap-4">
          <Field label="Min severity">
            <select value={minSeverity} onChange={(e) => setMinSeverity(e.target.value)} className={sel}>
              {SEVERITIES.map((s) => <option key={s} value={s}>{s}</option>)}
            </select>
          </Field>
          <Field label="Schedule (optional)">
            <select value={scheduleId} onChange={(e) => setScheduleId(e.target.value)} className={sel}>
              <option value="">Always active</option>
              {schedules.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </select>
          </Field>
        </div>

        <Field label="Categories (empty = all)">
          <div className="flex flex-wrap gap-2 mt-1">
            {CATEGORIES.map((c) => (
              <button key={c} type="button" onClick={() => toggleCat(c)}
                className={`px-2.5 py-1 text-xs rounded border transition-colors ${
                  categories.includes(c)
                    ? 'bg-blue-700 border-blue-600 text-white'
                    : 'bg-gray-800 border-gray-700 text-gray-400 hover:text-white'
                }`}>
                {c}
              </button>
            ))}
          </div>
        </Field>

        <div className="grid grid-cols-2 gap-4">
          <Field label="Cluster scope (optional)">
            <select value={clusterId}
              onChange={(e) => { setClusterId(e.target.value); setNodeId('') }}
              className={sel}>
              <option value="">Any cluster</option>
              {clusters.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
            </select>
          </Field>
          <Field label="Node scope (optional)">
            <select value={nodeId} onChange={(e) => setNodeId(e.target.value)}
              disabled={!clusterId} className={sel}>
              <option value="">Any node</option>
              {nodes.map((n) => <option key={n.id} value={n.id}>{n.hostname}</option>)}
            </select>
          </Field>
        </div>

        <div className="grid grid-cols-2 gap-4">
          <Field label="Event codes (comma-sep, prefix ok)" hint="e.g. NBC-HA, NBC-SVC-002">
            <input value={codes} onChange={(e) => setCodes(e.target.value)}
              placeholder="NBC-HA, NBC-SVC-002" className={inp} />
          </Field>
          <Field label="Message regex (optional)">
            <input value={msgRegex} onChange={(e) => setMsgRegex(e.target.value)}
              placeholder="stopped|failed" className={inp} />
          </Field>
        </div>

        {/* Metric threshold */}
        <div>
          <label className="block text-xs text-gray-400 mb-2">Metric threshold (optional)</label>
          <div className="grid grid-cols-3 gap-3">
            <select value={metricField} onChange={(e) => setMetricField(e.target.value)} className={sel}>
              <option value="">None</option>
              {METRIC_FIELDS.map((f) => <option key={f.value} value={f.value}>{f.label}</option>)}
            </select>
            {metricField && (
              <>
                <select value={metricOperator} onChange={(e) => setMetricOperator(e.target.value)} className={sel}>
                  {METRIC_OPERATORS.map((op) => <option key={op} value={op}>{op}</option>)}
                </select>
                <input type="number" step="any" required value={metricValue}
                  onChange={(e) => setMetricValue(e.target.value)}
                  placeholder="Threshold" className={inp} />
              </>
            )}
          </div>
          {metricField && !nodeId && (
            <p className="text-amber-400 text-xs mt-2">
              ⚠ Metric rules require a specific node — select a cluster and node above.
            </p>
          )}
        </div>
      </Section>

      {/* ── Action ──────────────────────────────────────────────────────────── */}
      <Section title="Action">
        <Field label="Transports">
          {transports.length === 0 ? (
            <p className="text-gray-600 text-xs mt-1">No transports configured yet.</p>
          ) : (
            <div className="flex flex-wrap gap-2 mt-1">
              {transports.map((t) => (
                <button key={t.id} type="button" onClick={() => toggleTransport(t.id)}
                  className={`px-2.5 py-1 text-xs rounded border transition-colors ${
                    selectedTransports.includes(t.id)
                      ? 'bg-blue-700 border-blue-600 text-white'
                      : 'bg-gray-800 border-gray-700 text-gray-400 hover:text-white'
                  }`}>
                  {t.name}
                </button>
              ))}
            </div>
          )}
        </Field>

        <div className="grid grid-cols-2 gap-4">
          <div>
            <Field label="Fire mode">
              <select value={fireMode} onChange={(e) => setFireMode(e.target.value as AlertRuleBody['fire_mode'])} className={sel}>
                {FIRE_MODES.map((m) => <option key={m} value={m}>{FIRE_MODE_LABELS[m]}</option>)}
              </select>
            </Field>
            {fireMode === 'every_occurrence' && (
              <p className="text-gray-500 text-xs mt-1.5">
                No active state is tracked — acknowledge and resolve do not apply.
              </p>
            )}
          </div>
          <label className="flex items-center gap-2 text-sm text-gray-300 cursor-pointer pt-5">
            <input type="checkbox" checked={notifyOnClear} onChange={(e) => setNotifyOnClear(e.target.checked)}
              className="accent-blue-500" />
            Notify when alert clears
          </label>
        </div>

        {fireMode === 're_alert' && (
          <div className="grid grid-cols-2 gap-4">
            <Field label="Re-alert every (minutes)">
              <input type="number" min={1} required value={reAlertMins}
                onChange={(e) => setReAlertMins(e.target.value)} className={inp} />
            </Field>
            <Field label="Max re-alerts (blank = unlimited)">
              <input type="number" min={1} value={maxReAlerts}
                onChange={(e) => setMaxReAlerts(e.target.value)} className={inp} />
            </Field>
          </div>
        )}

        {fireMode !== 'every_occurrence' && (
          <div>
            <label className="block text-xs text-gray-400 mb-2">Escalation (optional)</label>
            <div className="grid grid-cols-2 gap-4">
              <Field label="Escalate after (minutes)">
                <input type="number" min={1} value={escalateAfterMins}
                  onChange={(e) => setEscalateAfterMins(e.target.value)}
                  placeholder="e.g. 30" className={inp} />
              </Field>
              <Field label="Escalate to transport">
                <select value={escalateTransportId} onChange={(e) => setEscalateTransportId(e.target.value)}
                  disabled={!escalateAfterMins} className={sel}>
                  <option value="">None</option>
                  {transports.filter((t) => t.enabled).map((t) => (
                    <option key={t.id} value={t.id}>{t.name}</option>
                  ))}
                </select>
              </Field>
            </div>
          </div>
        )}
      </Section>

      {err && <p className="text-red-400 text-sm">{err}</p>}
      <div className="flex gap-2">
        <button type="submit" disabled={saving}
          className="px-4 py-1.5 text-sm bg-blue-600 hover:bg-blue-500 disabled:opacity-50 text-white rounded transition-colors">
          {saving ? 'Saving…' : 'Save Rule'}
        </button>
        <button type="button" onClick={onCancel}
          className="px-4 py-1.5 text-sm bg-gray-800 hover:bg-gray-700 text-gray-300 rounded transition-colors">
          Cancel
        </button>
      </div>
    </form>
  )
}

function RulesTab() {
  const [rules, setRules] = useState<AlertRule[]>([])
  const [transports, setTransports] = useState<AlertTransport[]>([])
  const [schedules, setSchedules] = useState<AlertSchedule[]>([])
  const [loading, setLoading] = useState(true)
  const [adding, setAdding] = useState(false)
  const [editing, setEditing] = useState<string | null>(null)

  async function load() {
    setLoading(true)
    try {
      const [r, t, s] = await Promise.all([
        alertsApi.listRules(),
        alertsApi.listTransports(),
        alertsApi.listSchedules(),
      ])
      setRules(r); setTransports(t); setSchedules(s)
    } finally { setLoading(false) }
  }
  useEffect(() => { load() }, [])

  async function saveRule(body: AlertRuleBody, id?: string) {
    if (id) await alertsApi.updateRule(id, body)
    else await alertsApi.createRule(body)
    setAdding(false); setEditing(null); load()
  }

  async function del(id: string) {
    if (!confirm('Delete this rule?')) return
    await alertsApi.deleteRule(id); load()
  }

  async function toggleEnabled(rule: AlertRule) {
    await alertsApi.updateRule(rule.id, { ...rule, transport_ids: rule.transport_ids, enabled: !rule.enabled })
    load()
  }

  return (
    <div className="space-y-4">
      {!adding && (
        <button onClick={() => setAdding(true)}
          className="px-3 py-1.5 text-sm bg-blue-600 hover:bg-blue-500 text-white rounded transition-colors">
          + Add Rule
        </button>
      )}
      {adding && (
        <div className="bg-gray-900 border border-gray-700 rounded-lg p-5">
          <h3 className="text-sm font-medium text-white mb-4">New Alert Rule</h3>
          <RuleForm transports={transports} schedules={schedules}
            onSave={(b) => saveRule(b)} onCancel={() => setAdding(false)} />
        </div>
      )}
      {loading ? <p className="text-gray-500 text-sm py-4 text-center">Loading…</p> : (
        <div className="space-y-2">
          {rules.map((r) => (
            <div key={r.id}>
              {editing === r.id ? (
                <div className="bg-gray-900 border border-gray-700 rounded-lg p-5">
                  <RuleForm initial={r} transports={transports} schedules={schedules}
                    onSave={(b) => saveRule(b, r.id)} onCancel={() => setEditing(null)} />
                </div>
              ) : (
                <div className="bg-gray-900 border border-gray-700 rounded-lg px-4 py-3 flex items-start justify-between gap-4">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2 mb-1">
                      <span className="text-white text-sm font-medium">{r.name}</span>
                      <span className={`text-xs ${SEV[r.min_severity]}`}>{r.min_severity}+</span>
                      <span className="text-xs bg-gray-800 text-gray-400 px-1.5 py-0.5 rounded">
                        {FIRE_MODE_SHORT[r.fire_mode] ?? r.fire_mode}
                      </span>
                      {!r.enabled && <span className="text-xs text-gray-600">disabled</span>}
                    </div>
                    <div className="text-gray-500 text-xs">
                      {r.categories.length > 0 ? r.categories.join(', ') : 'all categories'}
                      {r.codes.length > 0 && <> · codes: {r.codes.join(', ')}</>}
                      {(r.transport_ids ?? []).length > 0 && (
                        <> · → {(r.transport_ids ?? []).map((id) => transports.find((t) => t.id === id)?.name ?? '?').join(', ')}</>
                      )}
                      {r.description && <> · {r.description}</>}
                    </div>
                  </div>
                  <div className="flex gap-2 shrink-0">
                    <button onClick={() => toggleEnabled(r)}
                      className={`px-2.5 py-1 text-xs border rounded transition-colors ${
                        r.enabled
                          ? 'bg-gray-800 hover:bg-gray-700 border-gray-700 text-gray-300'
                          : 'bg-emerald-900/40 hover:bg-emerald-900 border-emerald-800 text-emerald-400'
                      }`}>
                      {r.enabled ? 'Disable' : 'Enable'}
                    </button>
                    <button onClick={() => setEditing(r.id)}
                      className="px-2.5 py-1 text-xs bg-gray-800 hover:bg-gray-700 border border-gray-700 text-gray-300 rounded transition-colors">
                      Edit
                    </button>
                    <button onClick={() => del(r.id)}
                      className="px-2.5 py-1 text-xs bg-red-900/50 hover:bg-red-900 border border-red-900 text-red-400 rounded transition-colors">
                      Delete
                    </button>
                  </div>
                </div>
              )}
            </div>
          ))}
          {rules.length === 0 && <p className="text-gray-600 text-sm py-4 text-center">No alert rules configured.</p>}
        </div>
      )}
    </div>
  )
}

// ─── Schedules ────────────────────────────────────────────────────────────────

const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']

const TIMEZONES = [
  'UTC',
  'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles',
  'America/Phoenix', 'America/Anchorage', 'America/Honolulu', 'America/Toronto',
  'America/Vancouver', 'America/Sao_Paulo', 'America/Argentina/Buenos_Aires', 'America/Mexico_City',
  'Europe/London', 'Europe/Dublin', 'Europe/Lisbon', 'Europe/Paris', 'Europe/Berlin',
  'Europe/Amsterdam', 'Europe/Brussels', 'Europe/Zurich', 'Europe/Rome', 'Europe/Madrid',
  'Europe/Stockholm', 'Europe/Oslo', 'Europe/Helsinki', 'Europe/Warsaw', 'Europe/Prague',
  'Europe/Budapest', 'Europe/Bucharest', 'Europe/Athens', 'Europe/Istanbul', 'Europe/Moscow',
  'Asia/Dubai', 'Asia/Kolkata', 'Asia/Dhaka', 'Asia/Colombo', 'Asia/Bangkok', 'Asia/Jakarta',
  'Asia/Singapore', 'Asia/Kuala_Lumpur', 'Asia/Shanghai', 'Asia/Hong_Kong', 'Asia/Taipei',
  'Asia/Seoul', 'Asia/Tokyo', 'Asia/Manila', 'Asia/Karachi', 'Asia/Tashkent', 'Asia/Almaty',
  'Asia/Yekaterinburg', 'Asia/Novosibirsk', 'Asia/Vladivostok',
  'Australia/Perth', 'Australia/Darwin', 'Australia/Adelaide', 'Australia/Brisbane',
  'Australia/Sydney', 'Australia/Melbourne',
  'Pacific/Auckland', 'Pacific/Fiji', 'Pacific/Honolulu',
  'Africa/Cairo', 'Africa/Johannesburg', 'Africa/Lagos', 'Africa/Nairobi',
]

function SchedulesTab() {
  const [schedules, setSchedules] = useState<AlertSchedule[]>([])
  const [loading, setLoading] = useState(true)
  const [adding, setAdding] = useState(false)
  const [name, setName] = useState('')
  const [timezone, setTimezone] = useState('UTC')
  const [windows, setWindows] = useState<AlertSchedule['windows']>([{ days: [1,2,3,4,5], start: '09:00', end: '17:00' }])
  const [saving, setSaving] = useState(false)

  async function load() {
    setLoading(true)
    try { setSchedules(await alertsApi.listSchedules()) } finally { setLoading(false) }
  }
  useEffect(() => { load() }, [])

  function toggleDay(wi: number, day: number) {
    setWindows((ws) => ws.map((w, i) => i !== wi ? w : {
      ...w,
      days: w.days.includes(day) ? w.days.filter((d) => d !== day) : [...w.days, day].sort(),
    }))
  }

  function setWindowField(wi: number, field: 'start' | 'end', val: string) {
    setWindows((ws) => ws.map((w, i) => i !== wi ? w : { ...w, [field]: val }))
  }

  async function save(e: React.FormEvent) {
    e.preventDefault()
    setSaving(true)
    try { await alertsApi.createSchedule({ name, timezone, windows }); setAdding(false); setName(''); load() }
    finally { setSaving(false) }
  }

  async function del(id: string) {
    if (!confirm('Delete this schedule?')) return
    await alertsApi.deleteSchedule(id); load()
  }

  const baseInput = 'w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-white focus:outline-none focus:border-blue-500'

  return (
    <div className="space-y-4">
      {!adding && (
        <button onClick={() => setAdding(true)}
          className="px-3 py-1.5 text-sm bg-blue-600 hover:bg-blue-500 text-white rounded transition-colors">
          + Add Schedule
        </button>
      )}
      {adding && (
        <form onSubmit={save} className="bg-gray-900 border border-gray-700 rounded-lg p-5 space-y-4">
          <h3 className="text-sm font-medium text-white">New Schedule</h3>
          <div className="grid grid-cols-2 gap-4">
            <div>
              <label className="block text-xs text-gray-400 mb-1">Name</label>
              <input value={name} onChange={(e) => setName(e.target.value)} required className={baseInput} />
            </div>
            <div>
              <label className="block text-xs text-gray-400 mb-1">Timezone</label>
              <select value={timezone} onChange={(e) => setTimezone(e.target.value)} className={baseInput}>
                {TIMEZONES.map((tz) => <option key={tz} value={tz}>{tz}</option>)}
              </select>
            </div>
          </div>
          <div className="space-y-3">
            <div className="flex items-center justify-between">
              <span className="text-xs text-gray-400">Time windows</span>
              <button type="button"
                onClick={() => setWindows((w) => [...w, { days: [1,2,3,4,5], start: '09:00', end: '17:00' }])}
                className="text-xs text-blue-400 hover:text-blue-300">+ Add window</button>
            </div>
            {windows.map((w, wi) => (
              <div key={wi} className="bg-gray-800 rounded p-3 space-y-2">
                <div className="flex gap-1 flex-wrap">
                  {DAYS.map((d, di) => (
                    <button key={di} type="button" onClick={() => toggleDay(wi, di)}
                      className={`px-2 py-0.5 text-xs rounded border transition-colors ${
                        w.days.includes(di)
                          ? 'bg-blue-700 border-blue-600 text-white'
                          : 'bg-gray-700 border-gray-600 text-gray-400'
                      }`}>{d}</button>
                  ))}
                </div>
                <div className="flex items-center gap-2">
                  <input type="time" value={w.start} onChange={(e) => setWindowField(wi, 'start', e.target.value)}
                    className="bg-gray-700 border border-gray-600 rounded px-2 py-1 text-xs text-white focus:outline-none" />
                  <span className="text-gray-600 text-xs">to</span>
                  <input type="time" value={w.end} onChange={(e) => setWindowField(wi, 'end', e.target.value)}
                    className="bg-gray-700 border border-gray-600 rounded px-2 py-1 text-xs text-white focus:outline-none" />
                  {windows.length > 1 && (
                    <button type="button" onClick={() => setWindows((ws) => ws.filter((_, i) => i !== wi))}
                      className="text-red-500 hover:text-red-400 text-xs ml-auto">Remove</button>
                  )}
                </div>
              </div>
            ))}
          </div>
          <div className="flex gap-2">
            <button type="submit" disabled={saving}
              className="px-4 py-1.5 text-sm bg-blue-600 hover:bg-blue-500 disabled:opacity-50 text-white rounded transition-colors">
              {saving ? 'Saving…' : 'Save'}
            </button>
            <button type="button" onClick={() => setAdding(false)}
              className="px-4 py-1.5 text-sm bg-gray-800 hover:bg-gray-700 text-gray-300 rounded transition-colors">
              Cancel
            </button>
          </div>
        </form>
      )}
      {loading ? <p className="text-gray-500 text-sm py-4 text-center">Loading…</p> : (
        <div className="space-y-2">
          {schedules.map((s) => (
            <div key={s.id} className="bg-gray-900 border border-gray-700 rounded-lg px-4 py-3 flex items-center justify-between">
              <div>
                <span className="text-white text-sm font-medium">{s.name}</span>
                <span className="text-gray-500 text-xs ml-3">{s.timezone} · {s.windows.length} window(s)</span>
              </div>
              <button onClick={() => del(s.id)}
                className="px-2.5 py-1 text-xs bg-red-900/50 hover:bg-red-900 border border-red-900 text-red-400 rounded transition-colors">
                Delete
              </button>
            </div>
          ))}
          {schedules.length === 0 && <p className="text-gray-600 text-sm py-4 text-center">No schedules configured.</p>}
        </div>
      )}
    </div>
  )
}

// ─── Page ─────────────────────────────────────────────────────────────────────

export default function AlertingPage() {
  const [tab, setTab] = useState<Tab>('active')

  return (
    <Layout>
      <div className="max-w-7xl mx-auto px-6 py-8">
        <h1 className="text-xl font-semibold text-white mb-6">Alerting</h1>

        <div className="flex gap-1 mb-6 border-b border-gray-800">
          <TabBtn label="Active Alerts" active={tab === 'active'}     onClick={() => setTab('active')} />
          <TabBtn label="History"       active={tab === 'history'}    onClick={() => setTab('history')} />
          <TabBtn label="Rules"         active={tab === 'rules'}      onClick={() => setTab('rules')} />
          <TabBtn label="Transports"    active={tab === 'transports'} onClick={() => setTab('transports')} />
          <TabBtn label="Schedules"     active={tab === 'schedules'}  onClick={() => setTab('schedules')} />
        </div>

        {tab === 'active'     && <ActiveTab />}
        {tab === 'history'    && <HistoryTab />}
        {tab === 'rules'      && <RulesTab />}
        {tab === 'transports' && <TransportsTab />}
        {tab === 'schedules'  && <SchedulesTab />}
      </div>
    </Layout>
  )
}
