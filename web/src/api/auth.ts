import client from './client'

export interface LoginResponse {
  // Full token response (TOTP disabled)
  access_token?: string
  refresh_token?: string
  expires_in?: number
  // TOTP pending (TOTP enabled)
  requires_totp?: boolean
  totp_token?: string
}

export interface MeResponse {
  id: string
  username: string
  role: string
}

export const authApi = {
  login: (username: string, password: string) =>
    client.post<LoginResponse>('/auth/login', { username, password }),

  refresh: (refresh_token: string) =>
    client.post<{ access_token: string; expires_in: number }>('/auth/refresh', { refresh_token }),

  logout: (refresh_token: string) =>
    client.post('/auth/logout', { refresh_token }),

  me: () =>
    client.get<MeResponse>('/auth/me'),

  // TOTP
  verifyTOTP: (totp_token: string, code: string) =>
    client.post<LoginResponse>('/auth/totp/verify', { totp_token, code }),

  totpStatus: () =>
    client.get<{ totp_enabled: boolean }>('/auth/totp/status'),

  enrollTOTP: () =>
    client.post<{ qr_uri: string; secret: string; enrollment_token: string }>('/auth/totp/enroll', {}),

  confirmTOTP: (enrollment_token: string, code: string) =>
    client.post<{ totp_enabled: boolean }>('/auth/totp/confirm', { enrollment_token, code }),

  disableTOTP: (password: string) =>
    client.post<{ totp_enabled: boolean }>('/auth/totp/disable', { password }),
}
