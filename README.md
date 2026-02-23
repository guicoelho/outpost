# Outpost

Outpost is a per-user persistent compute environment with [opencode](https://opencode.ai) inside, exposed through a reverse proxy with Google SSO. A Go-based outbound proxy intercepts all traffic from the box, injects credentials for managed tools, blocks banned destinations, and passes everything else through. The box holds zero secrets.

## Architecture

```
                    Internet
                       |
            +----- public network -----+
            |          |               |
         [Caddy]  [oauth2-proxy]    [box]
          :80/:443    :4180          :8080
            |                         |
            +--- TLS + forward_auth --+
                                      |
                             +-- isolated network --+  (internal, no internet)
                             |                      |
                          [box]            [outbound-proxy]
                                               :3128
                                                |
                                       +-- internet network --+
                                                |
                                            Internet
```

**Three Docker networks enforce isolation:**

| Network    | Internet access | Services                     |
|------------|-----------------|------------------------------|
| `public`   | Yes             | caddy, oauth2-proxy, box     |
| `isolated` | No (internal)   | box, outbound-proxy          |
| `internet` | Yes             | outbound-proxy               |

The box can only reach the internet through the outbound proxy on the `isolated` network. The proxy is the sole gateway, running on both `isolated` and `internet`.

## Prerequisites

- Docker and Docker Compose v2
- A domain name with DNS pointing to your server (for TLS via Let's Encrypt)
- A Google Cloud OAuth 2.0 client (for SSO)
- API keys for the services you want to broker (GitHub, OpenAI, Anthropic, etc.)

## Quick Start (Pre-built)

Run the install script to download pre-built images from GHCR:

```bash
curl -fsSL https://raw.githubusercontent.com/guicoelho/outpost/main/install.sh | bash
```

Or install a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/guicoelho/outpost/main/install.sh | bash -s -- --version v0.0.1
```

Then edit `outpost/.env` and `outpost/config.yml` with your values and start:

```bash
cd outpost && docker compose up -d
```

To update an existing installation:

```bash
cd outpost && bash install.sh --update
```

## Quick Start (From Source)

### 1. Clone and configure environment variables

```bash
git clone <repo-url> && cd outpost
cp .env.example .env
```

Edit `.env` with your values:

```bash
# Your domain — Caddy auto-provisions TLS via Let's Encrypt
BOX_DOMAIN=box.yourdomain.com

# Google OAuth — create at https://console.cloud.google.com/apis/credentials
GOOGLE_CLIENT_ID=your-client-id.apps.googleusercontent.com
GOOGLE_CLIENT_SECRET=your-client-secret

# Random 32-byte base64 string for cookie encryption
# Generate with: python3 -c "import secrets,base64; print(base64.b64encode(secrets.token_bytes(32)).decode())"
COOKIE_SECRET=

# Restrict login to this email domain
ALLOWED_EMAIL_DOMAIN=yourcompany.com

# API credentials — consumed by outbound-proxy, never injected into the box
GITHUB_TOKEN=ghp_...
OPENAI_API_KEY=sk-...
ANTHROPIC_API_KEY=sk-ant-...
```

### 2. Create the proxy config

```bash
cp config.example.yml config.yml
```

Edit `config.yml` to define your managed tools, credentials, and blocklist (see [Proxy Configuration](#proxy-configuration) below).

### 3. Build and start

```bash
docker compose build
docker compose up -d
```

### 4. Verify

```bash
# All 4 containers should be running
docker compose ps

# Check proxy logs
docker compose logs outbound-proxy
```

Navigate to `https://box.yourdomain.com` — you'll be redirected to Google sign-in. After authentication, you'll see the opencode terminal in your browser.

## Proxy Configuration

The outbound proxy is configured via `config.yml` (mounted read-only into the container). Credential values support `${ENV_VAR}` expansion, resolved from the outbound-proxy container's environment.

### Example `config.yml`

```yaml
user: alice

managed_tools:
  - name: github
    match: "api.github.com"
    credentials:
      header_name: Authorization
      header_value: "token ${GITHUB_TOKEN}"
    policy:
      methods: [GET, POST, PUT, PATCH, DELETE]
      paths: ["/repos/**", "/user/**", "/gists/**"]
      rate_limit: "200/hour"

  - name: openai
    match: "api.openai.com"
    credentials:
      header_name: Authorization
      header_value: "Bearer ${OPENAI_API_KEY}"
    policy:
      rate_limit: "100/minute"

  - name: anthropic
    match: "api.anthropic.com"
    credentials:
      header_name: x-api-key
      header_value: "${ANTHROPIC_API_KEY}"
    policy:
      rate_limit: "100/minute"

  - name: mydb
    protocol: postgres
    match: "db.example.com:5432"
    local_port: 5432
    credentials:
      username: "${PG_USER}"
      password: "${PG_PASSWORD}"

blocked:
  - "*.competitor.com"
  - "malware-domain.com"
```

### Config Reference

**Top-level fields:**

| Field           | Description                              |
|-----------------|------------------------------------------|
| `user`          | Username label (for logging)             |
| `managed_tools` | List of services to inject credentials   |
| `blocked`       | List of glob patterns to block           |

**Managed tool fields:**

| Field                      | Description                                                        |
|----------------------------|--------------------------------------------------------------------|
| `name`                     | Identifier (used in logs and rate limiting)                        |
| `match`                    | Host glob pattern (e.g., `api.github.com`, `*.example.com`)       |
| `protocol`                 | `http` (default) or `postgres`                                     |
| `local_port`               | Listen port for PostgreSQL proxying                                |
| `credentials.header_name`  | HTTP header to inject (HTTP protocol)                              |
| `credentials.header_value` | Header value — supports `${ENV_VAR}` expansion                     |
| `credentials.username`     | PostgreSQL username (postgres protocol)                            |
| `credentials.password`     | PostgreSQL password (postgres protocol)                            |
| `credentials.ref`          | Alternative: `user:password` string (postgres protocol)            |
| `policy.methods`           | Allowed HTTP methods (empty = all allowed)                         |
| `policy.paths`             | Allowed URL path globs with `**` support (empty = all allowed)     |
| `policy.rate_limit`        | Token bucket limit, e.g., `200/hour`, `10/minute`, `5/second`     |

**Blocked patterns** use glob matching. A plain domain like `competitor.com` also matches all subdomains (`*.competitor.com`).

### How traffic is classified

For every outbound request from the box:

1. **Blocked** — destination matches a `blocked` pattern → `403 Forbidden`
2. **Managed** — destination matches a managed tool's `match` → MITM (for HTTPS), inject credentials, enforce policy
3. **Passthrough** — everything else → forwarded directly (HTTPS uses CONNECT tunnel, no inspection)

## Box Apps

The box supports running persistent web applications via pm2. Apps are declared in `/workspace/.boxapps.json` and automatically routed through the internal Caddy reverse proxy.

### Registering an app

Create or edit `/workspace/.boxapps.json`:

```json
[
  {
    "name": "dashboard",
    "path": "/workspace/dashboard",
    "port": 3001,
    "start": "npm start"
  },
  {
    "name": "api",
    "path": "/workspace/api-server",
    "port": 4000,
    "start": "python3 app.py"
  }
]
```

**Fields:**

| Field   | Description                                         |
|---------|-----------------------------------------------------|
| `name`  | Alphanumeric identifier (used in URL path and pm2)  |
| `path`  | Absolute path to the app directory                   |
| `port`  | Port the app listens on                              |
| `start` | Shell command to start the app                       |

Apps are accessible at `https://<domain>/apps/<name>/`. The file is watched with `inotifywait` — changes are picked up automatically, pm2 processes are synced, and Caddy routes are regenerated.

Apps survive container restarts (pm2 restores them from the manifest on boot).

## Services

### Caddy (inbound reverse proxy)

- Listens on ports 80 and 443
- Auto-provisions TLS certificates via Let's Encrypt
- Routes `/oauth2/*` to oauth2-proxy
- All other requests go through `forward_auth` (oauth2-proxy validates the session) then proxy to the box on port 8080

### oauth2-proxy

- Google OAuth 2.0 provider
- Restricts access to the configured email domain
- Sets `X-Auth-Request-User` and `X-Auth-Request-Email` headers on authenticated requests

### Box

- Ubuntu 22.04 with Node.js 20, Python 3.11, build tools
- [opencode](https://opencode.ai) AI coding agent, served via [ttyd](https://github.com/tsl0922/ttyd) web terminal
- Internal Caddy on port 8080 routes to ttyd (7681) and box apps
- pm2 process manager for persistent apps
- All outbound traffic routed through `HTTP_PROXY` / `HTTPS_PROXY` to the outbound proxy
- Trusts the proxy's CA certificate on startup for MITM of managed HTTPS connections
- Holds zero credentials — all secrets are injected by the proxy

### Outbound Proxy

- Go-based HTTP/HTTPS proxy on port 3128 (using [goproxy](https://github.com/elazarl/goproxy))
- Generates a self-signed CA (RSA 2048, 10-year validity) for MITM of managed HTTPS traffic
- Credential injection via configurable HTTP headers
- Policy enforcement: allowed methods, path globs, token-bucket rate limiting
- PostgreSQL TCP proxy: intercepts PG wire protocol, swaps in real credentials (cleartext and MD5 auth)
- Destination blocking via glob patterns
- All actions logged to stdout (timestamp, action, destination)

## Google OAuth Setup

1. Go to [Google Cloud Console](https://console.cloud.google.com/apis/credentials)
2. Create an OAuth 2.0 Client ID (Web application type)
3. Set the authorized redirect URI to: `https://<BOX_DOMAIN>/oauth2/callback`
4. Copy the Client ID and Client Secret into your `.env`
5. Generate a cookie secret:
   ```bash
   python3 -c "import secrets,base64; print(base64.b64encode(secrets.token_bytes(32)).decode())"
   ```

## Volumes

| Volume         | Purpose                                                  |
|----------------|----------------------------------------------------------|
| `workspace`    | Persistent `/workspace` directory inside the box         |
| `caddy_data`   | TLS certificates and Caddy persistent data               |
| `caddy_config` | Caddy runtime configuration                              |
| `proxy_data`   | Outbound proxy CA certificate and private key             |
| `proxy_ca`     | Shared volume — proxy writes CA cert, box reads it        |

## Verifying the Setup

From inside the box (via the web terminal):

```bash
# Managed tool — proxy injects credentials automatically
curl https://api.github.com/user
# Should return your GitHub user profile

# Blocked destination — returns 403
curl https://competitor.com
# 403 Forbidden

# Open internet — passes through directly
curl https://example.com
# Returns example.com HTML

# No credentials in the box
env | grep -i token
env | grep -i api_key
# Both should be empty
```

## Troubleshooting

**Container won't start:**
```bash
docker compose logs <service-name>
```

**TLS certificate not provisioning:**
- Ensure your domain's DNS A record points to the server
- Ensure ports 80 and 443 are open and not used by another process
- Check Caddy logs: `docker compose logs caddy`

**OAuth redirect error:**
- Verify the redirect URI in Google Cloud Console matches exactly: `https://<BOX_DOMAIN>/oauth2/callback`
- Check that `ALLOWED_EMAIL_DOMAIN` matches your Google account's domain

**Proxy not injecting credentials:**
- Check that `config.yml` exists and is valid YAML
- Verify environment variables are set in `.env`
- Check proxy logs: `docker compose logs outbound-proxy`
- Ensure the `match` pattern in config matches the actual destination host

**Box can't reach the internet:**
- Verify the outbound-proxy container is running
- Check that `HTTP_PROXY` and `HTTPS_PROXY` are set inside the box: `docker compose exec box env | grep -i proxy`
- Check proxy logs for blocked or errored requests

**CA certificate not trusted:**
- The box trusts the proxy CA on startup via `update-ca-certificates`
- For Node.js, `NODE_EXTRA_CA_CERTS` is set automatically
- For Python requests, `REQUESTS_CA_BUNDLE` is set automatically
- If issues persist, check: `docker compose exec box ls /usr/local/share/ca-certificates/proxy/`
