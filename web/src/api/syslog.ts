import client from './client'

export interface SyslogDestination {
  id: string
  name: string
  protocol: 'udp' | 'tcp' | 'tcp+tls'
  host: string
  port: number
  tls_ca_cert?: string
  categories: string[]
  min_severity: string
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface SyslogDestinationBody {
  name: string
  protocol: 'udp' | 'tcp' | 'tcp+tls'
  host: string
  port?: number
  tls_ca_cert?: string
  categories?: string[]
  min_severity?: string
  enabled?: boolean
}

export interface EventRetention {
  category: string
  retain_days: number
  updated_at: string
}

export const syslogApi = {
  list: () =>
    client.get<SyslogDestination[]>('/syslog/destinations').then((r) => r.data),

  create: (body: SyslogDestinationBody) =>
    client.post<SyslogDestination>('/syslog/destinations', body).then((r) => r.data),

  update: (id: string, body: SyslogDestinationBody) =>
    client.put<SyslogDestination>(`/syslog/destinations/${id}`, body).then((r) => r.data),

  delete: (id: string) =>
    client.delete(`/syslog/destinations/${id}`),
}

export const retentionApi = {
  list: () =>
    client.get<EventRetention[]>('/settings/retention').then((r) => r.data),

  update: (category: string, retain_days: number) =>
    client.put<EventRetention>('/settings/retention', { category, retain_days }).then((r) => r.data),
}
