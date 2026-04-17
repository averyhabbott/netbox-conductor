#!/usr/bin/env bash
# NetBox Conductor Agent — installer
# Usage: sudo bash install.sh
set -euo pipefail

BIN_DEST=/usr/local/bin/netbox-agent
ENV_DIR=/etc/netbox-agent
SERVICE_FILE=/etc/systemd/system/netbox-agent.service

if [[ $EUID -ne 0 ]]; then
  echo "Run as root: sudo bash install.sh" >&2
  exit 1
fi

echo "→ Creating netbox-agent user/group"
groupadd --system netbox-agent 2>/dev/null || true
useradd --system --gid netbox-agent --no-create-home --shell /usr/sbin/nologin netbox-agent 2>/dev/null || true

echo "→ Installing binary to $BIN_DEST"
install -m 755 netbox-agent "$BIN_DEST"

echo "→ Creating $ENV_DIR"
mkdir -p "$ENV_DIR"
chown netbox-agent:netbox-agent "$ENV_DIR"
chmod 750 "$ENV_DIR"
if [[ ! -f "$ENV_DIR/netbox-agent.env" ]]; then
  install -m 600 netbox-agent.env.example "$ENV_DIR/netbox-agent.env"
  chown netbox-agent:netbox-agent "$ENV_DIR/netbox-agent.env"
  echo "  Created $ENV_DIR/netbox-agent.env"
  echo "  Fill in AGENT_NODE_ID, AGENT_TOKEN, and AGENT_SERVER_URL before starting."
  echo "  (Or download a pre-filled env file from the Conductor UI.)"
else
  # Ensure agent owns the env file so cert-learning can update it.
  chown netbox-agent:netbox-agent "$ENV_DIR/netbox-agent.env"
  chmod 600 "$ENV_DIR/netbox-agent.env"
  echo "  $ENV_DIR/netbox-agent.env already exists — permissions updated"
fi

echo "→ Setting up managed directories"

# ── NetBox configuration directory ──────────────────────────────────────────
# The agent writes configuration.py here; NetBox services read it.
# Strategy: add netbox-agent to the 'netbox' group, then set setgid + group-write
# on the directory so files written by the agent are readable by the netbox user.
NB_CONFIG=""
if [[ -f "$ENV_DIR/netbox-agent.env" ]]; then
  NB_CONFIG=$(grep -E "^NETBOX_CONFIG_PATH=" "$ENV_DIR/netbox-agent.env" | head -1 | cut -d= -f2-)
fi
if [[ -z "$NB_CONFIG" ]]; then
  for p in /opt/netbox/netbox/netbox/configuration.py /opt/netbox-*/netbox/netbox/configuration.py; do
    if [[ -f "$p" ]]; then
      NB_CONFIG="$p"
      break
    fi
  done
fi
if [[ -n "$NB_CONFIG" ]]; then
  NB_DIR=$(dirname "$NB_CONFIG")
  echo "  NetBox config dir: $NB_DIR"
  if getent group netbox >/dev/null 2>&1; then
    usermod -aG netbox netbox-agent
    chown :netbox "$NB_DIR"
    chmod g+ws "$NB_DIR"
    echo "  Added netbox-agent to 'netbox' group; $NB_DIR is now group-writable (setgid)"
  else
    echo "  WARNING: 'netbox' group not found — skipping NetBox directory setup"
    echo "  Run manually: usermod -aG <netbox-group> netbox-agent && chmod g+ws $NB_DIR"
  fi
else
  echo "  WARNING: NetBox installation not detected"
  echo "  After setting NETBOX_CONFIG_PATH in $ENV_DIR/netbox-agent.env, re-run install.sh"
fi

# ── Patroni configuration directory ─────────────────────────────────────────
# Create if absent, owned by agent
mkdir -p /etc/patroni
chown netbox-agent:netbox-agent /etc/patroni
chmod 750 /etc/patroni

# ── HA packages: Patroni and Redis Sentinel ──────────────────────────────────
# Install at agent-setup time (running as root) so the patroni.install task
# never needs sudo. Enable patroni once here — no need to enable it before
# every restart in the task handler.
echo "→ Installing HA packages (patroni, redis-sentinel)"
if command -v apt-get >/dev/null 2>&1; then
  apt-get install -y patroni redis-sentinel
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y patroni redis-sentinel
elif command -v yum >/dev/null 2>&1; then
  yum install -y patroni redis-sentinel
else
  echo "  WARNING: no supported package manager found — install patroni and redis-sentinel manually"
fi
systemctl enable patroni 2>/dev/null || true

# ── pysyncobj deps directory for Patroni Raft DCS ────────────────────────────
# Pre-create a directory owned by netbox-agent so patroni.install can pip-install
# pysyncobj without sudo. A systemd drop-in makes Patroni find it via PYTHONPATH.
PATRONI_DEPS=/var/lib/netbox-agent/patroni-deps
mkdir -p "$PATRONI_DEPS"
chown netbox-agent:netbox-agent "$PATRONI_DEPS"
chmod 755 "$PATRONI_DEPS"
mkdir -p /etc/systemd/system/patroni.service.d
cat > /etc/systemd/system/patroni.service.d/pythonpath.conf <<'EOF'
[Service]
Environment=PYTHONPATH=/var/lib/netbox-agent/patroni-deps
EOF
systemctl daemon-reload 2>/dev/null || true

# ── Redis/Sentinel configuration directory ───────────────────────────────────
# Add agent to the redis group for sentinel.conf write access
if getent group redis >/dev/null 2>&1; then
  usermod -aG redis netbox-agent
fi

echo "→ Installing sudoers drop-in"
SUDOERS_FILE=/etc/sudoers.d/netbox-agent
install -m 440 netbox-agent-sudoers "$SUDOERS_FILE"
# Validate syntax — if visudo rejects it, remove and abort rather than breaking sudo.
if ! visudo -cf "$SUDOERS_FILE" >/dev/null 2>&1; then
  rm -f "$SUDOERS_FILE"
  echo "ERROR: sudoers syntax check failed — service management will not work" >&2
  exit 1
fi

echo "→ Installing systemd service"
install -m 644 netbox-agent.service "$SERVICE_FILE"
systemctl daemon-reload
systemctl enable netbox-agent

echo ""
echo "Installation complete. Next steps:"
echo "  1. Edit $ENV_DIR/netbox-agent.env with your Conductor URL and credentials"
echo "     (or download it from the Conductor UI: Cluster → Node → Download agent .env)"
echo "  2. sudo systemctl start netbox-agent"
echo "  3. sudo journalctl -u netbox-agent -f"
