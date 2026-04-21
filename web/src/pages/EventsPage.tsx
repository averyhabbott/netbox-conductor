import { useEffect, useState, useCallback } from 'react'
import { eventsApi, type Event, type EventFilter } from '../api/events'
import EventsTable from '../components/EventsTable'
import Layout from '../components/Layout'

const CATEGORIES = ['cluster', 'service', 'ha', 'config', 'agent']
const SEVERITIES = ['debug', 'info', 'warn', 'error', 'critical']

export default function EventsPage() {
  const [events, setEvents] = useState<Event[]>([])
  const [loading, setLoading] = useState(true)
  const [filter, setFilter] = useState<EventFilter>({ limit: 200 })
  const [offset, setOffset] = useState(0)

  const load = useCallback(async (f: EventFilter, off: number) => {
    setLoading(true)
    try {
      const data = await eventsApi.list({ ...f, offset: off, limit: 200 })
      setEvents(data)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { load(filter, offset) }, [filter, offset, load])

  function setF(key: keyof EventFilter, value: string) {
    setOffset(0)
    setFilter((f) => ({ ...f, [key]: value || undefined }))
  }

  return (
    <Layout>
    <div className="max-w-7xl mx-auto px-6 py-8">
      <div className="flex items-center justify-between mb-6">
        <h1 className="text-xl font-semibold text-white">Events</h1>
        <button
          onClick={() => load(filter, offset)}
          className="px-3 py-1.5 text-sm bg-gray-800 hover:bg-gray-700 border border-gray-700 text-gray-300 rounded transition-colors"
        >
          Refresh
        </button>
      </div>

      {/* Filters */}
      <div className="bg-gray-900 border border-gray-800 rounded-lg p-4 mb-6 flex flex-wrap gap-3">
        <select
          value={filter.category ?? ''}
          onChange={(e) => setF('category', e.target.value)}
          className="bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-gray-300 focus:outline-none focus:border-blue-500"
        >
          <option value="">All categories</option>
          {CATEGORIES.map((c) => <option key={c} value={c}>{c}</option>)}
        </select>

        <select
          value={filter.severity ?? ''}
          onChange={(e) => setF('severity', e.target.value)}
          className="bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-gray-300 focus:outline-none focus:border-blue-500"
        >
          <option value="">All severities</option>
          {SEVERITIES.map((s) => <option key={s} value={s}>{s}</option>)}
        </select>

        <input
          type="text"
          placeholder="Code prefix (e.g. NBC-HA)"
          value={filter.code ?? ''}
          onChange={(e) => setF('code', e.target.value)}
          className="bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-gray-300 focus:outline-none focus:border-blue-500 w-52"
        />

        <input
          type="datetime-local"
          value={filter.from?.slice(0, 16) ?? ''}
          onChange={(e) => setF('from', e.target.value ? new Date(e.target.value).toISOString() : '')}
          className="bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-gray-300 focus:outline-none focus:border-blue-500"
        />
        <span className="text-gray-600 self-center text-xs">to</span>
        <input
          type="datetime-local"
          value={filter.to?.slice(0, 16) ?? ''}
          onChange={(e) => setF('to', e.target.value ? new Date(e.target.value).toISOString() : '')}
          className="bg-gray-800 border border-gray-700 rounded px-3 py-1.5 text-sm text-gray-300 focus:outline-none focus:border-blue-500"
        />

        {(filter.category || filter.severity || filter.code || filter.from || filter.to) && (
          <button
            onClick={() => { setOffset(0); setFilter({ limit: 200 }) }}
            className="px-3 py-1.5 text-sm text-gray-400 hover:text-white transition-colors"
          >
            Clear
          </button>
        )}
      </div>

      {/* Table */}
      <div className="bg-gray-900 border border-gray-800 rounded-lg p-4">
        <EventsTable events={events} loading={loading} showCluster showNode />

        {/* Pagination */}
        {!loading && (
          <div className="flex items-center gap-3 mt-4 pt-4 border-t border-gray-800">
            <button
              disabled={offset === 0}
              onClick={() => setOffset(Math.max(0, offset - 200))}
              className="px-3 py-1 text-sm bg-gray-800 hover:bg-gray-700 disabled:opacity-40 disabled:cursor-not-allowed border border-gray-700 rounded text-gray-300"
            >
              ← Prev
            </button>
            <span className="text-gray-500 text-sm">Showing {offset + 1}–{offset + events.length}</span>
            <button
              disabled={events.length < 200}
              onClick={() => setOffset(offset + 200)}
              className="px-3 py-1 text-sm bg-gray-800 hover:bg-gray-700 disabled:opacity-40 disabled:cursor-not-allowed border border-gray-700 rounded text-gray-300"
            >
              Next →
            </button>
          </div>
        )}
      </div>
    </div>
    </Layout>
  )
}
