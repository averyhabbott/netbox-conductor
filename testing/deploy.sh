#!/usr/bin/env bash
# Deploy netbox-conductor to nb-conductor and optionally update agents on managed nodes.
# Run from anywhere: cd to repo root so make and bin/ paths work.
#
# Usage:
#   ./testing/deploy.sh              # deploy conductor only
#   ./testing/deploy.sh --agents     # also push new agent binary to nb-1 and nb-2
set -euo pipefail

cd "$(dirname "$0")/.."

REMOTE="nb-conductor@orb"
BIN_DIR="/opt/netbox-conductor/bin"
AGENT_BIN_DIR="/var/lib/netbox-conductor/bin"
SERVICE="netbox-conductor"
SUDOERS_SRC="deployments/agent/netbox-agent-sudoers"
WITNESS_SRC="deployments/server/patroni-witness.py"
WITNESS_DEST="/opt/netbox-conductor/patroni-witness.py"

# Managed node list for --agents flag
AGENT_NODES=("nb-1@orb" "nb-2@orb")
SERVICE_SRC="deployments/agent/netbox-agent.service"

echo "→ Building all binaries..."
make build-all

echo "→ Copying binaries and scripts to $REMOTE..."
scp bin/netbox-conductor-linux-arm64     "$REMOTE":/tmp/netbox-conductor
scp bin/netbox-agent-linux-amd64         "$REMOTE":/tmp/netbox-agent-linux-amd64
scp bin/netbox-agent-linux-arm64         "$REMOTE":/tmp/netbox-agent-linux-arm64
scp "$WITNESS_SRC"                       "$REMOTE":/tmp/patroni-witness.py

echo "→ Installing and restarting service..."
ssh "$REMOTE" "
  sudo mv /tmp/netbox-conductor $BIN_DIR/netbox-conductor &&
  sudo chmod +x $BIN_DIR/netbox-conductor &&
  sudo mv /tmp/netbox-agent-linux-amd64 $AGENT_BIN_DIR/netbox-agent-linux-amd64 &&
  sudo mv /tmp/netbox-agent-linux-arm64 $AGENT_BIN_DIR/netbox-agent-linux-arm64 &&
  sudo chmod +x $AGENT_BIN_DIR/netbox-agent-linux-amd64 &&
  sudo chmod +x $AGENT_BIN_DIR/netbox-agent-linux-arm64 &&
  sudo mv /tmp/patroni-witness.py $WITNESS_DEST &&
  sudo chmod +x $WITNESS_DEST &&
  sudo install -d -m 755 -o netbox-conductor -g netbox-conductor /var/lib/netbox-conductor/raft &&
  sudo systemctl restart $SERVICE &&
  sleep 2 &&
  sudo systemctl is-active $SERVICE
"

echo "✓ Conductor deployed and running"

if [[ "${1:-}" == "--agents" ]]; then
  echo ""
  echo "→ Updating agents on managed nodes..."
  for NODE in "${AGENT_NODES[@]}"; do
    echo "  → $NODE"
    scp bin/netbox-agent-linux-amd64   "$NODE":/tmp/netbox-agent
    scp "$SUDOERS_SRC"                 "$NODE":/tmp/netbox-agent-sudoers
    scp "$SERVICE_SRC"                 "$NODE":/tmp/netbox-agent.service
    ssh "$NODE" "
      sudo mv /tmp/netbox-agent /usr/local/bin/netbox-agent &&
      sudo chmod +x /usr/local/bin/netbox-agent &&
      sudo install -m 440 /tmp/netbox-agent-sudoers /etc/sudoers.d/netbox-agent &&
      sudo visudo -cf /etc/sudoers.d/netbox-agent &&
      sudo install -m 644 /tmp/netbox-agent.service /etc/systemd/system/netbox-agent.service &&
      sudo systemctl daemon-reload &&
      sudo systemctl restart netbox-agent &&
      sleep 2 &&
      sudo systemctl is-active netbox-agent
    "

    echo "  → $NODE: pgBackRest prerequisites"
    ssh "$NODE" '
      sudo mkdir -p /var/lib/pgbackrest /var/log/pgbackrest /etc/pgbackrest
      sudo chown postgres:postgres /var/lib/pgbackrest /var/log/pgbackrest
      sudo chmod 750 /var/lib/pgbackrest
      sudo chmod 770 /var/log/pgbackrest
      sudo chown netbox-agent:postgres /etc/pgbackrest
      sudo chmod 755 /etc/pgbackrest
      PG_CTL=$(find /usr/lib/postgresql -name pg_ctl -type f 2>/dev/null | sort -V | tail -1)
      [ -n "$PG_CTL" ] && sudo ln -sf "$PG_CTL" /usr/local/bin/pg_ctl && echo "  pg_ctl → $PG_CTL"
    '

    echo "  ✓ $NODE updated"
  done
fi
