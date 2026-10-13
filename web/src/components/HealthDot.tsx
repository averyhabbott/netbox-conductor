const HEALTH_DOT: Record<string, { color: string; title: string }> = {
  healthy:  { color: 'bg-emerald-400', title: 'Healthy' },
  degraded: { color: 'bg-amber-400',   title: 'Degraded (service down)' },
}

// Anything not healthy or degraded (including unknown) shows as offline.
export default function HealthDot({ status }: { status?: string }) {
  const { color, title } = HEALTH_DOT[status ?? ''] ?? { color: 'bg-red-500', title: 'Offline' }
  return <span title={title} className={`inline-block w-2.5 h-2.5 rounded-full flex-shrink-0 ${color}`} />
}
