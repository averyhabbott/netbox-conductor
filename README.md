# NetBox Conductor

A management platform for orchestrating multi-node [NetBox](https://netboxlabs.com/) deployments. NetBox Conductor coordinates configuration, service lifecycle, database high availability (via Patroni), Redis Sentinel, and media sync across a cluster of NetBox servers — all from a single web UI.

---

## How It Works

**Conductor** is a single Go binary that serves the web UI and API. Each managed NetBox server runs a lightweight **agent** binary that maintains a persistent outbound WebSocket connection to the Conductor. The Conductor dispatches tasks (write config, restart service, push Patroni config, etc.) to agents over that connection; agents report back heartbeats, service state, and task results.

No inbound firewall rules are needed on agent hosts — all traffic is agent-initiated.

```
┌──────────────────────────────────────────────────────────────────┐
│                       NetBox Conductor                           │
│  (web UI + API + task dispatcher + Patroni witness)              │
└──────────────────┬───────────────────────────────────────────────┘
                   │ wss:// (outbound from agents)
          ┌────────┴─────────┐
          │                  │
┌─────────▼──────┐  ┌────────▼────────┐
│    nbfa-1      │  │    nbfa-2       │
│  netbox-agent  │  │  netbox-agent   │
│  Patroni       │  │  Patroni        │
│  PostgreSQL    │  │  PostgreSQL     │
│  Redis         │  │  Redis          │
└────────────────┘  └─────────────────┘
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

### PostgreSQL HA — Configure Failover

The **Configure Failover** button on a cluster's Settings tab is the single-step HA setup. It:

1. Saves failover settings to the database
2. Auto-generates any missing credentials (postgres superuser, replication user, Patroni REST password, Redis password)
3. Starts the Conductor's built-in Patroni witness subprocess — the third Raft voter that gives 2-node clusters quorum without a third data node
4. Optionally runs `pg_dump` on the primary database before making any changes
5. Renders and pushes `patroni.yml` to every node (with per-node Raft peer lists and witness address)
6. Restarts the Patroni service on each node
7. If `App Tier Always Available` is enabled: renders and pushes Redis Sentinel config to every node, and pre-seeds the correct `DATABASE.HOST` on all nodes
8. Marks the cluster as configured

**Patroni RAFT consensus** uses the built-in DCS (no external etcd/Consul required). The Conductor acts as the third Raft member via `patroni_raft_controller`, providing quorum in 2-node clusters without a dedicated witness VM.

**`failsafe_mode: true`** is set in all generated configs — the primary continues serving if it loses contact with the standby, preventing a split-brain shutdown in a 2-node cluster.

### Automatic Failover & Failback
- Triggered by agent disconnect, heartbeat reporting NetBox stopped, or entering maintenance mode
- Configurable grace period (default 30 s) before failover fires — allows transient outages to self-heal
- 90-second startup suppression window after Conductor restart (prevents mass-trigger when all agents reconnect)
- Split-brain prevention: `stop.netbox` is queued for the failed node and executed on reconnect
- Maintenance-mode failover: moves NetBox off a node the moment it enters maintenance
- Auto-failback: when a higher-priority node reconnects and is healthy, NetBox moves back after one grace period
- All events (failover, failback, maintenance moves) are recorded in the **History** tab with trigger, from/to node, and outcome

### App Tier Always Available

When enabled, all nodes always run NetBox — there is no single "active" node. The load balancer steers using `GET /status` and Patroni handles the database primary election independently.

- All nodes' `configuration.py` points `DATABASE.HOST` at the current Patroni primary's IP
- When Patroni elects a new primary (via `patroni.state` message from the agent), the Conductor automatically dispatches `config.update_db_host` to all connected cluster nodes to update `DATABASE.HOST` and restart NetBox
- Redis Sentinel provides Redis HA; all nodes connect to the Sentinel master

When disabled (active/standby), `configuration.py` always points `DATABASE.HOST` to `localhost`. Only one node runs NetBox at a time; the LB steers based on `GET /status` alone.

### Health Checks (`GET /status`)

The agent serves a lightweight health endpoint for VIP health checkers and reverse proxies:

| Mode | 200 OK condition |
|---|---|
| Patroni not configured | `netbox.service` is active |
| App tier always available | `netbox.service` is active |
| Active/standby + Patroni | `netbox.service` active **AND** local Patroni `/primary` returns 200 |

In active/standby mode the `/status` check is gated on Patroni primary status. This ensures the load balancer routes only to the node whose local PostgreSQL is writable — no traffic reaches a replica even if NetBox is still running there during a transition.

Response body always includes `patroni_primary` when relevant:
```json
{"status":"ok","netbox":true,"rq":true,"node_id":"<uuid>","patroni_primary":true}
```

### Redis Sentinel
- Push Sentinel configuration across the cluster
- Sentinel addresses auto-derived from node IPs (port 26379)
- Auth password stored as an encrypted cluster credential

### Media Sync
- Relay NetBox media files between nodes through the Conductor (no direct node-to-node SSH required)
- Chunked transfer with backpressure
- Extra sync folders configurable per cluster

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
- **Failover history tab** per cluster: time, type, trigger, from/to node, outcome

---

## Architecture

| Component | Technology |
|-----------|-----------|
| Server | Go 1.25 + Echo v4 |
| Frontend | React 18 + TypeScript + Vite + Tailwind CSS v4 |
| Database | PostgreSQL (pgx/v5 driver) |
| Agent protocol | WebSocket + JSON envelopes |
| HA | Patroni (built-in Raft DCS), Redis Sentinel |
| Auth | JWT (RS256), bcrypt, AES-256-GCM, TOTP |

The server binary embeds the compiled frontend (`go:embed`), so there is only one file to deploy. The agent is a separate static binary compiled for the target platform.

---

## Installation

### Server Prerequisites

The conductor server and build toolchain are tested on **Linux (arm64/amd64)** and **macOS**. The agent binary targets Linux only.

#### Git and Make

**macOS:**

```bash
brew install git make
```

**Linux (Ubuntu/Debian):**

```bash
sudo apt-get install -y git make
```

#### Go 1.25+

**macOS:**

```bash
brew install go
```

**Linux (Ubuntu/Debian):**

```bash
# Download the official tarball — check https://go.dev/dl/ for the latest 1.25.x release
wget https://go.dev/dl/go1.25.9.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.25.9.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
source /etc/profile.d/go.sh
go version
```

#### Node.js 20+

**macOS:**

```bash
brew install node
```

**Linux (Ubuntu/Debian):**

```bash
curl -fsSL https://deb.nodesource.com/setup_22.x | sudo -E bash -
sudo apt-get install -y nodejs
node --version
```

#### PostgreSQL 15+ (conductor server host only)

**macOS:**

```bash
brew install postgresql@15
brew services start postgresql@15
```

**Linux (Ubuntu/Debian):**

```bash
sudo apt-get install -y postgresql postgresql-contrib
sudo systemctl enable --now postgresql
```

#### Managed nodes (agent hosts)

The agent binary is statically compiled — no runtime dependencies are required on managed nodes beyond a standard Linux installation with systemd.

---

### Get the Code

```bash
git clone https://github.com/averyhabbott/netbox-conductor.git
cd netbox-conductor
```

### Install Frontend Dependencies

```bash
cd web && npm install && cd ..
```

This only needs to be run once after cloning (and again after pulling changes that update `web/package.json`).

### Build

```bash
# Build server (Linux arm64) + agent binaries (amd64 + arm64)
make build-all

# Output:
#   bin/netbox-conductor-linux-arm64
#   bin/netbox-agent-linux-amd64
#   bin/netbox-agent-linux-arm64
```

### Server Setup

Run all commands from the repository root (the directory that contains `deployments/` and `bin/`):

```bash
cd /path/to/netbox-conductor
```

**1. Create the system user and directories:**

```bash
sudo groupadd --system netbox-conductor
sudo useradd --system --gid netbox-conductor --no-create-home --shell /usr/sbin/nologin netbox-conductor

sudo mkdir -p /opt/netbox-conductor/bin
sudo mkdir -p /etc/netbox-conductor
sudo mkdir -p /var/log/netbox-conductor
sudo chown netbox-conductor:netbox-conductor /etc/netbox-conductor
sudo chown netbox-conductor:netbox-conductor /var/log/netbox-conductor
```

**2. Copy the binary:**

```bash
sudo cp bin/netbox-conductor-linux-arm64 /opt/netbox-conductor/bin/netbox-conductor
sudo chmod +x /opt/netbox-conductor/bin/netbox-conductor
```

**3. Copy agent binaries** (served to managed nodes via download endpoint):

```bash
sudo mkdir -p /var/lib/netbox-conductor/bin
sudo cp bin/netbox-agent-linux-amd64 /var/lib/netbox-conductor/bin/
sudo cp bin/netbox-agent-linux-arm64 /var/lib/netbox-conductor/bin/
sudo chmod +x /var/lib/netbox-conductor/bin/netbox-agent-linux-*
```

**4. Create the database user and database:**

Generate a strong database password and save it — you will need it in the next step:

```bash
openssl rand -hex 16
```

**macOS:**

```bash
createuser --pwprompt netbox_conductor   # enter the generated password when prompted
createdb -O netbox_conductor netbox_conductor
```

**Linux:**

```bash
sudo -u postgres psql -c "CREATE USER netbox_conductor WITH PASSWORD '<generated password>';"
sudo -u postgres createdb -O netbox_conductor netbox_conductor
```

Migrations run automatically on first startup — no manual migration step needed.

**5. Generate the master key** (encrypts credentials at rest):

```bash
openssl rand -hex 32 | sudo tee /etc/netbox-conductor/master.key
sudo chmod 400 /etc/netbox-conductor/master.key
sudo chown netbox-conductor:netbox-conductor /etc/netbox-conductor/master.key
```

**6. Configure the environment:**

```bash
sudo cp deployments/server/netbox-conductor.env.example /etc/netbox-conductor/netbox-conductor.env
sudo chown root:netbox-conductor /etc/netbox-conductor/netbox-conductor.env
sudo chmod 640 /etc/netbox-conductor/netbox-conductor.env
```

Generate a JWT signing secret:

```bash
openssl rand -hex 32
```

Edit `/etc/netbox-conductor/netbox-conductor.env` and fill in the required values:

```bash
# ── Required ──────────────────────────────────────────────────────────────────

# PostgreSQL connection string — use the password generated in Step 4
DATABASE_URL=postgres://netbox_conductor:<password>@localhost:5432/netbox_conductor?sslmode=disable

# Secret used to sign JWT tokens — paste the output of: openssl rand -hex 32
JWT_SECRET=<openssl output>

# ── Network ───────────────────────────────────────────────────────────────────

# Address and port the server binds to.
# Default is 8443 — port 443 (standard HTTPS) requires root or CAP_NET_BIND_SERVICE,
# which the netbox-conductor service user does not have.
LISTEN_ADDR=:8443

# Public base URL advertised to operators in agent ENV snippets.
# Port is taken from LISTEN_ADDR automatically if not specified here.
SERVER_URL=https://your-conductor.example.com

# ── Logging ───────────────────────────────────────────────────────────────────

# Root directory for log files
LOG_DIR=/var/log

# Instance name — becomes the top-level log subdirectory (<LOG_DIR>/<LOG_NAME>/)
LOG_NAME=netbox-conductor

# Minimum log level: debug, info, warn, error
LOG_LEVEL=info

# ── Encryption ────────────────────────────────────────────────────────────────

# Path to the 32-byte AES-256-GCM master key file — generated in Step 5
NETBOX_CONDUCTOR_MASTER_KEY_FILE=/etc/netbox-conductor/master.key

# ── TLS ───────────────────────────────────────────────────────────────────────

# On first startup the conductor auto-generates a self-signed ECDSA P-256 cert
# if these files do not exist. Download the CA from /api/v1/downloads/ca.crt
# and distribute to agent nodes (or use UPDATE_CERT=true on agents).
TLS_CERT_FILE=/etc/netbox-conductor/tls.crt
TLS_KEY_FILE=/etc/netbox-conductor/tls.key
```

**7. Install and start the systemd service:**

```bash
sudo cp deployments/server/netbox-conductor.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now netbox-conductor
sudo journalctl -u netbox-conductor -f
```

The UI is available at `https://<server>:8443`. On first start, create your admin account via the login page (if no users exist, the first registration is granted admin).

---

### Agent Setup (per managed node)

There are two ways to install the agent on a managed node:

**Option A — Download tarball from Conductor UI (recommended):**

1. In the Conductor UI, navigate to your cluster and click **Add Node**
2. Complete Step 1 (hostname, IP, role) and Step 2 (install agent):
   ```bash
   curl -fsSLk https://your-conductor.example.com:8443/api/v1/downloads/agent-linux-amd64 \
     -o netbox-agent.tar.gz
   tar -xzf netbox-agent.tar.gz
   sudo bash install.sh
   ```
3. The tarball includes: agent binary, `install.sh`, `netbox-agent.service`, `netbox-agent-sudoers`, and `netbox-agent.env.example`
4. Step 3 in the UI generates the env snippet — copy it to `/etc/netbox-agent/netbox-agent.env` on the node, then:
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

# Create config dir — must be owned by the agent (cert-learning writes here)
sudo mkdir -p /etc/netbox-agent
sudo cp netbox-agent.env.example /etc/netbox-agent/netbox-agent.env
sudo chown -R netbox-agent:netbox-agent /etc/netbox-agent
sudo chmod 600 /etc/netbox-agent/netbox-agent.env
```

Edit `/etc/netbox-agent/netbox-agent.env`:

```bash
AGENT_NODE_ID=<uuid from Conductor>
AGENT_TOKEN=<token from Conductor>

# WebSocket URL — must use wss:// and include the port if not on 443
AGENT_SERVER_URL=wss://your-conductor.example.com:8443/api/v1/agent/connect

# TLS — cert-learning handles this automatically by default:
# On first start the agent downloads the conductor's CA cert, saves it to
# /etc/netbox-agent/ca.crt, updates this env file, and switches to verified TLS.
UPDATE_CERT=true

# To supply the CA cert manually instead:
# UPDATE_CERT=false
# AGENT_TLS_CA_CERT=/etc/netbox-agent/ca.crt

# For development only (insecure — not for production):
# AGENT_TLS_SKIP_VERIFY=true

# NetBox paths (adjust to your installation)
NETBOX_CONFIG_PATH=/opt/netbox/netbox/netbox/configuration.py
NETBOX_MEDIA_ROOT=/opt/netbox/netbox/media

# Patroni REST API (default is fine for standard install)
PATRONI_REST_URL=http://127.0.0.1:8008
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now netbox-agent
```

The node will appear as **Connected** in the Conductor UI once the agent authenticates.

---

### Reverse Proxy Health Checks

The agent exposes a lightweight health check endpoint that reverse proxies and VIP health-checkers use to steer traffic.

```
GET https://<node>/status
```

#### Health check logic

| Cluster state | Returns 200 if… |
|---|---|
| Patroni not yet configured | `netbox.service` is active |
| App tier always available | `netbox.service` is active (all nodes eligible) |
| Active/standby + Patroni configured | `netbox.service` active **AND** `GET http://127.0.0.1:8008/primary` returns 200 |

In active/standby mode the Patroni primary check ensures the load balancer never routes to a replica node, even if NetBox is still in the process of shutting down during a failover transition.

Response body:
```json
{"status":"ok","netbox":true,"rq":true,"node_id":"<uuid>","patroni_primary":true}
```
`patroni_primary` is omitted when Patroni is not configured or the cluster is always-available.

#### How it works

The agent binds the status server to `127.0.0.1:8081` (loopback only) by default. The node's nginx or Apache reverse proxy exposes `GET /status` on the public HTTPS port. Health checkers probe `https://<node>/status` and never need direct access to the agent port.

**Setup** — copy the provided example config to the node's reverse proxy:

```bash
# nginx (Debian/Ubuntu)
sudo cp deployments/agent/nginx-netbox-conductor.conf /etc/nginx/sites-available/netbox
sudo ln -s /etc/nginx/sites-available/netbox /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx

# Apache (Debian/Ubuntu)
sudo cp deployments/agent/apache-netbox-conductor.conf /etc/apache2/sites-available/netbox.conf
sudo a2enmod proxy proxy_http ssl rewrite headers
sudo a2ensite netbox
sudo apache2ctl configtest && sudo systemctl reload apache2
```

These configs are drop-in replacements for the standard NetBox nginx/Apache configs. They add a single `location = /status` block (nginx) or `<Location /status>` block (Apache) that proxies requests to `127.0.0.1:8081`.

> **SELinux (RHEL/CentOS/Rocky):** if nginx or Apache cannot connect to 127.0.0.1:8081, enable the policy:
>
> ```bash
> sudo setsebool -P httpd_can_network_connect 1
> ```

> **Direct agent port access:** If your load balancer must probe the agent port directly (e.g. a remote HAProxy with no access to port 443), set `AGENT_STATUS_ADDR=0.0.0.0:8081` in the agent env file. The endpoint is then reachable at `http://<node>:8081/status`.

Controlled by `AGENT_STATUS_ADDR` in the agent env file (default `127.0.0.1:8081`; empty string disables the server). The legacy `AGENT_STATUS_PORT` integer variable is still accepted for backward compatibility.

#### HAProxy

HAProxy checks `https://<node>/status` on port 443 — the same port as application traffic, served by the node's reverse proxy.

```haproxy
frontend netbox_frontend
    bind *:443 ssl crt /etc/ssl/certs/haproxy.pem
    default_backend netbox_backends

backend netbox_backends
    option httpchk GET /status
    http-check expect status 200

    # Both nodes checked via HTTPS; nb-2 is standby (backup)
    server nb-1 nb-1.example.com:443 ssl verify none check inter 10s fall 2 rise 1
    server nb-2 nb-2.example.com:443 ssl verify none check inter 10s fall 2 rise 1 backup
```

HAProxy marks a server down after 2 consecutive failed checks (`fall 2`) and brings it back after 1 passing check (`rise 1`). In active/standby mode only `nb-1` receives traffic; `nb-2` takes over automatically when `nb-1` returns 503.

---

### Directory Permissions (managed nodes)

The `install.sh` script handles all of these automatically. For reference:

| Path | Setup required |
|---|---|
| `/opt/netbox/netbox/netbox/` | `usermod -aG netbox netbox-agent` + `chown :netbox <dir>` + `chmod g+ws <dir>` — setgid ensures files written by the agent inherit the `netbox` group and are readable by gunicorn and netbox-rq |
| `/etc/patroni` | `mkdir -p`, `chown netbox-agent:netbox-agent`, `chmod 750` |
| Redis Sentinel config | `usermod -aG redis netbox-agent` |

> **Important:** The NetBox config directory group must be `netbox` (not `netbox-agent`) so that the `netbox` user running gunicorn and netbox-rq can read `configuration.py` after the agent writes it. Do not use `chown netbox-agent:netbox-agent` on this directory.

---

### Master Key Rotation

To re-encrypt all secrets at rest with a new AES-256-GCM key:

```bash
DATABASE_URL=postgres://... \
NETBOX_CONDUCTOR_MASTER_KEY_FILE=/etc/netbox-conductor/master.key \
NEW_MASTER_KEY_FILE=/etc/netbox-conductor/master.key.new \
  rotate-key
```

Without `--in-place`, the new key is written to `NEW_MASTER_KEY_FILE`. Swap the file and restart the conductor once you have verified the new key. With `--in-place`, the current key file is overwritten on success.

All re-encryption runs in a single transaction — it either fully succeeds or rolls back with no changes.

---

## Development

```bash
# 1. Start PostgreSQL and create the dev database
createdb netbox_conductor_dev

# 2. Copy and edit the server env
cp deployments/server/netbox-conductor.env.example .env
# Edit DATABASE_URL, JWT_SECRET
# Set SERVER_URL=https://localhost  (port is taken from LISTEN_ADDR automatically)

# 3. Start the backend (auto-runs migrations, serves API on :8443 by default)
make dev

# 4. In a separate terminal, start the frontend dev server (proxies /api to backend)
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
# Build and push conductor to nb-conductor@orb
bash testing/deploy.sh

# Also push agent binary, service file, and sudoers to nb-1 and nb-2
bash testing/deploy.sh --agents
```

---

## Roadmap

Items are roughly ordered by priority.

### Near-term

| Item | Description |
|---|---|
| **Failover outcome verification** | After dispatching `start.netbox` to a candidate, confirm via heartbeat; retry next candidate if the first fails within a configurable window |
| **Restore from backup** | UI to trigger `pg_dump` restore from a previously saved backup file |
| **Patroni install UI** | Button on Node Detail to trigger `patroni.install` task; executor already implemented |

### Medium-term

| Item | Description |
|---|---|
| **NetBox upgrade orchestration** | One-click rolling upgrade: upgrade standby → validate → migrate primary → upgrade old primary |
| **Cluster-wide failover freeze** | Operator-toggled flag to suppress all automatic failovers during maintenance windows |
| **Failback safety checks** | Verify replica lag before failing back to a reconnected node |
| **Persistent failover history export** | CSV/JSON download of the failover events table |

### Long-term / Wishlist

| Item | Description |
|---|---|
| **OAuth2 / LDAP / SAML** | External identity provider support |
| **Playwright E2E tests** | Browser-level integration test suite |
| **Graceful shutdown drain** | Close WebSocket sessions cleanly on SIGTERM before process exit |

---

## Project Structure

```
netbox-conductor/
├── cmd/
│   ├── server/          # Conductor server binary entry point
│   ├── agent/           # Agent binary entry point (task executor)
│   └── rotate-key/      # Offline key-rotation tool (re-encrypts all secrets)
├── deployments/
│   ├── server/          # Server systemd unit, env template, Patroni witness script
│   └── agent/           # Agent systemd unit, install.sh, env template, sudoers, bundle.go
├── internal/
│   ├── agent/
│   │   ├── config/      # Agent env file loading, validation, and cert-learning
│   │   ├── executor/    # Task implementations (config write, db host update, Patroni, media sync, upgrade, etc.)
│   │   ├── statusserver/ # Local HTTP health endpoint (Patroni-aware in active/standby mode)
│   │   └── ws/          # WebSocket client (connect, reconnect, heartbeat, OnServerHello callback)
│   ├── server/
│   │   ├── api/
│   │   │   ├── handlers/ # HTTP endpoint implementations
│   │   │   ├── middleware/ # Auth, audit, rate limiting
│   │   │   └── router.go  # All route registrations
│   │   ├── configgen/   # NetBox configuration.py and Patroni/Sentinel config renderers
│   │   ├── crypto/      # AES-256-GCM encryption helpers
│   │   ├── db/
│   │   │   ├── migrations/ # SQL migration files (golang-migrate)
│   │   │   └── queries/    # DB query implementations (clusters, nodes, failover_events, …)
│   │   ├── failover/    # Automatic failover/failback manager; records events to failover_events table
│   │   ├── hub/         # WebSocket session registry and dispatcher
│   │   ├── logging/     # Structured JSON logging, per-agent log files
│   │   ├── patroni/     # Witness subprocess management (patroni_raft_controller)
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
        ├── pages/       # Page-level React components (ClusterDetail, Dashboard, …)
        └── store/       # Zustand state stores
```

---

## License

Private / internal tooling. Not currently licensed for public distribution.
