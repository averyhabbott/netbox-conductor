import client from './client'

// ─── Transports ───────────────────────────────────────────────────────────────

export interface AlertTransport {
  id: string
  name: string
  type: 'webhook' | 'email' | 'slack_webhook' | 'slack_bot'
  config: Record<string, unknown>
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface AlertTransportBody {
  name: string
  type: 'webhook' | 'email' | 'slack_webhook' | 'slack_bot'
  config: Record<string, unknown>
  enabled?: boolean
}

// ─── Schedules ────────────────────────────────────────────────────────────────

export interface ScheduleWindow {
  days: number[]   // 0=Sun … 6=Sat
  start: string    // "09:00"
  end: string      // "17:00"
}

export interface AlertSchedule {
  id: string
  name: string
  timezone: string
  windows: ScheduleWindow[]
  created_at: string
  updated_at: string
}

export interface AlertScheduleBody {
  name: string
  timezone?: string
  windows?: ScheduleWindow[]
}

// ─── Rules ────────────────────────────────────────────────────────────────────

export interface AlertRule {
  id: string
  name: string
  description: string
  enabled: boolean
  categories: string[]
  codes: string[]
  min_severity: string
  message_regex?: string
  metric_field?: string
  metric_operator?: string
  metric_value?: number
  cluster_id?: string
  node_id?: string
  fire_mode: 'once' | 're_alert' | 'every_occurrence'
  re_alert_mins?: number
  max_re_alerts?: number
  notify_on_clear: boolean
  escalate_after_mins?: number
  escalate_transport_id?: string
  schedule_id?: string
  transport_ids: string[]
  created_at: string
  updated_at: string
}

export interface AlertRuleBody {
  name: string
  description?: string
  enabled?: boolean
  categories?: string[]
  codes?: string[]
  min_severity?: string
  message_regex?: string
  metric_field?: string
  metric_operator?: string
  metric_value?: number
  cluster_id?: string | null
  node_id?: string | null
  fire_mode?: 'once' | 're_alert' | 'every_occurrence'
  re_alert_mins?: number | null
  max_re_alerts?: number | null
  notify_on_clear?: boolean
  escalate_after_mins?: number | null
  escalate_transport_id?: string | null
  schedule_id?: string | null
  transport_ids?: string[]
}

// ─── Active alert states ──────────────────────────────────────────────────────

export interface ActiveAlertState {
  id: string
  rule_id: string
  cluster_id?: string
  cluster_name?: string
  node_id?: string
  node_name?: string
  state: 'active' | 'resolved' | 'acknowledged'
  re_alert_count: number
  escalated: boolean
  first_fired_at: string
  last_fired_at: string
  last_alerted_at?: string
  resolved_at?: string
  acknowledged_at?: string
  acknowledged_by?: string
  rule_name?: string
}

// ─── Alert fire log ───────────────────────────────────────────────────────────

export interface AlertFireLog {
  id: string
  rule_id?: string
  rule_name: string
  transport_id?: string
  transport_name: string
  transport_type: string
  cluster_id?: string
  cluster_name?: string
  node_id?: string
  node_name?: string
  event_code: string
  event_message: string
  event_severity: string
  is_resolve: boolean
  fired_at: string
}

// ─── System logs ──────────────────────────────────────────────────────────────

export interface SystemLogsResponse {
  lines: string[]
}

// ─── API ──────────────────────────────────────────────────────────────────────

export const alertsApi = {
  // Rules
  listRules: () =>
    client.get<AlertRule[]>('/alerts/rules').then((r) => r.data),
  createRule: (body: AlertRuleBody) =>
    client.post<AlertRule>('/alerts/rules', body).then((r) => r.data),
  updateRule: (id: string, body: AlertRuleBody) =>
    client.put<AlertRule>(`/alerts/rules/${id}`, body).then((r) => r.data),
  deleteRule: (id: string) =>
    client.delete(`/alerts/rules/${id}`),

  // Transports
  listTransports: () =>
    client.get<AlertTransport[]>('/alerts/transports').then((r) => r.data),
  createTransport: (body: AlertTransportBody) =>
    client.post<AlertTransport>('/alerts/transports', body).then((r) => r.data),
  updateTransport: (id: string, body: AlertTransportBody) =>
    client.put<AlertTransport>(`/alerts/transports/${id}`, body).then((r) => r.data),
  deleteTransport: (id: string) =>
    client.delete(`/alerts/transports/${id}`),
  testTransport: (id: string) =>
    client.post(`/alerts/transports/${id}/test`),

  // Schedules
  listSchedules: () =>
    client.get<AlertSchedule[]>('/alerts/schedules').then((r) => r.data),
  createSchedule: (body: AlertScheduleBody) =>
    client.post<AlertSchedule>('/alerts/schedules', body).then((r) => r.data),
  updateSchedule: (id: string, body: AlertScheduleBody) =>
    client.put<AlertSchedule>(`/alerts/schedules/${id}`, body).then((r) => r.data),
  deleteSchedule: (id: string) =>
    client.delete(`/alerts/schedules/${id}`),

  // Active alerts
  listActive: () =>
    client.get<ActiveAlertState[]>('/alerts/active').then((r) => r.data),
  acknowledge: (id: string) =>
    client.post(`/alerts/active/${id}/acknowledge`),
  resolve: (id: string) =>
    client.post(`/alerts/active/${id}/resolve`),

  // Alert history
  listHistory: () =>
    client.get<AlertFireLog[]>('/alerts/history').then((r) => r.data),

  // System logs
  systemLogs: (lines = 200) =>
    client.get<SystemLogsResponse>('/system/logs', { params: { lines } }).then((r) => r.data),
}
