import client from './client'

export interface LoginResponse {
  access_token: string
  refresh_token: string
  expires_in: number
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
}
