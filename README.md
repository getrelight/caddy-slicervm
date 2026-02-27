# caddy-slicervm

A Caddy module that routes subdomains to [SlicerVM](https://slicervm.com/) microVMs with scale-to-zero. Idle VMs are paused automatically and resumed on the next incoming request.

```
DNS --> Caddy + slicervm module (:443) --> microVM (bridge IP)
             |
             +-- pauses idle VMs / resumes on request
```

Convention: subdomain = node tag. A request to `myapp.apps.example.com` finds the node tagged `myapp` in the configured host group.

### DNS

Point a wildcard record at the Slicer host's public IP:

```
*.apps.example.com.  A  203.0.113.10
```

### Build and run

```bash
xcaddy build --with github.com/slicervm/caddy-slicervm=./
./caddy run --config Caddyfile
```

Caddy handles TLS certificates automatically via Let's Encrypt.

## Caddyfile

The `slicer_url` can be an HTTP URL or a Unix socket path:

```caddyfile
{
    order slicervm before reverse_proxy
}

*.apps.example.com {
    slicervm {
        slicer_url    ~/slicer-mac/slicer.sock
        slicer_token  {env.SLICER_TOKEN}
        host_group    apps
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
| `slicer_url` | (required) | Slicer API URL or Unix socket path |
| `slicer_token` | (required) | Slicer API token |
| `host_group` | (required) | Host group containing app VMs |
| `idle_timeout` | `5m` | How long before an idle VM is paused (min 30s) |
| `wake_timeout` | `30s` | Max time to wait for a VM to resume |
| `app_port` | `8080` | Port on the VM to proxy to |
| `watch_interval` | `30s` | How often to check for idle VMs |

## How it works

On each request the module:

1. Extracts the app name from the first subdomain label
2. Lists all VMs via `GET /nodes` (includes status) and finds the node tagged with the app name
3. If the VM is paused, calls `POST /vm/{hostname}/resume` and blocks until ready
4. Sets `{http.vars.slicervm_upstream}` to `ip:port` for Caddy's `reverse_proxy`
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

# 1. Create node tagged with the app name
curl -s --unix-socket $SOCK -X POST \
  http://localhost/hostgroup/sbox/nodes \
  -d '{"tags": ["myapp"]}'
# -> {"hostname":"sbox-1","ip":"192.168.64.3","tags":["myapp"],"created_at":"..."}

# 2. Wait for agent ready
curl -s --unix-socket $SOCK \
  http://localhost/vm/sbox-1/health
# -> 200 once booted (typically instant)

# 3. Install runtime (if not in base image)
curl -s --unix-socket $SOCK -X POST \
  "http://localhost/vm/sbox-1/exec?cmd=/bin/bash&args=-c&args=curl+-fsSL+https://deb.nodesource.com/setup_22.x+|+bash+-+%26%26+apt-get+install+-y+nodejs&uid=0&gid=0"

# 4. Upload app bundle (tar of your app files)
tar cf app.tar index.js package.json
curl -s --unix-socket $SOCK -X POST \
  "http://localhost/vm/sbox-1/cp?path=/app&uid=1000&gid=1000&mode=tar" \
  -H "Content-Type: application/x-tar" \
  --data-binary @app.tar

# 5. Start the app
curl -s --unix-socket $SOCK -X POST \
  "http://localhost/vm/sbox-1/exec?cmd=node&args=/app/index.js&uid=1000&gid=1000&cwd=/app"
```

At this point `myapp.apps.example.com` is live - Caddy routes to it automatically.

**Consecutive deploys:**

```bash
# 1. Find the node (look for tag "myapp" in the response)
curl -s --unix-socket $SOCK http://localhost/nodes
# -> [{"hostname":"sbox-1","status":"Paused","tags":["myapp"],...}]

# 2. Resume if paused
curl -s --unix-socket $SOCK -X POST \
  http://localhost/vm/sbox-1/resume

# 3. Stop old process
curl -s --unix-socket $SOCK -X POST \
  "http://localhost/vm/sbox-1/exec?cmd=pkill&args=-f&args=node&uid=0&gid=0"

# 4. Upload new bundle
curl -s --unix-socket $SOCK -X POST \
  "http://localhost/vm/sbox-1/cp?path=/app&uid=1000&gid=1000&mode=tar" \
  -H "Content-Type: application/x-tar" \
  --data-binary @app.tar

# 5. Start new version
curl -s --unix-socket $SOCK -X POST \
  "http://localhost/vm/sbox-1/exec?cmd=node&args=/app/index.js&uid=1000&gid=1000&cwd=/app"
```

**Teardown:**

```bash
curl -s --unix-socket $SOCK -X DELETE \
  http://localhost/hostgroup/sbox/nodes/sbox-1
```
