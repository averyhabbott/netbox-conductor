import { Link, useLocation } from 'react-router-dom'
import { useAuthStore } from '../store/auth'

interface LayoutProps {
  children: React.ReactNode
}

export default function Layout({ children }: LayoutProps) {
  const user = useAuthStore((s) => s.user)
  const logout = useAuthStore((s) => s.logout)
  const { pathname } = useLocation()
  const navLinks = [
    { to: '/', label: 'Dashboard' },
    { to: '/clusters', label: 'Clusters' },
    { to: '/nodes', label: 'Nodes' },
    { to: '/available-agents', label: 'Available Agents' },
    { to: '/settings', label: 'Settings' },
  ]

  return (
    <div className="min-h-screen bg-gray-950 text-white">
      <nav className="border-b border-gray-800 px-6 py-4 flex items-center justify-between">
        <div className="flex items-center gap-8">
          <span className="font-semibold text-lg">NetBox Conductor</span>
          <div className="flex items-center gap-4">
            {navLinks.map((link) => (
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
