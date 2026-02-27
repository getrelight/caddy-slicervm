# caddy-slicervm

A Caddy module that routes subdomains to [SlicerVM](https://slicervm.com/) microVMs with scale-to-zero. Idle VMs are paused automatically and resumed on the next incoming request.

```
DNS --> Caddy + slicervm module (:443) --> microVM (bridge IP)
             |
             +-- pauses idle VMs / resumes on request
```

Convention: subdomain = SlicerVM host group name. A request to `myapp.apps.example.com` is routed to the first node in host group `myapp`.

## Host setup

Caddy itself runs inside a Slicer VM on the host. Ports 80 and 443 are forwarded from the host to the Caddy VM.

### 1. DNS

Point a wildcard record at the Slicer host's public IP:

```
*.apps.example.com.  A  203.0.113.10
```

### 2. Build Caddy with the module

```bash
xcaddy build --with github.com/slicervm/caddy-slicervm=./
# produces ./caddy binary
```

### 3. Provision the Caddy VM via Slicer API

```bash
# Create VM in a "caddy" host group
curl -X POST http://localhost:8080/hostgroup/caddy/nodes \
  -H "Authorization: Bearer $SLICER_TOKEN" \
  -d '{"ram_gb": 1, "cpus": 1, "disk_image": "ubuntu-24.04"}'
# -> {"hostname": "caddy-abc123", "ip": "192.168.137.2"}

# Wait for agent ready
curl http://localhost:8080/vm/caddy-abc123/health \
  -H "Authorization: Bearer $SLICER_TOKEN"

# Upload the custom Caddy binary
curl -X POST "http://localhost:8080/vm/caddy-abc123/cp?path=/usr/local/bin/caddy&uid=0&gid=0&permissions=0755" \
  -H "Authorization: Bearer $SLICER_TOKEN" \
  -H "Content-Type: application/octet-stream" \
  --data-binary @./caddy

# Upload the Caddyfile
curl -X POST "http://localhost:8080/vm/caddy-abc123/cp?path=/etc/caddy/Caddyfile&uid=0&gid=0&permissions=0644" \
  -H "Authorization: Bearer $SLICER_TOKEN" \
  -H "Content-Type: application/octet-stream" \
  --data-binary @./Caddyfile

# Start Caddy
curl -X POST http://localhost:8080/vm/caddy-abc123/exec \
  -H "Authorization: Bearer $SLICER_TOKEN" \
  -d '{"command": "SLICER_TOKEN=... caddy run --config /etc/caddy/Caddyfile", "shell": "/bin/bash"}'
```

### 4. Forward ports 80/443 from the host to the Caddy VM

```bash
# Forward HTTPS
slicer vm forward caddy-abc123 0.0.0.0:443:127.0.0.1:443

# Forward HTTP (ACME challenges + HTTPS redirects)
slicer vm forward caddy-abc123 0.0.0.0:80:127.0.0.1:80
```

The Caddy VM accesses the Slicer API over the bridge network (`slicer_url` in the Caddyfile points to the host's bridge IP or `slicerd`'s listen address).

Caddy handles TLS certificates automatically via Let's Encrypt.

## Caddyfile

```caddyfile
{
    order slicervm before reverse_proxy
}

*.apps.example.com {
    slicervm {
        slicer_url    http://127.0.0.1:8080
        slicer_token  {env.SLICER_TOKEN}
        idle_timeout  5m
        wake_timeout  30s
        app_port      8080
    }
    reverse_proxy {http.vars.slicervm_upstream}
}
```

### Directives

| Directive | Default | Description |
|---|---|---|
| `slicer_url` | (required) | Slicer API base URL |
| `slicer_token` | (required) | Slicer API token |
| `idle_timeout` | `5m` | How long before an idle VM is paused (min 30s) |
| `wake_timeout` | `30s` | Max time to wait for a VM to resume |
| `app_port` | `8080` | Port on the VM to proxy to |
| `watch_interval` | `30s` | How often to check for idle VMs |

## How it works

On each request the module:

1. Extracts the app name from the first subdomain label
2. Looks up the host group via `GET /hostgroup/{app}/nodes`
3. If the VM is paused, calls `POST /vm/{hostname}/resume` and blocks until ready
4. Sets `{http.vars.slicervm_upstream}` to `ip:port` for Caddy's `reverse_proxy`
5. Records the request time for idle tracking

A background goroutine runs every `watch_interval` and pauses VMs that haven't received traffic for `idle_timeout` via `POST /vm/{hostname}/pause`.

Concurrent requests to a paused VM are coalesced - only one `resume` call is made, all requests block on the same wake signal.

## Slicer REST API usage

The module itself uses three endpoints:

```
GET  /hostgroup/{app}/nodes    # look up VM by subdomain
POST /vm/{hostname}/resume     # wake on incoming request
POST /vm/{hostname}/pause      # idle watcher
```

### Deployment flow (handled by your CLI/CD, not this module)

**First deploy** - create a VM and push your app:

```bash
# 1. Create VM in host group "myapp"
curl -X POST http://localhost:8080/hostgroup/myapp/nodes \
  -H "Authorization: Bearer $SLICER_TOKEN" \
  -d '{"ram_gb": 1, "cpus": 1, "disk_image": "ubuntu-24.04"}'
# -> {"hostname": "myapp-abc123", "ip": "10.0.0.5"}

# 2. Wait for agent ready
curl http://localhost:8080/vm/myapp-abc123/health \
  -H "Authorization: Bearer $SLICER_TOKEN"
# -> 200 once booted

# 3. Upload app bundle
curl -X POST "http://localhost:8080/vm/myapp-abc123/cp?path=/app&uid=1000&gid=1000&mode=tar" \
  -H "Authorization: Bearer $SLICER_TOKEN" \
  -H "Content-Type: application/x-tar" \
  --data-binary @app.tar

# 4. Start the app
curl -X POST http://localhost:8080/vm/myapp-abc123/exec \
  -H "Authorization: Bearer $SLICER_TOKEN" \
  -d '{"command": "cd /app && npm start", "shell": "/bin/bash", "uid": 1000, "gid": 1000}'
```

At this point `myapp.apps.example.com` is live - Caddy routes to it automatically.

**Consecutive deploys** - update an existing app:

```bash
# 1. Check VM state
curl http://localhost:8080/hostgroup/myapp/nodes \
  -H "Authorization: Bearer $SLICER_TOKEN"
# -> [{"hostname": "myapp-abc123", "status": "Paused", ...}]

# 2. Resume if paused
curl -X POST http://localhost:8080/vm/myapp-abc123/resume \
  -H "Authorization: Bearer $SLICER_TOKEN"

# 3. Stop old process
curl -X POST http://localhost:8080/vm/myapp-abc123/exec \
  -H "Authorization: Bearer $SLICER_TOKEN" \
  -d '{"command": "pkill -f npm", "shell": "/bin/bash"}'

# 4. Upload new bundle
curl -X POST "http://localhost:8080/vm/myapp-abc123/cp?path=/app&uid=1000&gid=1000&mode=tar" \
  -H "Authorization: Bearer $SLICER_TOKEN" \
  -H "Content-Type: application/x-tar" \
  --data-binary @app.tar

# 5. Start new version
curl -X POST http://localhost:8080/vm/myapp-abc123/exec \
  -H "Authorization: Bearer $SLICER_TOKEN" \
  -d '{"command": "cd /app && npm start", "shell": "/bin/bash", "uid": 1000, "gid": 1000}'
```

**Teardown:**

```bash
curl -X DELETE http://localhost:8080/hostgroup/myapp/nodes/myapp-abc123 \
  -H "Authorization: Bearer $SLICER_TOKEN"
```
