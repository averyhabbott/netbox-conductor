# NetBox Conductor

A management platform for orchestrating multi-node [NetBox](https://netboxlabs.com/) deployments. NetBox Conductor coordinates configuration, service lifecycle, database high availability (via Patroni), Redis Sentinel, and media sync across a cluster of NetBox servers — all from a single web UI.

---

## How It Works

**Conductor** is a single Go binary that serves the web UI and API. Each managed NetBox server runs a lightweight **agent** binary that maintains a persistent outbound WebSocket connection to the Conductor. The Conductor dispatches tasks (write config, restart service, push Patroni config, etc.) to agents over that connection; agents report back heartbeats, service state, and task results.

No inbound firewall rules are needed on agent hosts — all traffic is agent-initiated.

```
┌─────────────────────────────────────────────┐
│             NetBox Conductor                │
│  (web UI + API + task dispatcher)           │
└──────────────┬──────────────────────────────┘
               │ wss:// (outbound from agent)
       ┌───────┴────────┐
       │                │
┌──────▼──────┐  ┌──────▼──────┐
│  nbfa-1     │  │  nbfa-2     │
│  netbox-    │  │  netbox-    │
│  agent      │  │  agent      │
└─────────────┘  └─────────────┘
```

---

## Features

### Cluster & Node Management
- Create clusters with multiple nodes (hyperconverged, app-only, or db-only roles)
- Per-node maintenance mode (suppresses auto-start, excludes from failover)
- Node removal: **Decommission** (full walkthrough with manual cleanup steps) or **Force Remove** (records only, agent not contacted)
- Failover priority (1–100) controls which node Patroni prefers as primary

### Configuration Management
- Edit `configuration.py` in a syntax-highlighted browser editor
- Version history with diffs
- Push config to all nodes or a single node (atomic write on agent via temp-file rename)
- Per-node ALLOWED_HOSTS and other override support

### Service Control
- Start / Stop / Restart NetBox and NetBox-RQ on any node
- Restart Patroni, Redis, or Redis Sentinel
- All actions dispatched as WebSocket tasks with full result tracking

### PostgreSQL HA (Patroni)
- Live cluster topology view
- Switchover and manual failover from the UI
- Push `patroni.yml` config to all nodes
- Install Patroni on a node (dispatches `patroni.install` task)
- DB Restore: reinitialize replica or pgBackRest point-in-time recovery
- Retention policy: set retention days and expire command; run expire on demand
- Built-in witness node (lightweight pysyncobj subprocess on Conductor) for 2-node tie-breaking

### Redis Sentinel
- Push Sentinel configuration across the cluster
- Sentinel addresses auto-derived from node IPs (port 26379)

### Media Sync
- Relay NetBox media files between nodes through the Conductor (no direct node-to-node SSH required)
- Chunked transfer with backpressure

### Agent Pool (Staging)
- Deploy agents to servers before assigning them to a cluster
- Agents connect via staging tokens and appear in the "Available Agents" pool
- Assign directly to a cluster from the pool

### Security
- JWT authentication (RS256) with TOTP two-factor option
- Role-based access: `viewer` (read-only), `operator` (actions), `admin` (full control)
- AES-256-GCM encryption for all credentials at rest
- Audit log on every mutating action (CSV export available)
- Per-agent token authentication; tokens revoked on node removal

### Observability
- Live heartbeat sparklines (CPU, memory, disk, service state)
- Task history per node with full request/response payloads
- Agent log viewer (agent logs + NetBox application logs, per-file selector)
- Prometheus metrics endpoint (`/metrics`)
- Server-Sent Events for real-time UI updates

---

## Architecture

| Component | Technology |
|-----------|-----------|
| Server | Go 1.25 + Echo v4 |
| Frontend | React 18 + TypeScript + Vite + Tailwind CSS v4 |
| Database | PostgreSQL (pgx/v5 driver) |
| Agent protocol | WebSocket + JSON envelopes |
| HA | Patroni (Raft consensus), Redis Sentinel |
| Auth | JWT (RS256), bcrypt, AES-256-GCM, TOTP |

The server binary embeds the compiled frontend (`go:embed`), so there is only one file to deploy. The agent is a separate static binary compiled for the target platform.

---

## Installation

### Prerequisites

- Go 1.25+
- Node.js 20+ (for building the frontend)
- PostgreSQL 15+ on the server host
- Linux hosts for the managed nodes (agent binary targets Linux amd64/arm64)

### Build

```bash
# Build server (Linux arm64) + agent binaries (amd64 + arm64)
make build-all

# Output:
#   bin/netbox-tool-server-linux-arm64
#   bin/netbox-agent-linux-amd64
#   bin/netbox-agent-linux-arm64

# Build for development (current OS)
make build-server   # server for local OS
make build-agent    # agent for local OS
```

### Server Setup

**1. Create the system user and directories:**

```bash
groupadd --system netbox-conductor
useradd --system --gid netbox-conductor --no-create-home --shell /usr/sbin/nologin netbox-conductor

mkdir -p /opt/netbox-conductor/bin
mkdir -p /etc/netbox-conductor
mkdir -p /var/log/netbox-conductor
chown netbox-conductor:netbox-conductor /var/log/netbox-conductor
```

**2. Copy the binary:**

```bash
cp bin/netbox-tool-server-linux-arm64 /opt/netbox-conductor/bin/netbox-conductor
chmod +x /opt/netbox-conductor/bin/netbox-conductor
```

**3. Copy agent binaries** (served to managed nodes via download endpoint):

```bash
mkdir -p /var/lib/netbox-conductor/bin
cp bin/netbox-agent-linux-amd64 /var/lib/netbox-conductor/bin/
cp bin/netbox-agent-linux-arm64 /var/lib/netbox-conductor/bin/
chmod +x /var/lib/netbox-conductor/bin/netbox-agent-linux-*
```

**4. Configure the environment** — copy and edit `deployments/server/netbox-conductor.env.example` to `/etc/netbox-conductor/netbox-conductor.env`:

```bash
# Required
DATABASE_URL=postgres://netbox_conductor:<password>@localhost:5432/netbox_conductor?sslmode=disable
JWT_SECRET=<64 random hex chars>
SERVER_URL=https://your-conductor.example.com

# Optional (defaults shown)
LISTEN_ADDR=:8443
LOG_DIR=/var/log
LOG_NAME=netbox-conductor
LOG_LEVEL=info

# Encryption key for secrets at rest (generate once; back up securely)
# Either point to a key file:
NETBOX_TOOL_MASTER_KEY_FILE=/etc/netbox-conductor/master.key
# Or provide inline:
# NETBOX_TOOL_MASTER_KEY=<64 hex chars>

# TLS (leave empty to auto-generate a self-signed cert on first start)
# TLS_CERT_FILE=/etc/netbox-conductor/tls.crt
# TLS_KEY_FILE=/etc/netbox-conductor/tls.key

# Path to agent binaries for download endpoint
AGENT_BIN_DIR=/var/lib/netbox-conductor/bin
```

**5. Create the database:**

```bash
createdb netbox_conductor
# Migrations run automatically on startup — no manual migration step needed
```

**6. Install and start the systemd service:**

```bash
cp deployments/server/netbox-conductor.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now netbox-conductor
journalctl -u netbox-conductor -f
```

The UI is available at `https://<server>:8443`. On first start, create your admin account via the login page (if no users exist, the first registration is granted admin).

### Agent Setup (per managed node)

There are two ways to install the agent on a managed node:

**Option A — Download tarball from Conductor UI (recommended):**

1. In the Conductor UI, navigate to your cluster and click **Add Node**
2. Complete Step 1 (hostname, IP, role) and Step 2 (install agent):
   ```bash
   curl -fsSL https://your-conductor.example.com/api/v1/downloads/agent-linux-amd64 \
     -o netbox-agent.tar.gz
   tar -xzf netbox-agent.tar.gz
   sudo bash install.sh
   ```
3. The tarball includes: agent binary, `install.sh`, `netbox-agent.service`, `netbox-agent-sudoers`, and `netbox-agent.env.example`
4. Step 3 generates the env snippet — copy it to `/etc/netbox-agent/netbox-agent.env` on the node, then:
   ```bash
   sudo systemctl start netbox-agent
   ```

**Option B — Manual:**

```bash
# On the managed node:
sudo groupadd --system netbox-agent
sudo useradd --system --gid netbox-agent --no-create-home --shell /usr/sbin/nologin netbox-agent

sudo cp netbox-agent /usr/local/bin/netbox-agent
sudo chmod +x /usr/local/bin/netbox-agent

# Install service file and sudoers
sudo cp netbox-agent.service /etc/systemd/system/
sudo install -m 440 netbox-agent-sudoers /etc/sudoers.d/netbox-agent

# Create config dir
sudo mkdir -p /etc/netbox-agent
sudo cp netbox-agent.env.example /etc/netbox-agent/netbox-agent.env
sudo chown root:netbox-agent /etc/netbox-agent/netbox-agent.env
sudo chmod 640 /etc/netbox-agent/netbox-agent.env
```

Edit `/etc/netbox-agent/netbox-agent.env`:

```bash
AGENT_NODE_ID=<uuid from Conductor>
AGENT_TOKEN=<token from Conductor>
AGENT_SERVER_URL=https://your-conductor.example.com/api/v1/agent/connect
# For self-signed certs in dev:
# AGENT_TLS_SKIP_VERIFY=true

# NetBox paths (adjust to your installation)
NETBOX_CONFIG_PATH=/opt/netbox/netbox/netbox/configuration.py
NETBOX_MEDIA_ROOT=/opt/netbox/netbox/media

# Patroni (default is fine for standard install)
PATRONI_REST_URL=http://127.0.0.1:8008
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now netbox-agent
```

The node will appear as **Connected** in the Conductor UI once the agent authenticates.

### Directory Permissions (managed nodes)

The `install.sh` script handles these, but for reference:

| Directory | Required action |
|---|---|
| `/opt/netbox/netbox/netbox` | `chown root:netbox-agent`, `chmod g+w` |
| `/etc/patroni` | Create, `chown netbox-agent:netbox-agent` |
| `/etc/redis` | `usermod -aG redis netbox-agent` |

---

## Development

```bash
# 1. Start PostgreSQL and create the dev database
createdb netbox_conductor_dev

# 2. Copy and edit the server env
cp deployments/server/netbox-conductor.env.example .env
# Edit DATABASE_URL, JWT_SECRET, SERVER_URL

# 3. Start the backend (auto-runs migrations, serves API on :8443)
make dev

# 4. In a separate terminal, start the frontend dev server (proxies /api to :8443)
make dev-frontend
# → http://localhost:5173
```

### Running Tests

```bash
make test       # Go unit tests
make vet        # go vet
make typecheck  # TypeScript type check
```

### Dev Deploy Script

For the OrbStack dev environment:

```bash
# Build and push conductor to nbfa-tool
bash testing/deploy.sh

# Also push agent binary, service file, and sudoers to nbfa-1 and nbfa-2
bash testing/deploy.sh --agents
```

---

## Roadmap

Items are roughly ordered by priority.

### Near-term

| Item | Description |
|---|---|
| **Patroni install UI** | Button on Node Detail to trigger `patroni.install` task; executor already implemented |
| **Redis Sentinel config editor** | Dedicated UI panel (config push already works, no editor) |
| **Dashboard live status** | SSE-driven summary cards (connected agents, cluster health) instead of redirect to cluster list |

### Medium-term

| Item | Description |
|---|---|
| **NetBox upgrade orchestration** | One-click rolling upgrade: upgrade standby → validate → migrate primary → upgrade old primary |
| **Task timeout sweep** | Server background job to move stale `sent`/`ack` tasks to `timeout` status |
| **ClusterDetail topology graph** | Visual Patroni topology diagram |
| **Master key rotation** | CLI command to re-encrypt all secrets with a new AES key |

### Long-term / Wishlist

| Item | Description |
|---|---|
| **OAuth2 / LDAP / SAML** | External identity provider support |
| **Playwright E2E tests** | Browser-level integration test suite |
| **Additional media sync folders** | User-defined extra sync paths beyond MEDIA_ROOT |
| **Graceful shutdown drain** | Close WebSocket sessions cleanly on SIGTERM before process exit |

---

## Project Structure

```
netbox-conductor/
├── cmd/
│   ├── server/          # Conductor server binary entry point
│   └── agent/           # Agent binary entry point (task executor)
├── deployments/
│   ├── server/          # Server systemd unit, env template, Patroni witness script
│   └── agent/           # Agent systemd unit, install.sh, env template, sudoers, bundle.go
├── internal/
│   ├── agent/
│   │   ├── config/      # Agent env file loading and validation
│   │   ├── executor/    # Task implementations (config write, Patroni, media sync, etc.)
│   │   └── ws/          # WebSocket client (connect, reconnect, heartbeat)
│   ├── server/
│   │   ├── api/
│   │   │   ├── handlers/ # HTTP endpoint implementations
│   │   │   ├── middleware/ # Auth, audit, rate limiting
│   │   │   └── router.go  # All route registrations
│   │   ├── configgen/   # NetBox configuration.py template renderer
│   │   ├── crypto/      # AES-256-GCM encryption helpers
│   │   ├── db/
│   │   │   ├── migrations/ # SQL migration files (golang-migrate)
│   │   │   └── queries/    # DB query implementations
│   │   ├── hub/         # WebSocket session registry and dispatcher
│   │   ├── logging/     # Structured JSON logging, per-agent log files
│   │   ├── patroni/     # Witness subprocess management
│   │   ├── scheduler/   # Background jobs (health checks, task timeouts)
│   │   ├── sse/         # Server-Sent Events broker
│   │   └── tlscert/     # TLS cert auto-generation
│   └── shared/
│       └── protocol/    # WebSocket message types shared by server and agent
├── planning/            # Architecture docs, notes, current state snapshots
├── testing/             # Dev deploy scripts
└── web/
    └── src/
        ├── api/         # Axios API client modules
        ├── components/  # Shared React components
        ├── pages/       # Page-level React components
        └── store/       # Zustand state stores
```

---

## License

Private / internal tooling. Not currently licensed for public distribution.
