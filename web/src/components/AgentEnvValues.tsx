import { useCopy } from '../hooks/useCopy'

const code = 'text-xs bg-gray-800 px-1.5 py-0.5 rounded font-mono'

// The agent .env values the conductor issues for a node (Add Node wizard and
// Re-key agent). Issuing them revokes the node's previous token.
export default function AgentEnvValues({ hostname, snippet }: { hostname: string; snippet: string }) {
  const [copied, copy] = useCopy()
  return (
    <>
      <p className="text-sm text-gray-400">
        On <code className={code}>{hostname}</code>, set these values in{' '}
        <code className={code}>/etc/netbox-agent/netbox-agent.env</code> (replace the existing lines):
      </p>
      <div className="relative">
        <pre className="bg-gray-950 border border-gray-800 rounded-lg p-4 text-xs font-mono text-gray-300 overflow-x-auto">
{snippet}
        </pre>
        <button
          onClick={() => copy(snippet)}
          className="absolute top-2 right-2 bg-gray-800 hover:bg-gray-700 text-xs px-2 py-1 rounded transition-colors"
        >
          {copied ? '✓ Copied' : 'Copy'}
        </button>
      </div>
    </>
  )
}
