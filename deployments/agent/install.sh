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
if [[ ! -f "$ENV_DIR/netbox-agent.env" ]]; then
  install -m 640 netbox-agent.env.example "$ENV_DIR/netbox-agent.env"
  chown root:netbox-agent "$ENV_DIR/netbox-agent.env"
  echo "  Created $ENV_DIR/netbox-agent.env"
  echo "  Fill in AGENT_NODE_ID, AGENT_TOKEN, and AGENT_SERVER_URL before starting."
  echo "  (Or download a pre-filled env file from the Conductor UI.)"
else
  echo "  $ENV_DIR/netbox-agent.env already exists — not overwritten"
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
