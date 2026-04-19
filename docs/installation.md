# Installation Guide

## Contents

- [Build Prerequisites](#build-prerequisites)
- [Get the Code and Build](#get-the-code-and-build)
- [Conductor Server Setup](#conductor-server-setup)
- [Agent Setup](#agent-setup-per-managed-node)
- [Cluster Credentials](#cluster-credentials)
- [Directory Permissions Reference](#directory-permissions-reference)
- [Master Key Rotation](#master-key-rotation)

---

## Build Prerequisites

The conductor server and build toolchain target **Linux (arm64/amd64)**. The agent binary also targets Linux only.

### Git, Make, and wget

```bash
sudo apt-get install -y git make wget
```

### Go 1.25+

```bash
# Check https://go.dev/dl/ for the latest 1.25.x release
wget https://go.dev/dl/go1.25.9.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.25.9.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
source /etc/profile.d/go.sh
go version
```

### Node.js 20+

```bash
curl -fsSL https://deb.nodesource.com/setup_22.x | sudo -E bash -
sudo apt-get install -y nodejs
node --version
```

### PostgreSQL 15+ (conductor host only)

```bash
sudo apt-get install -y postgresql postgresql-contrib
sudo systemctl enable --now postgresql
```

### Managed nodes (agent hosts)

The agent binary is statically compiled — no runtime dependencies are required on managed nodes beyond a standard Linux installation with systemd.

---

## Get the Code and Build

```bash
git clone https://github.com/averyhabbott/netbox-conductor.git
cd netbox-conductor

# Install frontend dependencies (once after cloning, and after package.json changes)
cd web && npm install && cd ..

# Build server (Linux arm64) + agent binaries (amd64 + arm64)
make build-all

# Output:
#   bin/netbox-conductor-linux-arm64
#   bin/netbox-agent-linux-amd64
#   bin/netbox-agent-linux-arm64
```

---

## Conductor Server Setup

Run all commands from the repository root (the directory containing `deployments/` and `bin/`).

**1. Run the install script:**

The install script creates the system user, all required directories, the conductor binary, the Patroni witness Python environment, and the systemd unit in one step. It auto-detects the host architecture (`amd64` or `arm64`) and picks the matching binary from `bin/`.

```bash
sudo bash deployments/server/install.sh
```

Then copy the agent binaries so managed nodes can download them:

```bash
sudo cp bin/netbox-agent-linux-amd64 /var/lib/netbox-conductor/bin/
sudo cp bin/netbox-agent-linux-arm64 /var/lib/netbox-conductor/bin/
sudo chmod +x /var/lib/netbox-conductor/bin/netbox-agent-linux-*
```

> The Conductor includes a built-in Raft witness process (`patroni_raft_controller`) that provides a third vote in 2-node clusters. It is installed automatically by the script into `/opt/netbox-conductor/venv` and runs as a subprocess, one per active/standby cluster. The process auto-restarts on crash and is recovered automatically when the conductor restarts.

**2. Create the database user and database:**

Generate a strong password and save it — you will need it in the next step:

```bash
openssl rand -hex 16
```

```bash
sudo -u postgres psql -c "CREATE USER netbox_conductor WITH PASSWORD '<generated password>';"
sudo -u postgres createdb -O netbox_conductor netbox_conductor
```

Migrations run automatically on first startup — no manual migration step needed.

**3. Generate the master key** (encrypts credentials at rest):

```bash
openssl rand -hex 32 | sudo tee /etc/netbox-conductor/master.key
sudo chmod 400 /etc/netbox-conductor/master.key
sudo chown netbox-conductor:netbox-conductor /etc/netbox-conductor/master.key
```

**4. Configure the environment:**

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
# PostgreSQL connection string — no password here; set it in DB_PASSWORD below
DATABASE_URL=postgres://netbox_conductor@localhost:5432/netbox_conductor?sslmode=disable

# Database password — injected into DATABASE_URL at startup (use the password generated in Step 2)
DB_PASSWORD=

# Secret used to sign JWT tokens — paste the output of: openssl rand -hex 32
JWT_SECRET=

# Address and port the server binds to (port 443 requires root or CAP_NET_BIND_SERVICE)
LISTEN_ADDR=:8443

# The conductor's reachable IP address — must be a valid IPv4 or IPv6 address, not a hostname.
# Used as the Patroni Raft witness listen address and, when SERVER_URL is not set, as the
# base URL included in agent ENV snippets. Required for active_standby (Patroni) clusters.
SERVER_BIND_IP=

# Public base URL included in agent ENV snippets (optional).
# If omitted, derived automatically from SERVER_BIND_IP and LISTEN_ADDR.
# Set this only if agents reach the conductor via a different address than SERVER_BIND_IP
# (e.g. a load balancer VIP or public DNS name).
# SERVER_URL=https://conductor.example.com

# Log directory and instance name — logs go to <LOG_DIR>/<LOG_NAME>/conductor.log
LOG_DIR=/var/log
LOG_NAME=netbox-conductor
LOG_LEVEL=info

# Path to the AES-256-GCM master key file generated in Step 3
NETBOX_CONDUCTOR_MASTER_KEY_FILE=/etc/netbox-conductor/master.key

# TLS cert and key — auto-generated as self-signed on first startup if absent.
# Download the CA from GET /api/v1/downloads/ca.crt and distribute to agent nodes,
# or set UPDATE_CERT=true on each agent to have it fetched automatically.
TLS_CERT_FILE=/etc/netbox-conductor/tls.crt
TLS_KEY_FILE=/etc/netbox-conductor/tls.key
```

**5. Start the service:**

```bash
sudo systemctl enable --now netbox-conductor
sudo journalctl -u netbox-conductor -f
```

The UI is available at `https://<conductor>:8443`. On first start against an empty database, a default admin account is automatically created:

- **Username:** `admin`
- **Password:** `changeme123!`

Change this password immediately after first login.

---

## Agent Setup (per managed node)

### Option A — Download from Conductor UI (recommended)

1. In the Conductor UI, navigate to your cluster and click **Add Node**
2. Complete Step 1 (hostname, IP, role) and Step 2 to download and run the installer:

   ```bash
   curl -fsSLk https://conductor.example.com:8443/api/v1/downloads/agent-linux-amd64 \
     -o netbox-agent.tar.gz
   tar -xzf netbox-agent.tar.gz
   sudo bash install.sh
   ```

3. The tarball includes the agent binary, `install.sh`, `netbox-agent.service`, `netbox-agent-sudoers`, and `netbox-agent.env.example`
4. Step 3 in the UI generates a pre-filled env snippet — copy it to `/etc/netbox-agent/netbox-agent.env`, then:

   ```bash
   sudo systemctl start netbox-agent
   ```

### Option B — Manual

```bash
sudo groupadd --system netbox-agent
sudo useradd --system --gid netbox-agent --no-create-home --shell /usr/sbin/nologin netbox-agent

sudo cp netbox-agent /usr/local/bin/netbox-agent
sudo chmod +x /usr/local/bin/netbox-agent

sudo cp netbox-agent.service /etc/systemd/system/
sudo install -m 440 netbox-agent-sudoers /etc/sudoers.d/netbox-agent

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
AGENT_SERVER_URL=wss://conductor.example.com:8443/api/v1/agent/connect

# TLS — cert-learning fetches and saves the CA cert automatically on first start
UPDATE_CERT=true
# To supply the CA cert manually: UPDATE_CERT=false and set AGENT_TLS_CA_CERT=/etc/netbox-agent/ca.crt
# For development only (insecure): AGENT_TLS_SKIP_VERIFY=true

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

The node appears as **Connected** in the Conductor UI once the agent authenticates.

---

## Cluster Credentials

Each cluster stores eight encrypted credential kinds. They are set per-cluster under the **Credentials** tab in the UI.

| Kind | Description |
| --- | --- |
| `postgres_superuser` | PostgreSQL superuser (`postgres`) password |
| `postgres_replication` | Replication user (`replicator`) password used by Patroni |
| `netbox_db_user` | NetBox application database user password |
| `redis_tasks_password` | Redis password for the NetBox task queue |
| `redis_caching_password` | Redis password for the NetBox caching backend |
| `netbox_secret_key` | NetBox `SECRET_KEY` — changing this invalidates all sessions |
| `netbox_api_token_pepper` | NetBox `API_TOKEN_PEPPERS[0]` — changing this invalidates all API tokens |
| `patroni_rest_password` | HTTP basic-auth password for the Patroni REST API |

Credentials are never stored or transmitted in plaintext — they are encrypted at rest with the AES-256-GCM master key generated during conductor setup.

### Setting credentials for a new cluster

**Auto-Generate** (fresh installs): Click **Auto-Generate Credentials** on the Credentials tab. The Conductor generates cryptographically random passwords for all credential kinds that have a default username and stores them encrypted. The plaintext values are shown once — copy them before closing the dialog.

**Import From Existing** (migrating an existing NetBox instance): Click **Import From Existing** on the Credentials tab. Select a connected node; the Conductor reads `configuration.py` from that node and extracts the current `SECRET_KEY`, `API_TOKEN_PEPPERS`, database password, and Redis passwords. Review the detected values, select which to import, and click **Import**. The values are stored encrypted without requiring manual copy-paste.

After credentials are set, use the **Config Editor** to render and push `configuration.py` to nodes, or use **Sync Config** (Settings → General) to pull the live config from a source node and push it to destination nodes.

---

## Directory Permissions Reference

The `install.sh` script handles all of these automatically. For reference:

| Path | Setup required |
| --- | --- |
| `/opt/netbox/netbox/netbox/` | `usermod -aG netbox netbox-agent` + `chown :netbox <dir>` + `chmod g+ws <dir>` — setgid ensures files written by the agent inherit the `netbox` group and are readable by gunicorn and netbox-rq |
| `/etc/patroni` | `mkdir -p`, `chown netbox-agent:netbox-agent`, `chmod 750` |
| Redis Sentinel config | `usermod -aG redis netbox-agent` |

> **Important:** The NetBox config directory group must be `netbox` (not `netbox-agent`) so that the `netbox` user running gunicorn and netbox-rq can read `configuration.py` after the agent writes it.

---

## Master Key Rotation

To re-encrypt all secrets at rest with a new AES-256-GCM key:

```bash
DATABASE_URL=postgres://... \
NETBOX_CONDUCTOR_MASTER_KEY_FILE=/etc/netbox-conductor/master.key \
NEW_MASTER_KEY_FILE=/etc/netbox-conductor/master.key.new \
  rotate-key
```

Without `--in-place`, the new key is written to `NEW_MASTER_KEY_FILE`. Swap the file and restart the conductor once you have verified the new key. With `--in-place`, the current key file is overwritten on success.

All re-encryption runs in a single transaction — it either fully succeeds or rolls back with no changes.
