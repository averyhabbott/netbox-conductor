#!/usr/bin/env bash
# NetBox Conductor — server-side install / upgrade script
# Run as root on the conductor node.
#
# Usage:
#   sudo bash install.sh [--binary /path/to/netbox-conductor]
#
# What it does:
#   1. Creates the netbox-conductor OS user and directory layout
#   2. Installs/upgrades the conductor binary (optional; skip if no --binary flag)
#   3. Creates a Python venv and installs Patroni + psycopg2-binary
#      (provides patroni_raft_controller, the built-in Raft witness binary)
#   4. Installs/reloads the systemd unit

set -euo pipefail

INSTALL_DIR=/opt/netbox-conductor
BIN_DIR=${INSTALL_DIR}/bin
CONF_DIR=/etc/netbox-conductor
LOG_DIR=/var/log/netbox-conductor
VENV_DIR=${INSTALL_DIR}/venv
SERVICE_NAME=netbox-conductor
SERVICE_FILE=/etc/systemd/system/${SERVICE_NAME}.service

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ── Parse args ────────────────────────────────────────────────────────────────

BINARY_SRC=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary) BINARY_SRC="$2"; shift 2 ;;
    *) echo "Unknown argument: $1" >&2; exit 1 ;;
  esac
done

# ── Check prerequisites ───────────────────────────────────────────────────────

if [[ $EUID -ne 0 ]]; then
  echo "Error: must be run as root" >&2
  exit 1
fi

for cmd in python3 systemctl; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "Error: '$cmd' not found — install it and retry" >&2
    exit 1
  fi
done

# ── OS user & directories ─────────────────────────────────────────────────────

echo "==> Creating OS user and directories..."

if ! id "${SERVICE_NAME}" &>/dev/null; then
  useradd --system --no-create-home --shell /usr/sbin/nologin \
    --comment "NetBox Conductor server" "${SERVICE_NAME}"
fi

install -d -m 755 -o "${SERVICE_NAME}" -g "${SERVICE_NAME}" "${INSTALL_DIR}"
install -d -m 755 -o "${SERVICE_NAME}" -g "${SERVICE_NAME}" "${BIN_DIR}"
install -d -m 750 -o root             -g "${SERVICE_NAME}" "${CONF_DIR}"
install -d -m 750 -o "${SERVICE_NAME}" -g "${SERVICE_NAME}" "${LOG_DIR}"
# /var/lib/netbox-conductor must be writable by the service user so the
# witness manager can create per-cluster raft data subdirectories at runtime.
install -d -m 755 -o "${SERVICE_NAME}" -g "${SERVICE_NAME}" /var/lib/netbox-conductor
install -d -m 755 -o "${SERVICE_NAME}" -g "${SERVICE_NAME}" /var/lib/netbox-conductor/bin
install -d -m 755 -o "${SERVICE_NAME}" -g "${SERVICE_NAME}" /var/lib/netbox-conductor/raft

# ── Conductor binary (optional) ───────────────────────────────────────────────

if [[ -n "${BINARY_SRC}" ]]; then
  echo "==> Installing conductor binary from ${BINARY_SRC}..."
  install -m 755 -o root -g "${SERVICE_NAME}" "${BINARY_SRC}" "${BIN_DIR}/netbox-conductor"
fi

# ── Python venv + Patroni (provides patroni_raft_controller witness) ─────────

echo "==> Setting up Python venv at ${VENV_DIR}..."

# Ensure python3-venv is installed.  On Debian/Ubuntu the module ships in a
# separate package whose name includes the Python minor version (e.g.
# python3.13-venv).  Try the generic name first; fall back to the versioned one.
if ! python3 -c "import ensurepip" &>/dev/null 2>&1; then
  PY_VER=$(python3 -c "import sys; print(f'{sys.version_info.major}.{sys.version_info.minor}')")
  echo "    python3-venv not available — installing python${PY_VER}-venv via apt..."
  apt-get install -y --no-install-recommends "python${PY_VER}-venv" || \
    apt-get install -y --no-install-recommends python3-venv
fi

if [[ ! -d "${VENV_DIR}" ]]; then
  python3 -m venv "${VENV_DIR}"
fi

echo "==> Installing Patroni, pysyncobj, and psycopg..."
"${VENV_DIR}/bin/pip" install --quiet --upgrade pip
# patroni ships patroni_raft_controller, the built-in Raft witness binary used
# by the Conductor for 2-node HA clusters. pysyncobj is Patroni's Raft
# transport layer (required by patroni_raft_controller). psycopg[binary]
# (psycopg3) is Patroni's PostgreSQL adapter dependency.
"${VENV_DIR}/bin/pip" install --quiet patroni pysyncobj "psycopg[binary]"

chown -R "${SERVICE_NAME}:${SERVICE_NAME}" "${VENV_DIR}"

# ── Env file (only if it doesn't already exist) ───────────────────────────────

if [[ ! -f "${CONF_DIR}/netbox-conductor.env" ]]; then
  echo "==> Creating default env file at ${CONF_DIR}/netbox-conductor.env"
  install -m 640 -o root -g "${SERVICE_NAME}" \
    "${SCRIPT_DIR}/netbox-conductor.env.example" \
    "${CONF_DIR}/netbox-conductor.env"
  echo ""
  echo "  *** Edit ${CONF_DIR}/netbox-conductor.env before starting the service ***"
  echo ""
fi

# ── systemd unit ─────────────────────────────────────────────────────────────

echo "==> Installing systemd unit..."
install -m 644 -o root -g root "${SCRIPT_DIR}/netbox-conductor.service" "${SERVICE_FILE}"
systemctl daemon-reload
systemctl enable "${SERVICE_NAME}"

# ── Done ─────────────────────────────────────────────────────────────────────

echo ""
echo "Installation complete."
echo ""
echo "Next steps:"
echo "  1. Edit ${CONF_DIR}/netbox-conductor.env"
echo "  2. sudo systemctl start ${SERVICE_NAME}"
echo "  3. sudo journalctl -u ${SERVICE_NAME} -f"
echo ""
echo "To upgrade an already-running conductor:"
echo "  sudo bash install.sh --binary ./netbox-conductor"
echo "  sudo systemctl restart ${SERVICE_NAME}"
