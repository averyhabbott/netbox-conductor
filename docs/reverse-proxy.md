# Reverse Proxy & Health Checks

Each agent node exposes a lightweight health endpoint that reverse proxies and VIP health-checkers use to steer traffic:

```
GET https://<node>/status
```

---

## Health Check Logic

| Cluster state | Returns 200 if… |
|---|---|
| Patroni not yet configured | `netbox.service` is active |
| App tier always available | `netbox.service` is active (all nodes eligible) |
| Active/standby + Patroni configured | `netbox.service` active **AND** `GET http://127.0.0.1:8008/primary` returns 200 |

In active/standby mode the Patroni primary check ensures the load balancer never routes to a replica node, even if NetBox is still shutting down during a failover transition.

Response body:

```json
{"status":"ok","netbox":true,"rq":true,"node_id":"<uuid>","patroni_primary":true}
```

`patroni_primary` is omitted when Patroni is not configured or the cluster is always-available.

---

## How It Works

The agent binds the status server to `127.0.0.1:8081` (loopback only) by default. The node's nginx or Apache reverse proxy exposes `GET /status` on the public HTTPS port. Health checkers probe `https://<node>/status` and never need direct access to the agent port.

Controlled by `AGENT_STATUS_ADDR` in the agent env file (default `127.0.0.1:8081`; empty string disables the server). The legacy `AGENT_STATUS_PORT` integer variable is still accepted for backward compatibility.

> **Direct agent port access:** If your load balancer must probe the agent port directly (e.g. a remote HAProxy with no access to port 443), set `AGENT_STATUS_ADDR=0.0.0.0:8081` in the agent env file. The endpoint is then reachable at `http://<node>:8081/status`.

---

## nginx

`nginx-netbox-conductor.conf` is included in the agent tarball downloaded from the conductor. Copy it into place after running the agent install:

```bash
sudo cp /opt/netbox-agent/nginx-netbox-conductor.conf /etc/nginx/sites-available/netbox
sudo ln -s /etc/nginx/sites-available/netbox /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
```

Edit the file first to replace `netbox.example.com` with your actual hostname and update the SSL certificate paths. This is a drop-in replacement for the standard NetBox nginx config — it adds a `location = /status` block that proxies requests to `127.0.0.1:8081`.

> **RHEL/CentOS/Rocky:** place the file in `/etc/nginx/conf.d/netbox.conf` instead of `sites-available/`. If nginx cannot connect to 127.0.0.1:8081, run `sudo setsebool -P httpd_can_network_connect 1`.

---

## Apache

`apache-netbox-conductor.conf` is included in the agent tarball downloaded from the conductor. Copy it into place after running the agent install:

```bash
sudo a2enmod proxy proxy_http ssl rewrite headers
sudo cp /opt/netbox-agent/apache-netbox-conductor.conf /etc/apache2/sites-available/netbox.conf
sudo a2ensite netbox
sudo apache2ctl configtest && sudo systemctl reload apache2
```

Edit the file first to replace `netbox.example.com` with your actual hostname and update the SSL certificate paths. This adds a `<Location /status>` block proxying to `127.0.0.1:8081`.

> **RHEL/CentOS/Rocky:** place the file in `/etc/httpd/conf.d/netbox.conf` instead.

---

## HAProxy

HAProxy checks `https://<node>/status` on port 443 — the same port as application traffic, served by the node's reverse proxy.

```haproxy
frontend netbox_frontend
    bind *:443 ssl crt /etc/ssl/certs/haproxy.pem
    default_backend netbox_backends

backend netbox_backends
    option httpchk GET /status
    http-check expect status 200

    # Both nodes checked via HTTPS; node-2 is standby (backup)
    server node-1 node-1.example.com:443 ssl verify none check inter 10s fall 2 rise 1
    server node-2 node-2.example.com:443 ssl verify none check inter 10s fall 2 rise 1 backup
```

HAProxy marks a server down after 2 consecutive failed checks (`fall 2`) and brings it back after 1 passing check (`rise 1`). In active/standby mode only `node-1` receives traffic; `node-2` takes over automatically when `node-1` returns 503.
