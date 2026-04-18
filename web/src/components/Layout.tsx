import { useState, useRef, useEffect } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { useAuthStore } from '../store/auth'

interface LayoutProps {
  children: React.ReactNode
}

function DownloadAgentDropdown() {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as HTMLElement)) setOpen(false)
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  return (
    <div className="relative" ref={ref}>
      <button
        onClick={() => setOpen((v) => !v)}
        className="text-sm text-gray-400 hover:text-white transition-colors"
      >
        Download Agent <span className="text-gray-600">▾</span>
      </button>
      {open && (
        <div className="absolute left-0 top-full mt-2 bg-gray-900 border border-gray-700 rounded-lg shadow-xl z-50 min-w-max py-1">
          <a
            href="/api/v1/downloads/agent-linux-amd64"
            download
            onClick={() => setOpen(false)}
            className="block px-4 py-2 text-sm text-gray-200 hover:bg-gray-800 transition-colors rounded-t-lg"
          >
            Linux amd64
          </a>
          <a
            href="/api/v1/downloads/agent-linux-arm64"
            download
            onClick={() => setOpen(false)}
            className="block px-4 py-2 text-sm text-gray-200 hover:bg-gray-800 transition-colors rounded-b-lg"
          >
            Linux arm64
          </a>
        </div>
      )}
    </div>
  )
}

export default function Layout({ children }: LayoutProps) {
  const user = useAuthStore((s) => s.user)
  const logout = useAuthStore((s) => s.logout)
  const { pathname } = useLocation()
  const mainLinks = [
    { to: '/', label: 'Dashboard' },
    { to: '/clusters', label: 'Clusters' },
    { to: '/nodes', label: 'Nodes' },
    { to: '/available-agents', label: 'Available Agents' },
  ]

  return (
    <div className="min-h-screen bg-gray-950 text-white">
      <nav className="border-b border-gray-800 px-6 py-4 flex items-center justify-between">
        <div className="flex items-center gap-8">
          <span className="font-semibold text-lg">NetBox Conductor</span>
          <div className="flex items-center gap-4">
            {mainLinks.map((link) => (
              <Link
                key={link.to}
                to={link.to}
                className={`text-sm transition-colors ${
                  pathname === link.to
                    ? 'text-white font-medium'
                    : 'text-gray-400 hover:text-white'
                }`}
              >
                {link.label}
              </Link>
            ))}
            <DownloadAgentDropdown />
            <Link
              to="/settings"
              className={`text-sm transition-colors ${
                pathname === '/settings'
                  ? 'text-white font-medium'
                  : 'text-gray-400 hover:text-white'
              }`}
            >
              Settings
            </Link>
          </div>
        </div>
        <div className="flex items-center gap-4">
          <span className="text-sm text-gray-400">
            {user?.username}{' '}
            <span className="text-xs bg-gray-800 px-2 py-0.5 rounded">{user?.role}</span>
          </span>
          <button
            onClick={() => logout()}
            className="text-sm text-gray-400 hover:text-white transition-colors"
          >
            Sign out
          </button>
        </div>
      </nav>
      <main className="max-w-7xl mx-auto px-6 py-8">{children}</main>
    </div>
  )
}
