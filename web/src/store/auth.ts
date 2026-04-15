import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import { authApi } from '../api/auth'

interface AuthState {
  accessToken: string | null
  refreshToken: string | null
  user: { id: string; username: string; role: string } | null

  login: (username: string, password: string) => Promise<{ requiresTOTP: boolean; totpToken?: string }>
  verifyTOTP: (totpToken: string, code: string) => Promise<void>
  refresh: () => Promise<boolean>
  logout: () => void
  fetchMe: () => Promise<void>
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set, get) => ({
      accessToken: null,
      refreshToken: null,
      user: null,

      login: async (username, password) => {
        const { data } = await authApi.login(username, password)
        if (data.requires_totp) {
          return { requiresTOTP: true, totpToken: data.totp_token }
        }
        set({ accessToken: data.access_token, refreshToken: data.refresh_token })
        await get().fetchMe()
        return { requiresTOTP: false }
      },

      verifyTOTP: async (totpToken, code) => {
        const { data } = await authApi.verifyTOTP(totpToken, code)
        set({ accessToken: data.access_token, refreshToken: data.refresh_token })
        await get().fetchMe()
      },

      refresh: async () => {
        const rt = get().refreshToken
        if (!rt) return false
        try {
          const { data } = await authApi.refresh(rt)
          set({ accessToken: data.access_token })
          return true
        } catch {
          set({ accessToken: null, refreshToken: null, user: null })
          return false
        }
      },

      logout: async () => {
        const rt = get().refreshToken
        if (rt) {
          try { await authApi.logout(rt) } catch { /* best effort */ }
        }
        set({ accessToken: null, refreshToken: null, user: null })
      },

      fetchMe: async () => {
        const { data } = await authApi.me()
        set({ user: data })
      },
    }),
    {
      name: 'auth',
      partialize: (state) => ({
        accessToken: state.accessToken,
        refreshToken: state.refreshToken,
        user: state.user,
      }),
    }
  )
)
