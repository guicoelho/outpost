#!/usr/bin/env bash
set -euo pipefail

REPO="guicoelho/outpost"
DIR="./outpost"
VERSION=""
UPDATE=false

usage() {
  cat <<EOF
Usage: $0 [options]

Options:
  --version VERSION   Install a specific version (e.g. v0.0.1). Default: latest.
  --dir DIR           Installation directory. Default: ./outpost
  --update            Update an existing installation in place.
  -h, --help          Show this help message.

Install:
  curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | bash

Update:
  bash install.sh --update
EOF
  exit 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --dir)     DIR="$2";     shift 2 ;;
    --update)  UPDATE=true;  shift   ;;
    -h|--help) usage ;;
    *) echo "Unknown option: $1"; usage ;;
  esac
done

resolve_version() {
  if [[ -n "$VERSION" ]]; then
    echo "$VERSION"
    return
  fi
  local latest
  latest=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)
  if [[ -z "$latest" ]]; then
    echo "Error: could not determine latest release." >&2
    exit 1
  fi
  echo "$latest"
}

download() {
  local version="$1" file="$2" dest="$3"
  curl -fsSL "https://github.com/${REPO}/releases/download/${version}/${file}" -o "$dest"
}

# -------------------------------------------------------------------
# Update mode
# -------------------------------------------------------------------
if $UPDATE; then
  if [[ ! -f "$DIR/.env" ]]; then
    echo "Error: no existing installation found at $DIR (missing .env)." >&2
    exit 1
  fi

  VERSION=$(resolve_version)
  echo "Updating Outpost to ${VERSION} in ${DIR}..."

  download "$VERSION" docker-compose.yml      "$DIR/docker-compose.yml"
  download "$VERSION" Caddyfile.tmpl           "$DIR/Caddyfile.tmpl"
  download "$VERSION" caddy-entrypoint.sh      "$DIR/caddy-entrypoint.sh"
  chmod +x "$DIR/caddy-entrypoint.sh"
  mkdir -p "$DIR/oauth2-proxy/templates"
  download "$VERSION" sign_in.html             "$DIR/oauth2-proxy/templates/sign_in.html"

  # Update OUTPOST_VERSION in .env
  if grep -q '^OUTPOST_VERSION=' "$DIR/.env"; then
    sed -i.bak "s/^OUTPOST_VERSION=.*/OUTPOST_VERSION=${VERSION}/" "$DIR/.env" && rm -f "$DIR/.env.bak"
  else
    echo "OUTPOST_VERSION=${VERSION}" >> "$DIR/.env"
  fi

  echo "Pulling images and restarting..."
  docker compose -f "$DIR/docker-compose.yml" --env-file "$DIR/.env" pull
  docker compose -f "$DIR/docker-compose.yml" --env-file "$DIR/.env" up -d

  echo "Updated to ${VERSION}."
  exit 0
fi

# -------------------------------------------------------------------
# Install mode
# -------------------------------------------------------------------
VERSION=$(resolve_version)
echo "Installing Outpost ${VERSION} into ${DIR}..."

download "$VERSION" docker-compose.yml      "$DIR/docker-compose.yml"
download "$VERSION" .env.example            "$DIR/.env.example"
download "$VERSION" config.example.yml      "$DIR/config.example.yml"
download "$VERSION" Caddyfile.tmpl           "$DIR/Caddyfile.tmpl"
download "$VERSION" caddy-entrypoint.sh      "$DIR/caddy-entrypoint.sh"
chmod +x "$DIR/caddy-entrypoint.sh"
mkdir -p "$DIR/oauth2-proxy/templates"
download "$VERSION" sign_in.html             "$DIR/oauth2-proxy/templates/sign_in.html"

# Generate .env from template
cp "$DIR/.env.example" "$DIR/.env"

# Auto-generate COOKIE_SECRET
COOKIE_SECRET=$(python3 -c "import secrets,base64; print(base64.b64encode(secrets.token_bytes(32)).decode())" 2>/dev/null \
  || openssl rand -base64 32 2>/dev/null \
  || head -c 32 /dev/urandom | base64)
sed -i.bak "s/^COOKIE_SECRET=.*/COOKIE_SECRET=${COOKIE_SECRET}/" "$DIR/.env" && rm -f "$DIR/.env.bak"

# Write pinned version
if grep -q '^#.*OUTPOST_VERSION' "$DIR/.env"; then
  sed -i.bak "s/^#.*OUTPOST_VERSION=.*/OUTPOST_VERSION=${VERSION}/" "$DIR/.env" && rm -f "$DIR/.env.bak"
elif grep -q '^OUTPOST_VERSION=' "$DIR/.env"; then
  sed -i.bak "s/^OUTPOST_VERSION=.*/OUTPOST_VERSION=${VERSION}/" "$DIR/.env" && rm -f "$DIR/.env.bak"
else
  echo "OUTPOST_VERSION=${VERSION}" >> "$DIR/.env"
fi

# Create config.yml from example
cp "$DIR/config.example.yml" "$DIR/config.yml"

echo ""
echo "Outpost ${VERSION} installed to ${DIR}/"
echo ""
echo "Next steps:"
echo "  1. Edit ${DIR}/.env with your domain, OAuth, and API credentials"
echo "  2. Edit ${DIR}/config.yml to configure managed tools and blocklist"
echo "  3. Start Outpost:"
echo "       cd ${DIR} && docker compose up -d"
echo ""
