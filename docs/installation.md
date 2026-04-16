# Installation Guide

## Contents

- [Build Prerequisites](#build-prerequisites)
- [Get the Code and Build](#get-the-code-and-build)
- [Conductor Server Setup](#conductor-server-setup)
- [Agent Setup](#agent-setup-per-managed-node)
- [Directory Permissions Reference](#directory-permissions-reference)
- [Master Key Rotation](#master-key-rotation)

---

## Build Prerequisites

The conductor server and build toolchain are tested on **Linux (arm64/amd64)** and **macOS**. The agent binary targets Linux only.

### Git and Make

**macOS:**

```bash
brew install git make
```

**Linux (Ubuntu/Debian):**

```bash
sudo apt-get install -y git make
```

### Go 1.25+

**macOS:**

```bash
brew install go
```

**Linux (Ubuntu/Debian):**

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

### PostgreSQL 15+ (conductor host only)

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

**3. Set up the Patroni witness Python environment:**

The Conductor includes a built-in Patroni Raft witness process that provides a third vote in 2-node clusters. It is a Python script that requires `pysyncobj`.

```bash
# Debian/Ubuntu: install the venv package first (version must match your system Python)
sudo apt-get install -y python3-venv
# If the above fails (e.g. Python 3.13), use the version-specific package:
#   sudo apt-get install -y python3.13-venv

# Create the venv and install pysyncobj
sudo python3 -m venv /opt/netbox-conductor/venv
sudo /opt/netbox-conductor/venv/bin/pip install pysyncobj

# Deploy the witness script
sudo cp deployments/server/patroni-witness.py /opt/netbox-conductor/patroni-witness.py
sudo chmod 755 /opt/netbox-conductor/patroni-witness.py
sudo chown -R netbox-conductor:netbox-conductor /opt/netbox-conductor/venv
sudo chown netbox-conductor:netbox-conductor /opt/netbox-conductor/patroni-witness.py
```

> Patroni's built-in Raft DCS is implemented on top of `pysyncobj`. The Conductor spawns a witness subprocess so that a 2-node cluster has a 3rd Raft voter on a separate host, preserving quorum in a network partition. Without `pysyncobj` installed, the witness exits immediately and the cluster may fail to elect a primary.

**4. Copy agent binaries** (served to managed nodes via download endpoint):

```bash
sudo mkdir -p /var/lib/netbox-conductor/bin
sudo cp bin/netbox-agent-linux-amd64 /var/lib/netbox-conductor/bin/
sudo cp bin/netbox-agent-linux-arm64 /var/lib/netbox-conductor/bin/
sudo chmod +x /var/lib/netbox-conductor/bin/netbox-agent-linux-*
```

**5. Create the database user and database:**

Generate a strong password and save it — you will need it in the next step:

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

**6. Generate the master key** (encrypts credentials at rest):

```bash
openssl rand -hex 32 | sudo tee /etc/netbox-conductor/master.key
sudo chmod 400 /etc/netbox-conductor/master.key
sudo chown netbox-conductor:netbox-conductor /etc/netbox-conductor/master.key
```

**7. Configure the environment:**

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
# PostgreSQL connection string — use the password generated in Step 5
DATABASE_URL=postgres://netbox_conductor:<password>@localhost:5432/netbox_conductor?sslmode=disable

# Secret used to sign JWT tokens — paste the output of: openssl rand -hex 32
JWT_SECRET=<openssl output>

# Address and port the server binds to (port 443 requires root or CAP_NET_BIND_SERVICE)
LISTEN_ADDR=:8443

# Public base URL advertised to operators in agent ENV snippets
SERVER_URL=https://conductor.example.com

# Log directory and instance name — logs go to <LOG_DIR>/<LOG_NAME>/conductor.log
LOG_DIR=/var/log
LOG_NAME=netbox-conductor
LOG_LEVEL=info

# Path to the AES-256-GCM master key file generated in Step 6
NETBOX_CONDUCTOR_MASTER_KEY_FILE=/etc/netbox-conductor/master.key

# TLS cert and key — auto-generated as self-signed on first startup if absent.
# Download the CA from GET /api/v1/downloads/ca.crt and distribute to agent nodes,
# or set UPDATE_CERT=true on each agent to have it fetched automatically.
TLS_CERT_FILE=/etc/netbox-conductor/tls.crt
TLS_KEY_FILE=/etc/netbox-conductor/tls.key
```

**8. Install and start the systemd service:**

```bash
sudo cp deployments/server/netbox-conductor.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now netbox-conductor
sudo journalctl -u netbox-conductor -f
```

The UI is available at `https://<conductor>:8443`. On first start, create your admin account via the login page — the first registration on a fresh database is automatically granted admin.

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

## Directory Permissions Reference

The `install.sh` script handles all of these automatically. For reference:

| Path | Setup required |
|---|---|
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
