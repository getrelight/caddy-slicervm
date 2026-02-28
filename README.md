# caddy-relight-slicervm

A Relight Caddy module that routes requests to [SlicerVM](https://slicervm.com/) microVMs with scale-to-zero. Idle VMs are paused automatically and resumed on the next incoming request.

```
DNS --> Caddy + relight_slicervm module (:443) --> microVM (bridge IP)
             |
             +-- pauses idle VMs / resumes on request
```

Supports two routing modes:

- **Wildcard subdomains** - `myapp.apps.example.com` finds the node tagged `myapp`
- **Custom domains** - `myapp.com` finds the node tagged `myapp.com` (with on-demand TLS)

Tag matching: exact hostname match is tried first, then first subdomain label.

## Host setup

Caddy with this module runs on the Slicer host (the machine running `slicerd`). How you run Caddy (directly, via systemd, in a container) is up to the host operator - it's infrastructure, not something the app deployment CLI manages.

You need a host group for app VMs in the Slicer YAML config:

```yaml
config:
  host_groups:
    - name: apps
      count: 0
      vcpu: 1
      ram_gb: 1
      storage_size: 5G
      network:
        mode: bridge
        gateway: 192.168.137.1/24
```

### DNS

Point a wildcard record at the Slicer host's public IP:

```
*.apps.example.com.  A  203.0.113.10
```

For custom domains, each domain needs an A record (or CNAME) pointing to the same IP. Users configure this in their own DNS provider.

### Build and run

```bash
xcaddy build --with github.com/slicervm/caddy-relight-slicervm=./
./caddy run --config Caddyfile
```

Caddy handles TLS certificates automatically via Let's Encrypt.

## Caddyfile

The `slicer_url` can be an HTTP URL or a Unix socket path:

### Wildcard subdomains only

```caddyfile
{
    order relight_slicervm before reverse_proxy
}

*.apps.example.com {
    relight_slicervm {
        slicer_url    ~/slicer-mac/slicer.sock
        slicer_token  {env.SLICER_TOKEN}
        host_group    apps
        idle_timeout  5m
        wake_timeout  30s
        app_port      8080
    }
    reverse_proxy {http.vars.relight_slicervm_upstream}
}
```

### With custom domains (on-demand TLS)

```caddyfile
{
    order relight_slicervm before reverse_proxy
    on_demand_tls {
        ask http://127.0.0.1:5555/check
    }
}

# Wildcard subdomains - tag "myapp" matches myapp.apps.example.com
*.apps.example.com {
    relight_slicervm {
        slicer_url    ~/slicer-mac/slicer.sock
        slicer_token  {env.SLICER_TOKEN}
        host_group    apps
        idle_timeout  5m
        wake_timeout  30s
        app_port      8080
        ask_listen    127.0.0.1:5555
    }
    reverse_proxy {http.vars.relight_slicervm_upstream}
}

# Custom domains - tag "myapp.com" matches myapp.com
https:// {
    tls {
        on_demand
    }
    relight_slicervm {
        slicer_url    ~/slicer-mac/slicer.sock
        slicer_token  {env.SLICER_TOKEN}
        host_group    apps
        idle_timeout  5m
        wake_timeout  30s
        app_port      8080
    }
    reverse_proxy {http.vars.relight_slicervm_upstream}
}
```

The `ask_listen` directive starts an internal HTTP server that Caddy's `on_demand_tls` queries before provisioning a certificate. It checks if a VM exists with a tag matching the domain - returns 200 if found, 404 if not. This prevents certificate issuance for arbitrary domains.

### Directives

| Directive | Default | Description |
|---|---|---|
| `slicer_url` | (required) | Slicer API URL or Unix socket path |
| `slicer_token` | (required) | Slicer API token |
| `host_group` | (required) | Host group containing app VMs |
| `idle_timeout` | `5m` | How long before an idle VM is paused (min 30s) |
| `wake_timeout` | `30s` | Max time to wait for a VM to resume |
| `app_port` | `8080` | Port on the VM to proxy to |
| `watch_interval` | `30s` | How often to check for idle VMs |
| `ask_listen` | (disabled) | Address for on-demand TLS validation server |

## How it works

On each request the module:

1. Extracts the hostname from the request
2. Lists all VMs via `GET /nodes` (includes status) and finds a matching node by tag:
   - First tries exact match (tag == full hostname, e.g. `myapp.com`)
   - Falls back to first subdomain label (tag == `myapp` from `myapp.apps.example.com`)
3. If the VM is paused, calls `POST /vm/{hostname}/resume` and blocks until ready
4. Sets `{http.vars.relight_slicervm_upstream}` to `ip:port` for Caddy's `reverse_proxy`
5. Records the request time for idle tracking

A background goroutine runs every `watch_interval` and pauses VMs that haven't received traffic for `idle_timeout` via `POST /vm/{hostname}/pause`.

Concurrent requests to a paused VM are coalesced - only one `resume` call is made, all requests block on the same wake signal.

## Slicer REST API usage

The module uses three endpoints:

```
GET  /nodes                     # list all VMs with status, find by tag
POST /vm/{hostname}/resume      # wake on incoming request
POST /vm/{hostname}/pause       # idle watcher
```

Note: `GET /hostgroup/{name}/nodes` does not return `status` - that's why the module uses `GET /nodes` instead.

### Deployment flow (handled by your CLI/CD, not this module)

The host group must exist in the Slicer YAML config. Nodes are created via API and tagged with the app name.

The exec endpoint uses **query parameters**, not a JSON body. The request body carries stdin data only.

**First deploy:**

```bash
SOCK=~/slicer-mac/slicer.sock

# 1. Create node tagged with the app name (and optionally a custom domain)
curl -s --unix-socket $SOCK -X POST \
  http://localhost/hostgroup/apps/nodes \
  -d '{"tags": ["myapp", "myapp.com"]}'
# -> {"hostname":"apps-1","ip":"192.168.64.3","tags":["myapp","myapp.com"],"created_at":"..."}

# 2. Wait for agent ready
curl -s --unix-socket $SOCK \
  http://localhost/vm/apps-1/health
# -> 200 once booted (typically instant)

# 3. Install runtime (if not in base image)
curl -s --unix-socket $SOCK -X POST \
  "http://localhost/vm/apps-1/exec?cmd=/bin/bash&args=-c&args=curl+-fsSL+https://deb.nodesource.com/setup_22.x+|+bash+-+%26%26+apt-get+install+-y+nodejs&uid=0&gid=0"

# 4. Upload app bundle (tar of your app files)
tar cf app.tar index.js package.json
curl -s --unix-socket $SOCK -X POST \
  "http://localhost/vm/apps-1/cp?path=/app&uid=1000&gid=1000&mode=tar" \
  -H "Content-Type: application/x-tar" \
  --data-binary @app.tar

# 5. Start the app
curl -s --unix-socket $SOCK -X POST \
  "http://localhost/vm/apps-1/exec?cmd=node&args=/app/index.js&uid=1000&gid=1000&cwd=/app"
```

At this point both `myapp.apps.example.com` and `myapp.com` are live - Caddy routes to the same VM automatically.

**Consecutive deploys:**

```bash
# 1. Find the node (look for tag "myapp" in the response)
curl -s --unix-socket $SOCK http://localhost/nodes
# -> [{"hostname":"apps-1","status":"Paused","tags":["myapp","myapp.com"],...}]

# 2. Resume if paused
curl -s --unix-socket $SOCK -X POST \
  http://localhost/vm/apps-1/resume

# 3. Stop old process
curl -s --unix-socket $SOCK -X POST \
  "http://localhost/vm/apps-1/exec?cmd=pkill&args=-f&args=node&uid=0&gid=0"

# 4. Upload new bundle
curl -s --unix-socket $SOCK -X POST \
  "http://localhost/vm/apps-1/cp?path=/app&uid=1000&gid=1000&mode=tar" \
  -H "Content-Type: application/x-tar" \
  --data-binary @app.tar

# 5. Start new version
curl -s --unix-socket $SOCK -X POST \
  "http://localhost/vm/apps-1/exec?cmd=node&args=/app/index.js&uid=1000&gid=1000&cwd=/app"
```

**Teardown:**

```bash
curl -s --unix-socket $SOCK -X DELETE \
  http://localhost/hostgroup/apps/nodes/apps-1
```
