import client from './client'

export interface UserItem {
  id: string
  username: string
  role: string
  created_at: string
  last_login_at: string | null
}

export interface TLSCertInfo {
  subject: string
  not_before: string
  not_after: string
  dns_names: string[]
  ip_addresses: string[]
  fingerprint: string
}

export interface TLSInfoResponse {
  enabled: boolean
  cert_info?: TLSCertInfo
}

export const usersApi = {
  list: () =>
    client.get<UserItem[]>('/users'),

  create: (username: string, password: string, role: string) =>
    client.post<UserItem>('/users', { username, password, role }),

  updateRole: (id: string, role: string) =>
    client.patch(`/users/${id}/role`, { role }),

  delete: (id: string) =>
    client.delete(`/users/${id}`),

  changePassword: (currentPassword: string, newPassword: string) =>
    client.post('/auth/change-password', {
      current_password: currentPassword,
      new_password: newPassword,
    }),

  getTLSInfo: () =>
    client.get<TLSInfoResponse>('/settings/tls'),
}
