# NetBox Conductor — Frontend

React 18 + TypeScript + Vite + Tailwind CSS v4.

## Development

```bash
# Install dependencies (from repo root)
cd web && npm install

# Start the dev server (proxies /api/* to the backend on :8443)
npm run dev
# → http://localhost:5173

# Type check
npm run typecheck

# Build for production (output embedded into the Go binary via go:embed)
npm run build
```

The backend must be running locally for API calls to work. See [Development Guide](../docs/development.md) for full setup instructions.

## Structure

```text
web/src/
├── api/          # Axios client modules (clusters, nodes, patroni, …)
├── components/   # Shared UI components (layout, sidebar, etc.)
├── pages/        # Page-level components (Dashboard, ClusterDetail, …)
└── store/        # Zustand stores (auth, theme)
```
