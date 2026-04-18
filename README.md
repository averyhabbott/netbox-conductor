# NetBox Conductor

A management platform for orchestrating multi-node [NetBox](https://netboxlabs.com/) deployments. NetBox Conductor coordinates configuration, service lifecycle, database high availability (via Patroni), Redis Sentinel, and media sync across a cluster of NetBox servers — all from a single web UI.

---

## How It Works

**Conductor** is a single Go binary that serves the web UI and API. Each managed NetBox server runs a lightweight **agent** binary that maintains a persistent outbound WebSocket connection to the Conductor. The Conductor dispatches tasks (write config, restart service, push Patroni config, etc.) to agents over that connection; agents report back heartbeats, service state, and task results.

No inbound firewall rules are needed on agent hosts — all traffic is agent-initiated.

```text
┌──────────────────────────────────────────────────────────────────┐
│                       NetBox Conductor                           │
│  (web UI + API + task dispatcher + Patroni witness)              │
└──────────────────┬───────────────────────────────────────────────┘
                   │ wss:// (outbound from agents)
          ┌────────┴─────────┐
          │                  │
┌─────────▼──────┐  ┌────────▼────────┐
│    node-1      │  │    node-2       │
│  netbox-agent  │  │  netbox-agent   │
│  Patroni       │  │  Patroni        │
│  PostgreSQL    │  │  PostgreSQL     │
│  Redis         │  │  Redis          │
└────────────────┘  └─────────────────┘
```

---

## Features

- **Cluster & node management** — hyperconverged, app-only, and db-only node roles; maintenance mode; decommission workflow
- **Configuration management** — browser-based `configuration.py` editor with version history, diffs, and per-node overrides; **Sync Config** pulls the live config from a source node, lets you edit it, and pushes it to destination nodes
- **Credential management** — stores eight credential kinds (Postgres superuser, replication user, NetBox DB user, Redis Tasks/Caching passwords, NetBox Secret Key, API Token Pepper, Patroni REST password) encrypted with AES-256-GCM; one-click **Auto-Generate** or **Import From Existing** wizard reads live values from `configuration.py` on any connected node
- **Service control** — start/stop/restart NetBox, NetBox-RQ, Patroni, Redis, and Redis Sentinel on any node
- **PostgreSQL HA** — one-button Configure Failover: installs Patroni, generates credentials, pushes config, starts the built-in Raft witness; see [HA & Failover](docs/ha-failover.md)
- **Automatic failover & failback** — grace-period failover on disconnect or service failure; maintenance-triggered moves; auto-failback to higher-priority nodes
- **App Tier Always Available** — all nodes run NetBox simultaneously; `DATABASE.HOST` follows the Patroni primary automatically
- **Redis Sentinel** — push Sentinel config across the cluster; auth passwords managed as encrypted credentials
- **Health checks** — `GET /status` endpoint on each agent node for VIP health-checkers and reverse proxies; Patroni-aware in active/standby mode; see [Reverse Proxy](docs/reverse-proxy.md)
- **Alerting** — webhook and SMTP email alerts on agent disconnect or service degradation; configurable per condition
- **Cluster & conductor logs** — live log viewer per node; filterable cluster-wide log aggregation; conductor system logs
- **Media sync** — relay NetBox media files between nodes through the Conductor (no direct node-to-node SSH)
- **Agent pool** — stage agents before cluster assignment; assign from pool in the UI
- **Security** — JWT (RS256) auth, TOTP 2FA, RBAC (viewer/operator/admin), AES-256-GCM credential encryption, audit log

---

## Documentation

| Guide | Contents |
| --- | --- |
| [Installation](docs/installation.md) | Build prerequisites, conductor server setup, agent setup, key rotation |
| [HA & Failover](docs/ha-failover.md) | Configure Failover walkthrough, Patroni Raft witness, automatic failover/failback, app-tier modes |
| [Reverse Proxy & Health Checks](docs/reverse-proxy.md) | Health check logic, nginx/Apache/HAProxy configuration |
| [Development](docs/development.md) | Local dev setup, running tests, deploy scripts, project structure |
| [Roadmap](docs/roadmap.md) | Planned features |

---

## Architecture

| Component | Technology |
| --- | --- |
| Server | Go 1.25 + Echo v4 |
| Frontend | React 18 + TypeScript + Vite + Tailwind CSS v4 |
| Database | PostgreSQL (pgx/v5 driver) |
| Agent protocol | WebSocket + JSON envelopes |
| HA | Patroni (built-in Raft DCS), Redis Sentinel |
| Auth | JWT (RS256), bcrypt, AES-256-GCM, TOTP |

The server binary embeds the compiled frontend (`go:embed`), so there is only one file to deploy. The agent is a separate static binary compiled for the target platform.

---

## License

Private / internal tooling. Not currently licensed for public distribution.
