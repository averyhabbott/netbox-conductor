import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { nodesApi } from '../api/nodes'
import type { CreateNodeBody, Node, RegTokenResponse } from '../api/nodes'

interface Props {
  clusterId: string
  clusterName: string
  onClose: () => void
}

type Step = 1 | 2 | 3

export default function AddNodeWizard({ clusterId, clusterName, onClose }: Props) {
  const [step, setStep] = useState<Step>(1)
  const [node, setNode] = useState<Node | null>(null)
  const [regToken, setRegToken] = useState<RegTokenResponse | null>(null)
  const [copied, setCopied] = useState(false)
  const [error, setError] = useState('')

  const [form, setForm] = useState<CreateNodeBody>({
    hostname: '',
    ip_address: '',
    role: 'hyperconverged',
    failover_priority: 100,
    ssh_port: 22,
  })

  const createNode = useMutation({
    mutationFn: (body: CreateNodeBody) => nodesApi.create(clusterId, body),
    onSuccess: (created) => {
      setNode(created)
      setStep(2)
      setError('')
    },
    onError: (e: any) => {
      setError(e.response?.data?.message ?? 'Failed to create node')
    },
  })

  const generateToken = useMutation({
    mutationFn: () => nodesApi.generateRegToken(clusterId, node!.id),
    onSuccess: (tok) => {
      setRegToken(tok)
      setStep(3)
      setError('')
    },
    onError: (e: any) => {
      setError(e.response?.data?.message ?? 'Failed to generate token')
    },
  })

  const copyEnv = async () => {
    if (!regToken) return
    await navigator.clipboard.writeText(regToken.env_snippet)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4">
      <div className="bg-gray-900 border border-gray-700 rounded-xl w-full max-w-xl">
        {/* Header */}
        <div className="flex items-center justify-between px-6 py-4 border-b border-gray-800">
          <div>
            <h3 className="font-semibold">Add Node</h3>
            <p className="text-xs text-gray-500 mt-0.5">{clusterName}</p>
          </div>
          {/* Step indicators */}
          <div className="flex items-center gap-2">
            {([1, 2, 3] as Step[]).map((s) => (
              <div
                key={s}
                className={`w-7 h-7 rounded-full flex items-center justify-center text-xs font-medium ${
                  step === s
                    ? 'bg-blue-600 text-white'
                    : step > s
                    ? 'bg-emerald-600 text-white'
                    : 'bg-gray-800 text-gray-500'
                }`}
              >
                {step > s ? '✓' : s}
              </div>
            ))}
          </div>
        </div>

        <div className="px-6 py-6">
          {/* ── Step 1: Node details ── */}
          {step === 1 && (
            <form
              onSubmit={(e) => {
                e.preventDefault()
                createNode.mutate(form)
              }}
              className="space-y-4"
            >
              <p className="text-sm text-gray-400 mb-4">
                Enter the node's details. The agent binary will be installed manually.
              </p>
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <label className="block text-xs text-gray-400 mb-1">Hostname</label>
                  <input
                    className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
                    placeholder="nbfa-1"
                    value={form.hostname}
                    onChange={(e) => setForm({ ...form, hostname: e.target.value })}
                    required
                  />
                </div>
                <div>
                  <label className="block text-xs text-gray-400 mb-1">IP Address</label>
                  <input
                    className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
                    placeholder="192.168.1.10"
                    value={form.ip_address}
                    onChange={(e) => setForm({ ...form, ip_address: e.target.value })}
                    required
                  />
                </div>
              </div>
              <div>
                <label className="block text-xs text-gray-400 mb-1">Role</label>
                <select
                  className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
                  value={form.role}
                  onChange={(e) =>
                    setForm({ ...form, role: e.target.value as CreateNodeBody['role'] })
                  }
                >
                  <option value="hyperconverged">Hyper-converged (NetBox + DB + Redis)</option>
                  <option value="app">App only (NetBox + RQ + Redis)</option>
                  <option value="db_only">DB only (Postgres + Patroni)</option>
                </select>
              </div>
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <label className="block text-xs text-gray-400 mb-1">Failover Priority</label>
                  <input
                    type="number"
                    className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
                    value={form.failover_priority}
                    onChange={(e) =>
                      setForm({ ...form, failover_priority: Number(e.target.value) })
                    }
                    min={1}
                  />
                  <p className="text-xs text-gray-600 mt-1">Lower = preferred primary</p>
                </div>
                <div>
                  <label className="block text-xs text-gray-400 mb-1">SSH Port</label>
                  <input
                    type="number"
                    className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm focus:outline-none focus:border-blue-500"
                    value={form.ssh_port}
                    onChange={(e) =>
                      setForm({ ...form, ssh_port: Number(e.target.value) })
                    }
                  />
                </div>
              </div>
              {error && <p className="text-sm text-red-400">{error}</p>}
              <div className="flex gap-3 pt-2">
                <button
                  type="submit"
                  disabled={createNode.isPending}
                  className="flex-1 bg-blue-600 hover:bg-blue-500 disabled:opacity-50 rounded-lg py-2 text-sm font-medium transition-colors"
                >
                  {createNode.isPending ? 'Creating…' : 'Create Node →'}
                </button>
                <button
                  type="button"
                  onClick={onClose}
                  className="bg-gray-800 hover:bg-gray-700 rounded-lg px-4 py-2 text-sm transition-colors"
                >
                  Cancel
                </button>
              </div>
            </form>
          )}

          {/* ── Step 2: Install agent ── */}
          {step === 2 && node && (
            <div className="space-y-4">
              <div className="bg-emerald-900/20 border border-emerald-800/50 rounded-lg p-4">
                <p className="text-sm text-emerald-300 font-medium">
                  Node <code className="font-mono">{node.hostname}</code> created
                </p>
                <p className="text-xs text-emerald-400/80 mt-1">Node ID: {node.id}</p>
              </div>

              <p className="text-sm text-gray-300 font-medium">
                Step 2: Install the agent binary on the node
              </p>
              <p className="text-sm text-gray-400">
                SSH into <code className="text-xs bg-gray-800 px-1.5 py-0.5 rounded font-mono">{node.hostname}</code> and run:
              </p>

              <pre className="bg-gray-950 border border-gray-800 rounded-lg p-4 text-xs font-mono text-gray-300 overflow-x-auto whitespace-pre-wrap">
{`# Download and install the agent
curl -fsSL https://your-tool-server/api/v1/downloads/agent-linux-arm64 \\
  -o /usr/local/bin/netbox-agent
chmod +x /usr/local/bin/netbox-agent

# Create dedicated service user
useradd -r -s /bin/false -G netbox netbox-agent

# Create config directory
mkdir -p /etc/netbox-agent`}
              </pre>

              <p className="text-xs text-gray-500">
                Once the binary is installed, click Continue to get the registration token.
              </p>

              {error && <p className="text-sm text-red-400">{error}</p>}

              <div className="flex gap-3">
                <button
                  onClick={() => generateToken.mutate()}
                  disabled={generateToken.isPending}
                  className="flex-1 bg-blue-600 hover:bg-blue-500 disabled:opacity-50 rounded-lg py-2 text-sm font-medium transition-colors"
                >
                  {generateToken.isPending ? 'Generating…' : 'Generate Token →'}
                </button>
                <button
                  onClick={() => setStep(1)}
                  className="bg-gray-800 hover:bg-gray-700 rounded-lg px-4 py-2 text-sm transition-colors"
                >
                  Back
                </button>
              </div>
            </div>
          )}

          {/* ── Step 3: Paste ENV and connect ── */}
          {step === 3 && regToken && node && (
            <div className="space-y-4">
              <p className="text-sm text-gray-300 font-medium">
                Step 3: Configure and start the agent
              </p>
              <p className="text-sm text-gray-400">
                On <code className="text-xs bg-gray-800 px-1.5 py-0.5 rounded font-mono">{node.hostname}</code>,
                create <code className="text-xs bg-gray-800 px-1.5 py-0.5 rounded font-mono">/etc/netbox-agent/netbox-agent.env</code> with:
              </p>

              <div className="relative">
                <pre className="bg-gray-950 border border-gray-800 rounded-lg p-4 text-xs font-mono text-gray-300 overflow-x-auto">
{regToken.env_snippet}
                </pre>
                <button
                  onClick={copyEnv}
                  className="absolute top-2 right-2 bg-gray-800 hover:bg-gray-700 text-xs px-2 py-1 rounded transition-colors"
                >
                  {copied ? '✓ Copied' : 'Copy'}
                </button>
              </div>

              <p className="text-xs text-yellow-500/80">
                ⚠ This token expires at {new Date(regToken.expires_at).toLocaleTimeString()} —
                start the agent before then.
              </p>

              <pre className="bg-gray-950 border border-gray-800 rounded-lg p-4 text-xs font-mono text-gray-300">
{`# Install and start the systemd service
# (see deployments/agent/netbox-agent.service)
systemctl daemon-reload
systemctl enable --now netbox-agent`}
              </pre>

              <p className="text-sm text-gray-400">
                The node will appear as <strong className="text-emerald-400">Connected</strong>{' '}
                on the cluster page once the agent connects.
              </p>

              <button
                onClick={onClose}
                className="w-full bg-emerald-700 hover:bg-emerald-600 rounded-lg py-2 text-sm font-medium transition-colors"
              >
                Done — Back to Cluster
              </button>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
