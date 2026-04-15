import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { clustersApi } from '../api/clusters'
import Layout from '../components/Layout'

export default function Dashboard() {
  const { data: clusters } = useQuery({
    queryKey: ['clusters'],
    queryFn: clustersApi.list,
    refetchInterval: 30_000,
  })

  return (
    <Layout>
      <h2 className="text-2xl font-semibold mb-6">Dashboard</h2>

      <div className="grid grid-cols-1 md:grid-cols-3 gap-4 mb-8">
        {[
          { label: 'Clusters', value: clusters?.length ?? '—' },
          { label: 'Nodes Connected', value: '—' },
          { label: 'Active Alerts', value: '0' },
        ].map((stat) => (
          <div key={stat.label} className="bg-gray-900 border border-gray-800 rounded-xl p-6">
            <p className="text-sm text-gray-400 mb-1">{stat.label}</p>
            <p className="text-3xl font-semibold">{stat.value}</p>
          </div>
        ))}
      </div>

      <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
        <div className="flex items-center justify-between px-6 py-4 border-b border-gray-800">
          <h3 className="font-medium">Clusters</h3>
          <Link
            to="/clusters"
            className="text-sm text-blue-400 hover:text-blue-300 transition-colors"
          >
            View all →
          </Link>
        </div>

        {!clusters || clusters.length === 0 ? (
          <div className="p-8 text-center">
            <p className="text-gray-500 text-sm mb-3">No clusters configured yet.</p>
            <Link
              to="/clusters"
              className="text-blue-400 hover:text-blue-300 text-sm transition-colors"
            >
              Create your first cluster →
            </Link>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-gray-400 border-b border-gray-800">
                <th className="text-left px-6 py-3 font-medium">Name</th>
                <th className="text-left px-6 py-3 font-medium">Mode</th>
                <th className="text-left px-6 py-3 font-medium">Auto Failover</th>
                <th className="px-6 py-3" />
              </tr>
            </thead>
            <tbody>
              {clusters.map((c) => (
                <tr
                  key={c.id}
                  className="border-b border-gray-800 last:border-0 hover:bg-gray-800/40"
                >
                  <td className="px-6 py-3 font-medium">{c.name}</td>
                  <td className="px-6 py-3 text-gray-400">
                    {c.mode === 'active_standby' ? 'Active / Standby' : 'HA'}
                  </td>
                  <td className="px-6 py-3 text-gray-400">
                    {c.auto_failover ? (
                      <span className="text-emerald-400">On</span>
                    ) : (
                      'Off'
                    )}
                  </td>
                  <td className="px-6 py-3 text-right">
                    <Link
                      to={`/clusters/${c.id}`}
                      className="text-blue-400 hover:text-blue-300 transition-colors"
                    >
                      View →
                    </Link>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </Layout>
  )
}
