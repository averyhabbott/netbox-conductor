import { useEffect, useRef, useCallback } from 'react'
import { useAuthStore } from '../store/auth'

export type SSEEventType =
  | 'node.status'
  | 'node.heartbeat'
  | 'task.complete'
  | 'patroni.state'

export interface SSEEvent {
  type: SSEEventType
  node_id?: string
  payload: Record<string, unknown>
}

type SSEHandler = (event: SSEEvent) => void

/**
 * Connects to the /api/v1/events SSE stream and calls `onEvent` for each message.
 * Automatically reconnects on error (with a short delay).
 * Disconnects when the component unmounts.
 */
export function useSSE(onEvent: SSEHandler, enabled = true) {
  const handlerRef = useRef(onEvent)
  handlerRef.current = onEvent

  const reconnectTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const esRef = useRef<EventSource | null>(null)

  const connect = useCallback(() => {
    const token = useAuthStore.getState().accessToken
    if (!token) return

    // EventSource doesn't support custom headers natively; pass token as query param.
    // The server's JWT middleware reads from Authorization header — we need an alternative.
    // Use a fetch-based approach via ReadableStream instead.
    const ctrl = new AbortController()

    fetch('/api/v1/events', {
      headers: { Authorization: `Bearer ${token}` },
      signal: ctrl.signal,
    }).then(async (res) => {
      if (!res.ok || !res.body) {
        scheduleReconnect()
        return
      }

      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ''

      while (true) {
        const { value, done } = await reader.read()
        if (done) break

        buffer += decoder.decode(value, { stream: true })

        // SSE frames are separated by double newlines
        const frames = buffer.split('\n\n')
        buffer = frames.pop() ?? ''

        for (const frame of frames) {
          if (!frame.trim()) continue
          let eventType: string | null = null
          let dataLine: string | null = null

          for (const line of frame.split('\n')) {
            if (line.startsWith('event: ')) {
              eventType = line.slice(7).trim() // retained for future filtering
              void eventType
            } else if (line.startsWith('data: ')) {
              dataLine = line.slice(6)
            }
          }

          if (dataLine) {
            try {
              const parsed = JSON.parse(dataLine) as SSEEvent
              handlerRef.current(parsed)
            } catch {
              // ignore malformed frames
            }
          }
        }
      }

      scheduleReconnect()
    }).catch((err) => {
      if (err?.name !== 'AbortError') {
        scheduleReconnect()
      }
    })

    // Store abort controller so we can cancel on unmount
    esRef.current = { close: () => ctrl.abort() } as unknown as EventSource
  }, [])

  const scheduleReconnect = useCallback(() => {
    if (reconnectTimer.current) return
    reconnectTimer.current = setTimeout(() => {
      reconnectTimer.current = null
      connect()
    }, 3000)
  }, [connect])

  useEffect(() => {
    if (!enabled) return
    connect()
    return () => {
      esRef.current?.close()
      if (reconnectTimer.current) {
        clearTimeout(reconnectTimer.current)
      }
    }
  }, [enabled, connect])
}
