import axios from 'axios'
import { useAuthStore } from '../store/auth'

const client = axios.create({
  baseURL: '/api/v1',
  headers: { 'Content-Type': 'application/json' },
})

// Attach access token to every request
client.interceptors.request.use((config) => {
  const token = useAuthStore.getState().accessToken
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

// On 401, attempt token refresh once then redirect to login
client.interceptors.response.use(
  (res) => res,
  async (err) => {
    const original = err.config
    // A 401 from the refresh call itself must not trigger another refresh (infinite loop).
    if (err.response?.status === 401 && !original._retry && original.url !== '/auth/refresh') {
      original._retry = true
      const refreshed = await useAuthStore.getState().refresh()
      if (refreshed) {
        original.headers.Authorization = `Bearer ${useAuthStore.getState().accessToken}`
        return client(original)
      }
      useAuthStore.getState().logout()
      window.location.href = '/login'
    }
    return Promise.reject(err)
  }
)

// Returns the server's error message from a failed API call, else the fallback.
export function apiError(e: unknown, fallback: string): string
export function apiError(e: unknown): string | undefined
export function apiError(e: unknown, fallback?: string) {
  return (e as { response?: { data?: { message?: string } } } | null)?.response?.data?.message ?? fallback
}

export default client
