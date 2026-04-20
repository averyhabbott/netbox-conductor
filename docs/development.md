# Development Guide

## Local Setup

```bash
# 1. Create the dev database
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

---

## Running Tests

```bash
make test       # Go unit tests
make vet        # go vet
make typecheck  # TypeScript type check
```

---

## Dev Deploy Script

Builds and pushes binaries directly to the OrbStack dev VMs over SSH:

```bash
# Build and push conductor binary + service file
bash testing/deploy.sh

# Also push agent binary, service file, and sudoers to agent nodes
bash testing/deploy.sh --agents
```

---

## Project Structure

```
netbox-conductor/
├── cmd/
│   ├── server/          # Conductor server binary entry point
│   ├── agent/           # Agent binary entry point (task executor)
│   └── rotate-key/      # Offline key-rotation tool (re-encrypts all secrets)
├── deployments/
│   ├── server/          # Server systemd unit, env template, install.sh
│   └── agent/           # Agent systemd unit, install.sh, env template, sudoers, bundle.go
├── docs/                # This documentation
├── internal/
│   ├── agent/
│   │   ├── config/       # Agent env file loading, validation, and cert-learning
│   │   ├── executor/     # Task implementations (config write, db host update, Patroni, media sync, upgrade, etc.)
│   │   ├── statusserver/ # Local HTTP health endpoint (Patroni-aware in active/standby mode)
│   │   └── ws/           # WebSocket client (connect, reconnect, heartbeat, OnServerHello callback)
│   ├── server/
│   │   ├── alerting/     # Alert rule engine, state machine, dispatcher, and transport implementations (webhook, SMTP email, Slack)
│   │   ├── api/
│   │   │   ├── handlers/ # HTTP endpoint implementations
│   │   │   ├── middleware/ # Auth, audit, rate limiting
│   │   │   └── router.go # All route registrations
│   │   ├── configgen/    # NetBox configuration.py and Patroni/Sentinel config renderers; parser.go extracts credentials from a live configuration.py
│   │   ├── crypto/       # AES-256-GCM encryption helpers
│   │   ├── db/
│   │   │   ├── migrations/ # SQL migration files (golang-migrate)
│   │   │   └── queries/    # DB query implementations (clusters, nodes, events, heartbeats, alert rules/transports/schedules/states, syslog destinations, retention, …)
│   │   ├── events/       # NBC-{CAT}-{NNN} event code constants, Event type, Emitter, HeartbeatProcessor (derives service state-change events from consecutive heartbeats)
│   │   ├── failover/     # Automatic failover/failback manager
│   │   ├── hub/          # WebSocket session registry and dispatcher
│   │   ├── logging/      # Structured JSON logging with lumberjack rotation, per-agent log files
│   │   ├── partitions/   # PostgreSQL partition lifecycle manager (creates monthly/weekly partitions, drops expired ones)
│   │   ├── patroni/      # Witness subprocess management
│   │   ├── scheduler/    # Background jobs (health checks, task timeouts)
│   │   ├── sse/          # Server-Sent Events broker
│   │   ├── syslog/       # RFC 5424 syslog forwarding (TCP/UDP/TLS) with per-destination category and severity filtering
│   │   └── tlscert/      # TLS cert auto-generation
│   └── shared/
│       └── protocol/     # WebSocket message types shared by server and agent
├── planning/             # Architecture notes and current-state snapshots
├── testing/              # Dev deploy scripts
└── web/
    └── src/
        ├── api/          # Axios API client modules
        ├── components/   # Shared React components
        ├── pages/        # Page-level React components (ClusterDetail, Dashboard, …)
        └── store/        # Zustand state stores
```
