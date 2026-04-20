import { useEffect, useState } from 'react'
import {
  alertsApi,
  type AlertRule, type AlertRuleBody,
  type AlertTransport, type AlertTransportBody,
  type AlertSchedule,
  type ActiveAlertState,
} from '../api/alerts'
import { clustersApi, type Cluster } from '../api/clusters'
import { nodesApi, type Node } from '../api/nodes'
import Layout from '../components/Layout'

type Tab = 'active' | 'rules' | 'transports' | 'schedules'

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

// ─── Severity badge ───────────────────────────────────────────────────────────

const SEV: Record<string, string> = {
  debug:    'text-gray-400',
  info:     'text-blue-400',
  warn:     'text-amber-400',
  error:    'text-red-400',
  critical: 'text-red-300 font-semibold',
}

// ─── Active Alerts ────────────────────────────────────────────────────────────

function ActiveTab() {
  const [states, setStates] = useState<ActiveAlertState[]>([])
  const [loading, setLoading] = useState(true)

  async function load() {
    setLoading(true)
    try { setStates(await alertsApi.listActive()) } finally { setLoading(false) }
  }

  useEffect(() => {
    load()
    const id = setInterval(load, 15000)
    return () => clearInterval(id)
  }, [])

  async function ack(id: string) {
    await alertsApi.acknowledge(id)
    load()
  }

  async function resolve(id: string) {
    await alertsApi.resolve(id)
    load()
  }

  if (loading) return <p className="text-gray-500 text-sm py-8 text-center">Loading…</p>
  if (states.length === 0) return <p className="text-gray-600 text-sm py-8 text-center">No active alerts.</p>

  return (
    <div className="space-y-3">
      {states.map((s) => (
        <div key={s.id} className="bg-gray-900 border border-gray-700 rounded-lg p-4 flex items-start justify-between gap-4">
          <div>
            <div className="flex items-center gap-2 mb-1">
              <span className={`text-xs px-1.5 py-0.5 rounded font-medium ${
                s.state === 'active' ? 'bg-red-900 text-red-300' :
                s.state === 'acknowledged' ? 'bg-amber-900 text-amber-300' : 'bg-gray-800 text-gray-400'
              }`}>{s.state}</span>
              <span className="text-white text-sm font-medium">{s.rule_name ?? `Unknown rule (${s.rule_id.slice(0, 8)})`}</span>
              {s.escalated && <span className="text-xs bg-orange-900 text-orange-300 px-1.5 py-0.5 rounded">escalated</span>}
            </div>
            <div className="text-gray-500 text-xs space-x-3">
              <span>First fired: {new Date(s.first_fired_at).toLocaleString()}</span>
              <span>Re-alerts: {s.re_alert_count}</span>
              {s.cluster_id && <span>Cluster: {s.cluster_id.slice(0, 8)}</span>}
              {s.node_id && <span>Node: {s.node_id.slice(0, 8)}</span>}
            </div>
          </div>
          {(s.state === 'active' || s.state === 'acknowledged') && (
            <div className="flex gap-2 shrink-0">
              {s.state === 'active' && (
                <button
                  onClick={() => ack(s.id)}
                  className="px-3 py-1.5 text-sm bg-amber-700 hover:bg-amber-600 text-white rounded transition-colors"
                >
                  Acknowledge
                </button>
              )}
              <button
                onClick={() => resolve(s.id)}
                className="px-3 py-1.5 text-sm bg-gray-700 hover:bg-gray-600 text-gray-200 rounded transition-colors"
              >
                Resolve
              </button>
            </div>
          )}
        </div>
      ))}
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

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div>
      <label className="block text-xs text-gray-400 mb-1">{label}</label>
      {children}
      {hint && <p className="text-xs text-gray-600 mt-1">{hint}</p>}
    </div>
  )
}

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
  initial?: Partial<AlertTransport>
  onSave: (b: AlertTransportBody) => Promise<void>
  onCancel: () => void
}) {
  const cfg = initial?.config ?? {}
  const isEdit = !!initial?.id

  const [name, setName]       = useState(initial?.name ?? '')
  const [type, setType]       = useState<AlertTransportBody['type']>(initial?.type ?? 'webhook')
  const [enabled, setEnabled] = useState(initial?.enabled ?? true)
  const [saving, setSaving]   = useState(false)
  const [err, setErr]         = useState('')

  // ── Webhook fields ──────────────────────────────────────────────────────────
  const [whUrl,          setWhUrl]          = useState((cfg.url as string)           ?? '')
  const [whMethod,       setWhMethod]       = useState((cfg.method as string)        ?? 'POST')
  const [whBodyTemplate, setWhBodyTemplate] = useState((cfg.body_template as string) ?? '')

  // ── Email fields ────────────────────────────────────────────────────────────
  const [emailTo,   setEmailTo]   = useState(
    Array.isArray(cfg.to) ? (cfg.to as string[]).join(', ') : (cfg.to as string) ?? ''
  )
  const [smtpHost,  setSmtpHost]  = useState((cfg.smtp_host as string)  ?? '')
  const [smtpPort,  setSmtpPort]  = useState((cfg.smtp_port as number)  ?? 587)
  const [smtpTLS,   setSmtpTLS]   = useState((cfg.smtp_tls  as boolean) ?? true)
  const [smtpFrom,  setSmtpFrom]  = useState((cfg.smtp_from as string)  ?? '')
  const [smtpUser,  setSmtpUser]  = useState((cfg.smtp_user as string)  ?? '')
  const [smtpPass,  setSmtpPass]  = useState('') // never echo stored password

  // ── Slack Webhook fields ────────────────────────────────────────────────────
  const [slackWhUrl, setSlackWhUrl] = useState((cfg.url as string) ?? '')

  // ── Slack Bot fields ────────────────────────────────────────────────────────
  const [slackToken,   setSlackToken]   = useState('') // never echo stored token
  const [slackChannel, setSlackChannel] = useState((cfg.channel as string) ?? '')

  function buildConfig(): Record<string, unknown> {
    switch (type) {
      case 'webhook': {
        const c: Record<string, unknown> = { url: whUrl, method: whMethod }
        if (whBodyTemplate) c.body_template = whBodyTemplate
        return c
      }
      case 'email': {
        const c: Record<string, unknown> = {
          smtp_host: smtpHost,
          smtp_port: Number(smtpPort),
          smtp_tls:  smtpTLS,
          smtp_from: smtpFrom,
          to: emailTo.split(',').map((s) => s.trim()).filter(Boolean),
        }
        if (smtpUser) c.smtp_user = smtpUser
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
          // preserve the stored token when editing without re-entering it
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
    <form onSubmit={submit} className="space-y-4">
      {/* Name + Type */}
      <div className="grid grid-cols-2 gap-4">
        <Field label="Name">
          <TInput value={name} onChange={(e) => setName(e.target.value)} required placeholder="e.g. PagerDuty webhook" />
        </Field>
        <Field label="Type">
          <select value={type}
            onChange={(e) => setType(e.target.value as AlertTransportBody['type'])}
            className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-gray-300 focus:outline-none focus:border-blue-500">
            {TRANSPORT_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
          </select>
        </Field>
      </div>

      {/* ── Webhook ──────────────────────────────────────────────────────────── */}
      {type === 'webhook' && (
        <div className="space-y-3 bg-gray-800/40 border border-gray-700 rounded-lg p-4">
          <Field label="URL" hint="The endpoint that will receive the HTTP POST.">
            <TInput value={whUrl} onChange={(e) => setWhUrl(e.target.value)} required
              type="url" placeholder="https://hooks.example.com/…" className="font-mono" />
          </Field>
          <Field label="Method">
            <select value={whMethod} onChange={(e) => setWhMethod(e.target.value)}
              className="w-32 bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-gray-300 focus:outline-none focus:border-blue-500">
              <option>POST</option>
              <option>GET</option>
              <option>PUT</option>
            </select>
          </Field>
          <Field label="Body template (optional)"
            hint='JSON template. Use {{.Message}}, {{.Code}}, {{.Severity}} as placeholders.'>
            <textarea value={whBodyTemplate} onChange={(e) => setWhBodyTemplate(e.target.value)} rows={4}
              placeholder={'{\n  "text": "{{.Message}}",\n  "code": "{{.Code}}"\n}'}
              className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm font-mono text-gray-300 focus:outline-none focus:border-blue-500" />
          </Field>
        </div>
      )}

      {/* ── Email ────────────────────────────────────────────────────────────── */}
      {type === 'email' && (
        <div className="space-y-3 bg-gray-800/40 border border-gray-700 rounded-lg p-4">
          <Field label="Recipients" hint="Comma-separated list of email addresses.">
            <TInput value={emailTo} onChange={(e) => setEmailTo(e.target.value)} required
              placeholder="ops@example.com, alerts@example.com" />
          </Field>
          <div className="grid grid-cols-3 gap-3">
            <div className="col-span-2">
              <Field label="SMTP host">
                <TInput value={smtpHost} onChange={(e) => setSmtpHost(e.target.value)} required
                  placeholder="smtp.example.com" className="font-mono" />
              </Field>
            </div>
            <Field label="Port">
              <TInput value={smtpPort} onChange={(e) => setSmtpPort(Number(e.target.value))}
                type="number" min={1} max={65535} />
            </Field>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label="From address">
              <TInput value={smtpFrom} onChange={(e) => setSmtpFrom(e.target.value)} required
                type="email" placeholder="alerts@example.com" />
            </Field>
            <Field label="SMTP username (optional)">
              <TInput value={smtpUser} onChange={(e) => setSmtpUser(e.target.value)}
                autoComplete="off" placeholder="smtp-user" />
            </Field>
          </div>
          <Field label={isEdit ? 'SMTP password (leave blank to keep existing)' : 'SMTP password (optional)'}
            hint="Stored encrypted.">
            <TInput value={smtpPass} onChange={(e) => setSmtpPass(e.target.value)}
              type="password" autoComplete="new-password" placeholder={isEdit ? '••••••••' : ''} />
          </Field>
          <label className="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
            <input type="checkbox" checked={smtpTLS} onChange={(e) => setSmtpTLS(e.target.checked)}
              className="accent-blue-500" />
            Use TLS (STARTTLS / SMTPS)
          </label>
        </div>
      )}

      {/* ── Slack Incoming Webhook ────────────────────────────────────────────── */}
      {type === 'slack_webhook' && (
        <div className="space-y-3 bg-gray-800/40 border border-gray-700 rounded-lg p-4">
          <Field label="Webhook URL"
            hint="From Slack → Your App → Incoming Webhooks → Add New Webhook.">
            <TInput value={slackWhUrl} onChange={(e) => setSlackWhUrl(e.target.value)} required
              type="url" placeholder="https://hooks.slack.com/services/T00000000/B00000000/…"
              className="font-mono" />
          </Field>
        </div>
      )}

      {/* ── Slack Bot ────────────────────────────────────────────────────────── */}
      {type === 'slack_bot' && (
        <div className="space-y-3 bg-gray-800/40 border border-gray-700 rounded-lg p-4">
          <Field label={isEdit ? 'Bot token (leave blank to keep existing)' : 'Bot token'}
            hint="Requires chat:write scope. Stored encrypted.">
            <TInput value={slackToken} onChange={(e) => setSlackToken(e.target.value)}
              type="password" autoComplete="new-password"
              placeholder={isEdit ? '••••••••' : 'xoxb-…'}
              required={!isEdit} className="font-mono" />
          </Field>
          <Field label="Channel" hint='e.g. #alerts or a channel ID like C01234ABCDE.'>
            <TInput value={slackChannel} onChange={(e) => setSlackChannel(e.target.value)} required
              placeholder="#alerts" />
          </Field>
        </div>
      )}

      <label className="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)}
          className="accent-blue-500" />
        Enabled
      </label>
      {err && <p className="text-red-400 text-sm">{err}</p>}
      <div className="flex gap-2">
        <button type="submit" disabled={saving}
          className="px-4 py-1.5 text-sm bg-blue-600 hover:bg-blue-500 disabled:opacity-50 text-white rounded transition-colors">
          {saving ? 'Saving…' : 'Save'}
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
    try { await alertsApi.testTransport(id); alert('Test message sent!') }
    catch { alert('Test failed — check conductor logs.') }
    finally { setTesting(null) }
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
        <div className="bg-gray-900 border border-gray-700 rounded-lg p-4">
          <h3 className="text-sm font-medium text-white mb-4">New Transport</h3>
          <TransportForm onSave={(b) => save(b)} onCancel={() => setAdding(false)} />
        </div>
      )}
      {loading ? <p className="text-gray-500 text-sm py-4 text-center">Loading…</p> : (
        <div className="space-y-2">
          {transports.map((t) => (
            <div key={t.id}>
              {editing === t.id ? (
                <div className="bg-gray-900 border border-gray-700 rounded-lg p-4">
                  <TransportForm initial={t} onSave={(b) => save(b, t.id)} onCancel={() => setEditing(null)} />
                </div>
              ) : (
                <div className="bg-gray-900 border border-gray-700 rounded-lg px-4 py-3 flex items-center justify-between gap-4">
                  <div>
                    <div className="flex items-center gap-2">
                      <span className="text-white text-sm font-medium">{t.name}</span>
                      <span className="text-xs bg-gray-800 text-gray-400 px-1.5 py-0.5 rounded">{t.type}</span>
                      {!t.enabled && <span className="text-xs text-gray-600">disabled</span>}
                    </div>
                  </div>
                  <div className="flex gap-2">
                    <button onClick={() => test(t.id)} disabled={testing === t.id}
                      className="px-2.5 py-1 text-xs bg-gray-800 hover:bg-gray-700 border border-gray-700 text-gray-300 rounded transition-colors">
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
const FIRE_MODES = ['once', 're_alert', 'every_occurrence'] as const

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
  const [name,          setName]          = useState(initial?.name ?? '')
  const [description,   setDescription]   = useState(initial?.description ?? '')
  const [enabled,       setEnabled]       = useState(initial?.enabled ?? true)
  const [categories,    setCategories]    = useState<string[]>(initial?.categories ?? [])
  const [codes,         setCodes]         = useState((initial?.codes ?? []).join(', '))
  const [minSeverity,   setMinSeverity]   = useState(initial?.min_severity ?? 'warn')
  const [msgRegex,      setMsgRegex]      = useState(initial?.message_regex ?? '')
  const [fireMode,      setFireMode]      = useState<AlertRuleBody['fire_mode']>(initial?.fire_mode ?? 'once')
  const [reAlertMins,   setReAlertMins]   = useState(String(initial?.re_alert_mins ?? ''))
  const [maxReAlerts,   setMaxReAlerts]   = useState(String(initial?.max_re_alerts ?? ''))
  const [notifyOnClear, setNotifyOnClear] = useState(initial?.notify_on_clear ?? true)
  const [scheduleId,    setScheduleId]    = useState(initial?.schedule_id ?? '')
  const [selectedTransports, setSelectedTransports] = useState<string[]>(initial?.transport_ids ?? [])
  const [saving, setSaving] = useState(false)
  const [err,    setErr]    = useState('')

  // ── Scope ─────────────────────────────────────────────────────────────────
  const [clusterId, setClusterId] = useState(initial?.cluster_id ?? '')
  const [nodeId,    setNodeId]    = useState(initial?.node_id ?? '')
  const [clusters,  setClusters]  = useState<Cluster[]>([])
  const [nodes,     setNodes]     = useState<Node[]>([])

  // ── Metric threshold ──────────────────────────────────────────────────────
  const [metricField,    setMetricField]    = useState(initial?.metric_field ?? '')
  const [metricOperator, setMetricOperator] = useState(initial?.metric_operator ?? '>')
  const [metricValue,    setMetricValue]    = useState(
    initial?.metric_value != null ? String(initial.metric_value) : ''
  )

  // ── Escalation ────────────────────────────────────────────────────────────
  const [escalateAfterMins,   setEscalateAfterMins]   = useState(
    initial?.escalate_after_mins != null ? String(initial.escalate_after_mins) : ''
  )
  const [escalateTransportId, setEscalateTransportId] = useState(initial?.escalate_transport_id ?? '')

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

  const sel = 'w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-gray-300 focus:outline-none focus:border-blue-500'
  const inp = 'w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-white focus:outline-none focus:border-blue-500'

  return (
    <form onSubmit={submit} className="space-y-5">
      {/* ── Basic ─────────────────────────────────────────────────────────── */}
      <div className="grid grid-cols-2 gap-4">
        <div>
          <label className="block text-xs text-gray-400 mb-1">Name</label>
          <input value={name} onChange={(e) => setName(e.target.value)} required className={inp} />
        </div>
        <div>
          <label className="block text-xs text-gray-400 mb-1">Min severity</label>
          <select value={minSeverity} onChange={(e) => setMinSeverity(e.target.value)} className={sel}>
            {SEVERITIES.map((s) => <option key={s} value={s}>{s}</option>)}
          </select>
        </div>
      </div>
      <div>
        <label className="block text-xs text-gray-400 mb-1">Description</label>
        <input value={description} onChange={(e) => setDescription(e.target.value)} className={inp} />
      </div>

      {/* ── Match conditions ──────────────────────────────────────────────── */}
      <div>
        <label className="block text-xs text-gray-400 mb-2">Categories (empty = all)</label>
        <div className="flex flex-wrap gap-2">
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
      </div>
      <div className="grid grid-cols-2 gap-4">
        <div>
          <label className="block text-xs text-gray-400 mb-1">Event codes (comma-sep, prefix ok)</label>
          <input value={codes} onChange={(e) => setCodes(e.target.value)}
            placeholder="NBC-HA, NBC-SVC-002" className={inp} />
        </div>
        <div>
          <label className="block text-xs text-gray-400 mb-1">Message regex (optional)</label>
          <input value={msgRegex} onChange={(e) => setMsgRegex(e.target.value)}
            placeholder="stopped|failed" className={inp} />
        </div>
      </div>

      {/* ── Scope ─────────────────────────────────────────────────────────── */}
      <div className="bg-gray-800/40 border border-gray-700 rounded-lg p-4 space-y-3">
        <p className="text-xs font-medium text-gray-400">Scope — optional, leave blank to match any cluster / node</p>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="block text-xs text-gray-400 mb-1">Cluster</label>
            <select value={clusterId}
              onChange={(e) => { setClusterId(e.target.value); setNodeId('') }}
              className={sel}>
              <option value="">Any cluster</option>
              {clusters.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
            </select>
          </div>
          <div>
            <label className="block text-xs text-gray-400 mb-1">Node</label>
            <select value={nodeId} onChange={(e) => setNodeId(e.target.value)}
              disabled={!clusterId} className={sel}>
              <option value="">Any node</option>
              {nodes.map((n) => <option key={n.id} value={n.id}>{n.hostname}</option>)}
            </select>
          </div>
        </div>
      </div>

      {/* ── Metric threshold ──────────────────────────────────────────────── */}
      <div className="bg-gray-800/40 border border-gray-700 rounded-lg p-4 space-y-3">
        <p className="text-xs font-medium text-gray-400">Metric threshold — optional, evaluates against latest heartbeat</p>
        <div className="grid grid-cols-3 gap-3">
          <div>
            <label className="block text-xs text-gray-400 mb-1">Metric field</label>
            <select value={metricField} onChange={(e) => setMetricField(e.target.value)} className={sel}>
              <option value="">None</option>
              {METRIC_FIELDS.map((f) => <option key={f.value} value={f.value}>{f.label}</option>)}
            </select>
          </div>
          {metricField && (
            <>
              <div>
                <label className="block text-xs text-gray-400 mb-1">Operator</label>
                <select value={metricOperator} onChange={(e) => setMetricOperator(e.target.value)} className={sel}>
                  {METRIC_OPERATORS.map((op) => <option key={op} value={op}>{op}</option>)}
                </select>
              </div>
              <div>
                <label className="block text-xs text-gray-400 mb-1">Threshold</label>
                <input type="number" step="any" required value={metricValue}
                  onChange={(e) => setMetricValue(e.target.value)} className={inp} />
              </div>
            </>
          )}
        </div>
        {metricField && !nodeId && (
          <p className="text-amber-400 text-xs">
            ⚠ Metric rules require a specific node — select a cluster and node in the Scope section above.
          </p>
        )}
      </div>

      {/* ── Fire behavior ─────────────────────────────────────────────────── */}
      <div className="grid grid-cols-3 gap-4">
        <div>
          <label className="block text-xs text-gray-400 mb-1">Fire mode</label>
          <select value={fireMode} onChange={(e) => setFireMode(e.target.value as AlertRuleBody['fire_mode'])} className={sel}>
            {FIRE_MODES.map((m) => <option key={m} value={m}>{m}</option>)}
          </select>
        </div>
        {fireMode === 're_alert' && (
          <>
            <div>
              <label className="block text-xs text-gray-400 mb-1">Re-alert every (mins)</label>
              <input type="number" min={1} required value={reAlertMins}
                onChange={(e) => setReAlertMins(e.target.value)} className={inp} />
            </div>
            <div>
              <label className="block text-xs text-gray-400 mb-1">Max re-alerts (blank = unlimited)</label>
              <input type="number" min={1} value={maxReAlerts}
                onChange={(e) => setMaxReAlerts(e.target.value)} className={inp} />
            </div>
          </>
        )}
      </div>
      {fireMode === 'every_occurrence' && (
        <p className="text-gray-500 text-xs -mt-2">
          Each matching event fires and delivers immediately. No active state is tracked — acknowledge and resolve do not apply.
        </p>
      )}

      {/* ── Escalation ────────────────────────────────────────────────────── */}
      {fireMode !== 'every_occurrence' && (
        <div className="bg-gray-800/40 border border-gray-700 rounded-lg p-4 space-y-3">
          <p className="text-xs font-medium text-gray-400">Escalation — optional, fires a second transport if the alert stays active</p>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs text-gray-400 mb-1">Escalate after (mins)</label>
              <input type="number" min={1} value={escalateAfterMins}
                onChange={(e) => setEscalateAfterMins(e.target.value)}
                placeholder="e.g. 30" className={inp} />
            </div>
            <div>
              <label className="block text-xs text-gray-400 mb-1">Escalate to transport</label>
              <select value={escalateTransportId} onChange={(e) => setEscalateTransportId(e.target.value)}
                disabled={!escalateAfterMins} className={sel}>
                <option value="">None</option>
                {transports.filter((t) => t.enabled).map((t) => (
                  <option key={t.id} value={t.id}>{t.name}</option>
                ))}
              </select>
            </div>
          </div>
        </div>
      )}

      {/* ── Options ───────────────────────────────────────────────────────── */}
      <div className="flex gap-6">
        <label className="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)}
            className="accent-blue-500" />
          Enabled
        </label>
        <label className="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
          <input type="checkbox" checked={notifyOnClear} onChange={(e) => setNotifyOnClear(e.target.checked)}
            className="accent-blue-500" />
          Notify on clear
        </label>
      </div>

      {schedules.length > 0 && (
        <div>
          <label className="block text-xs text-gray-400 mb-1">Schedule (optional)</label>
          <select value={scheduleId} onChange={(e) => setScheduleId(e.target.value)} className={sel}>
            <option value="">Always active</option>
            {schedules.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
          </select>
        </div>
      )}

      <div>
        <label className="block text-xs text-gray-400 mb-2">Transports</label>
        {transports.length === 0 ? (
          <p className="text-gray-600 text-xs">No transports configured yet.</p>
        ) : (
          <div className="flex flex-wrap gap-2">
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
      </div>

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
                      <span className="text-xs bg-gray-800 text-gray-400 px-1.5 py-0.5 rounded">{r.fire_mode}</span>
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
  'America/New_York',
  'America/Chicago',
  'America/Denver',
  'America/Los_Angeles',
  'America/Phoenix',
  'America/Anchorage',
  'America/Honolulu',
  'America/Toronto',
  'America/Vancouver',
  'America/Sao_Paulo',
  'America/Argentina/Buenos_Aires',
  'America/Mexico_City',
  'Europe/London',
  'Europe/Dublin',
  'Europe/Lisbon',
  'Europe/Paris',
  'Europe/Berlin',
  'Europe/Amsterdam',
  'Europe/Brussels',
  'Europe/Zurich',
  'Europe/Rome',
  'Europe/Madrid',
  'Europe/Stockholm',
  'Europe/Oslo',
  'Europe/Helsinki',
  'Europe/Warsaw',
  'Europe/Prague',
  'Europe/Budapest',
  'Europe/Bucharest',
  'Europe/Athens',
  'Europe/Istanbul',
  'Europe/Moscow',
  'Asia/Dubai',
  'Asia/Kolkata',
  'Asia/Dhaka',
  'Asia/Colombo',
  'Asia/Bangkok',
  'Asia/Jakarta',
  'Asia/Singapore',
  'Asia/Kuala_Lumpur',
  'Asia/Shanghai',
  'Asia/Hong_Kong',
  'Asia/Taipei',
  'Asia/Seoul',
  'Asia/Tokyo',
  'Asia/Manila',
  'Asia/Karachi',
  'Asia/Tashkent',
  'Asia/Almaty',
  'Asia/Yekaterinburg',
  'Asia/Novosibirsk',
  'Asia/Vladivostok',
  'Australia/Perth',
  'Australia/Darwin',
  'Australia/Adelaide',
  'Australia/Brisbane',
  'Australia/Sydney',
  'Australia/Melbourne',
  'Pacific/Auckland',
  'Pacific/Fiji',
  'Pacific/Honolulu',
  'Africa/Cairo',
  'Africa/Johannesburg',
  'Africa/Lagos',
  'Africa/Nairobi',
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

  function addWindow() {
    setWindows((w) => [...w, { days: [1,2,3,4,5], start: '09:00', end: '17:00' }])
  }

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
              <input value={name} onChange={(e) => setName(e.target.value)} required
                className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-white focus:outline-none focus:border-blue-500" />
            </div>
            <div>
              <label className="block text-xs text-gray-400 mb-1">Timezone</label>
              <select value={timezone} onChange={(e) => setTimezone(e.target.value)}
                className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-white focus:outline-none focus:border-blue-500">
                {TIMEZONES.map((tz) => <option key={tz} value={tz}>{tz}</option>)}
              </select>
            </div>
          </div>
          <div className="space-y-3">
            <div className="flex items-center justify-between">
              <span className="text-xs text-gray-400">Time windows</span>
              <button type="button" onClick={addWindow}
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
          <TabBtn label="Active Alerts" active={tab === 'active'} onClick={() => setTab('active')} />
          <TabBtn label="Rules" active={tab === 'rules'} onClick={() => setTab('rules')} />
          <TabBtn label="Transports" active={tab === 'transports'} onClick={() => setTab('transports')} />
          <TabBtn label="Schedules" active={tab === 'schedules'} onClick={() => setTab('schedules')} />
        </div>

        {tab === 'active' && <ActiveTab />}
        {tab === 'rules' && <RulesTab />}
        {tab === 'transports' && <TransportsTab />}
        {tab === 'schedules' && <SchedulesTab />}
      </div>
    </Layout>
  )
}
