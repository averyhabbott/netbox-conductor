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
    if (err.response?.status === 401 && !original._retry) {
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

export default client
