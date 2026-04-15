import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import Layout from '../components/Layout'
import { useAuthStore } from '../store/auth'
import { usersApi, type UserItem } from '../api/users'
import { authApi } from '../api/auth'

type Tab = 'users' | 'system' | 'password' | 'totp'

export default function Settings() {
  const [tab, setTab] = useState<Tab>('users')
  const user = useAuthStore((s) => s.user)
  const isAdmin = user?.role === 'admin'

  return (
    <Layout>
      <div className="mb-6">
        <h1 className="text-2xl font-semibold">Settings</h1>
      </div>

      <div className="flex gap-1 mb-6 border-b border-gray-800">
        {isAdmin && (
          <TabBtn active={tab === 'users'} onClick={() => setTab('users')}>
            Users
          </TabBtn>
        )}
        <TabBtn active={tab === 'system'} onClick={() => setTab('system')}>
          System
        </TabBtn>
        <TabBtn active={tab === 'password'} onClick={() => setTab('password')}>
          Change Password
        </TabBtn>
        <TabBtn active={tab === 'totp'} onClick={() => setTab('totp')}>
          Two-Factor Auth
        </TabBtn>
      </div>

      {tab === 'users' && isAdmin && <UsersTab currentUserId={user?.id ?? ''} />}
      {tab === 'system' && <SystemTab />}
      {tab === 'password' && <ChangePasswordTab />}
      {tab === 'totp' && <TOTPTab />}
    </Layout>
  )
}

function TabBtn({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      onClick={onClick}
      className={`px-4 py-2 text-sm font-medium border-b-2 transition-colors ${
        active
          ? 'border-blue-500 text-white'
          : 'border-transparent text-gray-400 hover:text-white'
      }`}
    >
      {children}
    </button>
  )
}

// ── Users Tab ────────────────────────────────────────────────────────────────

function UsersTab({ currentUserId }: { currentUserId: string }) {
  const qc = useQueryClient()
  const [showCreate, setShowCreate] = useState(false)
  const [newUsername, setNewUsername] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [newRole, setNewRole] = useState('operator')
  const [createError, setCreateError] = useState('')

  const { data: users = [], isLoading } = useQuery({
    queryKey: ['users'],
    queryFn: () => usersApi.list().then((r) => r.data),
  })

  const createMutation = useMutation({
    mutationFn: () => usersApi.create(newUsername, newPassword, newRole),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['users'] })
      setShowCreate(false)
      setNewUsername('')
      setNewPassword('')
      setNewRole('operator')
      setCreateError('')
    },
    onError: (err: any) => {
      setCreateError(err?.response?.data?.message ?? 'Failed to create user')
    },
  })

  const updateRoleMutation = useMutation({
    mutationFn: ({ id, role }: { id: string; role: string }) =>
      usersApi.updateRole(id, role),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['users'] }),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => usersApi.delete(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['users'] }),
  })

  if (isLoading) {
    return <p className="text-gray-400 text-sm">Loading users…</p>
  }

  return (
    <div className="space-y-4">
      <div className="flex justify-between items-center">
        <h2 className="text-lg font-medium">User Accounts</h2>
        <button
          onClick={() => setShowCreate((v) => !v)}
          className="px-3 py-1.5 text-sm bg-blue-600 hover:bg-blue-500 rounded transition-colors"
        >
          + New User
        </button>
      </div>

      {showCreate && (
        <div className="bg-gray-900 border border-gray-700 rounded-lg p-4 space-y-3">
          <h3 className="text-sm font-medium">Create User</h3>
          {createError && (
            <p className="text-red-400 text-sm">{createError}</p>
          )}
          <div className="grid grid-cols-3 gap-3">
            <div>
              <label className="block text-xs text-gray-400 mb-1">Username</label>
              <input
                className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm focus:outline-none focus:border-blue-500"
                value={newUsername}
                onChange={(e) => setNewUsername(e.target.value)}
                placeholder="username"
              />
            </div>
            <div>
              <label className="block text-xs text-gray-400 mb-1">Password</label>
              <input
                type="password"
                className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm focus:outline-none focus:border-blue-500"
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
                placeholder="password"
              />
            </div>
            <div>
              <label className="block text-xs text-gray-400 mb-1">Role</label>
              <RoleSelect value={newRole} onChange={setNewRole} />
            </div>
          </div>
          <div className="flex gap-2 justify-end">
            <button
              onClick={() => setShowCreate(false)}
              className="px-3 py-1.5 text-sm text-gray-400 hover:text-white transition-colors"
            >
              Cancel
            </button>
            <button
              onClick={() => createMutation.mutate()}
              disabled={createMutation.isPending || !newUsername || !newPassword}
              className="px-3 py-1.5 text-sm bg-blue-600 hover:bg-blue-500 disabled:opacity-50 rounded transition-colors"
            >
              {createMutation.isPending ? 'Creating…' : 'Create'}
            </button>
          </div>
        </div>
      )}

      <div className="bg-gray-900 border border-gray-800 rounded-lg overflow-hidden">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-gray-800 text-gray-400 text-xs uppercase">
              <th className="text-left px-4 py-3">Username</th>
              <th className="text-left px-4 py-3">Role</th>
              <th className="text-left px-4 py-3">Created</th>
              <th className="text-left px-4 py-3">Last Login</th>
              <th className="px-4 py-3" />
            </tr>
          </thead>
          <tbody>
            {users.map((u: UserItem) => (
              <UserRow
                key={u.id}
                user={u}
                isSelf={u.id === currentUserId}
                onRoleChange={(role) =>
                  updateRoleMutation.mutate({ id: u.id, role })
                }
                onDelete={() => deleteMutation.mutate(u.id)}
              />
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function UserRow({
  user,
  isSelf,
  onRoleChange,
  onDelete,
}: {
  user: UserItem
  isSelf: boolean
  onRoleChange: (role: string) => void
  onDelete: () => void
}) {
  const [confirmDelete, setConfirmDelete] = useState(false)

  return (
    <tr className="border-b border-gray-800 last:border-0 hover:bg-gray-800/30">
      <td className="px-4 py-3 font-medium">
        {user.username}
        {isSelf && (
          <span className="ml-2 text-xs text-blue-400">(you)</span>
        )}
      </td>
      <td className="px-4 py-3">
        {isSelf ? (
          <span className="text-xs bg-gray-800 px-2 py-0.5 rounded">{user.role}</span>
        ) : (
          <RoleSelect value={user.role} onChange={onRoleChange} compact />
        )}
      </td>
      <td className="px-4 py-3 text-gray-400 text-xs">
        {new Date(user.created_at).toLocaleDateString()}
      </td>
      <td className="px-4 py-3 text-gray-400 text-xs">
        {user.last_login_at
          ? new Date(user.last_login_at).toLocaleString()
          : '—'}
      </td>
      <td className="px-4 py-3 text-right">
        {!isSelf &&
          (confirmDelete ? (
            <span className="flex items-center gap-2 justify-end">
              <span className="text-xs text-gray-400">Sure?</span>
              <button
                onClick={onDelete}
                className="text-xs text-red-400 hover:text-red-300"
              >
                Delete
              </button>
              <button
                onClick={() => setConfirmDelete(false)}
                className="text-xs text-gray-400 hover:text-white"
              >
                Cancel
              </button>
            </span>
          ) : (
            <button
              onClick={() => setConfirmDelete(true)}
              className="text-xs text-gray-500 hover:text-red-400 transition-colors"
            >
              Delete
            </button>
          ))}
      </td>
    </tr>
  )
}

function RoleSelect({
  value,
  onChange,
  compact,
}: {
  value: string
  onChange: (role: string) => void
  compact?: boolean
}) {
  const colorMap: Record<string, string> = {
    admin: 'text-red-400',
    operator: 'text-blue-400',
    viewer: 'text-gray-400',
  }
  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className={`bg-gray-800 border border-gray-700 rounded px-2 py-0.5 text-xs focus:outline-none focus:border-blue-500 ${colorMap[value] ?? ''} ${compact ? '' : 'w-full'}`}
    >
      <option value="admin">admin</option>
      <option value="operator">operator</option>
      <option value="viewer">viewer</option>
    </select>
  )
}

// ── System Tab ───────────────────────────────────────────────────────────────

function SystemTab() {
  const { data, isLoading } = useQuery({
    queryKey: ['settings', 'tls'],
    queryFn: () => usersApi.getTLSInfo().then((r) => r.data),
  })

  const daysUntilExpiry = data?.cert_info?.not_after
    ? Math.floor(
        (new Date(data.cert_info.not_after).getTime() - Date.now()) /
          (1000 * 60 * 60 * 24),
      )
    : null

  return (
    <div className="space-y-6">
      <div className="bg-gray-900 border border-gray-800 rounded-lg p-5">
        <h2 className="text-lg font-medium mb-4">TLS Certificate</h2>

        {isLoading && <p className="text-gray-400 text-sm">Loading…</p>}

        {!isLoading && !data?.enabled && (
          <div className="flex items-center gap-2 text-amber-400 text-sm">
            <span className="w-2 h-2 rounded-full bg-amber-400 inline-block" />
            TLS is disabled — server is running over plain HTTP
          </div>
        )}

        {!isLoading && data?.enabled && data.cert_info && (
          <div className="space-y-3">
            <div className="flex items-center gap-2 text-sm">
              <span
                className={`w-2 h-2 rounded-full inline-block ${
                  daysUntilExpiry !== null && daysUntilExpiry < 30
                    ? 'bg-amber-400'
                    : 'bg-green-400'
                }`}
              />
              <span className="text-green-400">TLS enabled</span>
              {daysUntilExpiry !== null && daysUntilExpiry < 30 && (
                <span className="text-amber-400 text-xs">
                  — expires in {daysUntilExpiry} days
                </span>
              )}
            </div>

            <div className="grid grid-cols-2 gap-x-8 gap-y-2 text-sm mt-2">
              <InfoRow label="Subject" value={data.cert_info.subject} />
              <InfoRow
                label="Valid from"
                value={new Date(data.cert_info.not_before).toLocaleDateString()}
              />
              <InfoRow
                label="Expires"
                value={new Date(data.cert_info.not_after).toLocaleDateString()}
              />
              {daysUntilExpiry !== null && (
                <InfoRow
                  label="Days remaining"
                  value={String(daysUntilExpiry)}
                  highlight={daysUntilExpiry < 30 ? 'amber' : 'green'}
                />
              )}
              {data.cert_info.dns_names.length > 0 && (
                <InfoRow
                  label="DNS SANs"
                  value={data.cert_info.dns_names.join(', ')}
                />
              )}
              {data.cert_info.ip_addresses.length > 0 && (
                <InfoRow
                  label="IP SANs"
                  value={data.cert_info.ip_addresses.join(', ')}
                />
              )}
              <div className="col-span-2">
                <span className="text-gray-400 text-xs">Fingerprint (SHA-256)</span>
                <p className="font-mono text-xs text-gray-300 break-all mt-0.5">
                  {data.cert_info.fingerprint}
                </p>
              </div>
            </div>

            <div className="pt-2">
              <a
                href="/api/v1/downloads/ca.crt"
                download="ca.crt"
                className="inline-flex items-center gap-1.5 px-3 py-1.5 text-sm bg-gray-800 hover:bg-gray-700 border border-gray-700 rounded transition-colors"
              >
                Download CA cert
              </a>
              <p className="text-xs text-gray-500 mt-1">
                Install on each agent as <code className="font-mono">AGENT_TLS_CA_CERT</code>
              </p>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function InfoRow({
  label,
  value,
  highlight,
}: {
  label: string
  value: string
  highlight?: 'amber' | 'green'
}) {
  const valueClass =
    highlight === 'amber'
      ? 'text-amber-400'
      : highlight === 'green'
        ? 'text-green-400'
        : 'text-gray-200'
  return (
    <div>
      <span className="text-gray-400 text-xs">{label}</span>
      <p className={`text-sm ${valueClass}`}>{value}</p>
    </div>
  )
}

// ── Change Password Tab ──────────────────────────────────────────────────────

function ChangePasswordTab() {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [success, setSuccess] = useState(false)

  const mutation = useMutation({
    mutationFn: () => usersApi.changePassword(current, next),
    onSuccess: () => {
      setSuccess(true)
      setCurrent('')
      setNext('')
      setConfirm('')
      setError('')
    },
    onError: (err: any) => {
      setError(err?.response?.data?.message ?? 'Failed to change password')
    },
  })

  const canSubmit =
    current.length > 0 &&
    next.length >= 8 &&
    next === confirm &&
    !mutation.isPending

  return (
    <div className="max-w-md">
      <div className="bg-gray-900 border border-gray-800 rounded-lg p-5 space-y-4">
        <h2 className="text-lg font-medium">Change Password</h2>

        {success && (
          <div className="bg-green-900/30 border border-green-700 rounded px-3 py-2 text-sm text-green-300">
            Password changed. All existing sessions have been revoked.
          </div>
        )}
        {error && (
          <div className="bg-red-900/30 border border-red-700 rounded px-3 py-2 text-sm text-red-300">
            {error}
          </div>
        )}

        <div className="space-y-3">
          <div>
            <label className="block text-xs text-gray-400 mb-1">Current password</label>
            <input
              type="password"
              className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
            />
          </div>
          <div>
            <label className="block text-xs text-gray-400 mb-1">New password</label>
            <input
              type="password"
              className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
              value={next}
              onChange={(e) => setNext(e.target.value)}
            />
            {next.length > 0 && next.length < 8 && (
              <p className="text-xs text-red-400 mt-1">Minimum 8 characters</p>
            )}
          </div>
          <div>
            <label className="block text-xs text-gray-400 mb-1">Confirm new password</label>
            <input
              type="password"
              className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
            />
            {confirm.length > 0 && next !== confirm && (
              <p className="text-xs text-red-400 mt-1">Passwords do not match</p>
            )}
          </div>
        </div>

        <button
          onClick={() => mutation.mutate()}
          disabled={!canSubmit}
          className="w-full py-2 text-sm bg-blue-600 hover:bg-blue-500 disabled:opacity-50 rounded transition-colors"
        >
          {mutation.isPending ? 'Updating…' : 'Update Password'}
        </button>
      </div>
    </div>
  )
}

// ── TOTP Tab ─────────────────────────────────────────────────────────────────

function TOTPTab() {
  const qc = useQueryClient()
  const [enrolling, setEnrolling] = useState(false)
  const [qrUri, setQrUri] = useState('')
  const [enrollToken, setEnrollToken] = useState('')
  const [secret, setSecret] = useState('')
  const [code, setCode] = useState('')
  const [disablePassword, setDisablePassword] = useState('')
  const [showDisable, setShowDisable] = useState(false)
  const [msg, setMsg] = useState<{ type: 'success' | 'error'; text: string } | null>(null)

  const { data: status, isLoading } = useQuery({
    queryKey: ['totp-status'],
    queryFn: () => authApi.totpStatus().then((r) => r.data),
  })

  const startEnroll = useMutation({
    mutationFn: () => authApi.enrollTOTP().then((r) => r.data),
    onSuccess: (d) => {
      setQrUri(d.qr_uri)
      setEnrollToken(d.enrollment_token)
      setSecret(d.secret)
      setEnrolling(true)
      setMsg(null)
    },
    onError: () => setMsg({ type: 'error', text: 'Failed to start enrollment' }),
  })

  const confirmEnroll = useMutation({
    mutationFn: () => authApi.confirmTOTP(enrollToken, code).then((r) => r.data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['totp-status'] })
      setEnrolling(false)
      setQrUri('')
      setCode('')
      setMsg({ type: 'success', text: 'Two-factor authentication enabled.' })
    },
    onError: () => setMsg({ type: 'error', text: 'Invalid code — try again' }),
  })

  const disable = useMutation({
    mutationFn: () => authApi.disableTOTP(disablePassword).then((r) => r.data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['totp-status'] })
      setShowDisable(false)
      setDisablePassword('')
      setMsg({ type: 'success', text: 'Two-factor authentication disabled.' })
    },
    onError: () => setMsg({ type: 'error', text: 'Incorrect password' }),
  })

  if (isLoading) return <p className="text-gray-400 text-sm">Loading…</p>

  return (
    <div className="max-w-md space-y-4">
      <div className="bg-gray-900 border border-gray-800 rounded-lg p-5 space-y-4">
        <div className="flex items-center justify-between">
          <h2 className="text-lg font-medium">Two-Factor Authentication</h2>
          <span
            className={`text-xs px-2 py-0.5 rounded-full ${
              status?.totp_enabled
                ? 'bg-green-900/50 text-green-400 border border-green-700'
                : 'bg-gray-800 text-gray-400 border border-gray-700'
            }`}
          >
            {status?.totp_enabled ? 'Enabled' : 'Disabled'}
          </span>
        </div>

        {msg && (
          <div
            className={`px-3 py-2 rounded text-sm border ${
              msg.type === 'success'
                ? 'bg-green-900/30 border-green-700 text-green-300'
                : 'bg-red-900/30 border-red-700 text-red-300'
            }`}
          >
            {msg.text}
          </div>
        )}

        {!status?.totp_enabled && !enrolling && (
          <div className="space-y-3">
            <p className="text-sm text-gray-400">
              Use an authenticator app (e.g. Google Authenticator, Authy) to add a second factor to your login.
            </p>
            <button
              onClick={() => startEnroll.mutate()}
              disabled={startEnroll.isPending}
              className="px-4 py-2 text-sm bg-blue-600 hover:bg-blue-500 disabled:opacity-50 rounded transition-colors"
            >
              {startEnroll.isPending ? 'Starting…' : 'Set up authenticator'}
            </button>
          </div>
        )}

        {enrolling && (
          <div className="space-y-4">
            <p className="text-sm text-gray-400">
              Scan the QR code with your authenticator app, then enter the 6-digit code to confirm.
            </p>
            {qrUri && (
              <div className="flex flex-col items-center gap-3">
                {/* Render QR as an img using a data-uri — the backend returns an otpauth:// URI;
                    we ask the browser to render it using a free QR API if available, otherwise
                    show the raw secret for manual entry. */}
                <img
                  src={`https://api.qrserver.com/v1/create-qr-code/?size=200x200&data=${encodeURIComponent(qrUri)}`}
                  alt="TOTP QR code"
                  className="rounded border border-gray-700"
                  width={200}
                  height={200}
                />
                <details className="w-full">
                  <summary className="text-xs text-gray-500 cursor-pointer hover:text-gray-300">
                    Manual entry
                  </summary>
                  <code className="block mt-1 font-mono text-xs text-gray-300 break-all bg-gray-800 px-3 py-2 rounded">
                    {secret}
                  </code>
                </details>
              </div>
            )}
            <div>
              <label className="block text-xs text-gray-400 mb-1">Verification code</label>
              <input
                type="text"
                inputMode="numeric"
                pattern="[0-9]*"
                maxLength={6}
                autoFocus
                value={code}
                onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
                placeholder="000000"
                className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm text-center tracking-widest focus:outline-none focus:border-blue-500"
              />
            </div>
            <div className="flex gap-2">
              <button
                onClick={() => { setEnrolling(false); setCode('') }}
                className="px-3 py-1.5 text-sm text-gray-400 hover:text-white transition-colors"
              >
                Cancel
              </button>
              <button
                onClick={() => confirmEnroll.mutate()}
                disabled={confirmEnroll.isPending || code.length !== 6}
                className="flex-1 py-1.5 text-sm bg-blue-600 hover:bg-blue-500 disabled:opacity-50 rounded transition-colors"
              >
                {confirmEnroll.isPending ? 'Verifying…' : 'Confirm'}
              </button>
            </div>
          </div>
        )}

        {status?.totp_enabled && !showDisable && (
          <div className="space-y-3">
            <p className="text-sm text-gray-400">
              Two-factor authentication is active. You will be prompted for a code at each login.
            </p>
            <button
              onClick={() => { setShowDisable(true); setMsg(null) }}
              className="px-4 py-2 text-sm bg-red-700 hover:bg-red-600 rounded transition-colors"
            >
              Disable 2FA
            </button>
          </div>
        )}

        {status?.totp_enabled && showDisable && (
          <div className="space-y-3">
            <p className="text-sm text-gray-400">Enter your current password to disable two-factor authentication.</p>
            <div>
              <label className="block text-xs text-gray-400 mb-1">Current password</label>
              <input
                type="password"
                className="w-full bg-gray-800 border border-gray-700 rounded px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
                value={disablePassword}
                onChange={(e) => setDisablePassword(e.target.value)}
              />
            </div>
            <div className="flex gap-2">
              <button
                onClick={() => { setShowDisable(false); setDisablePassword('') }}
                className="px-3 py-1.5 text-sm text-gray-400 hover:text-white transition-colors"
              >
                Cancel
              </button>
              <button
                onClick={() => disable.mutate()}
                disabled={disable.isPending || !disablePassword}
                className="flex-1 py-1.5 text-sm bg-red-700 hover:bg-red-600 disabled:opacity-50 rounded transition-colors"
              >
                {disable.isPending ? 'Disabling…' : 'Disable 2FA'}
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
