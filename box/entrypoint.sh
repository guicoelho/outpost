#!/usr/bin/env bash
set -Eeuo pipefail

sync_boxapps() {
    /usr/local/bin/boxapps-sync.sh || true
}

# Wait for the outbound-proxy to write the CA cert and tools manifest.
# depends_on only waits for container start, not readiness.
for i in $(seq 1 30); do
    if [ -f /usr/local/share/ca-certificates/proxy/ca.crt ] && [ -f /usr/local/share/ca-certificates/proxy/tools.json ]; then
        break
    fi
    sleep 0.5
done

if [ -f /usr/local/share/ca-certificates/proxy/ca.crt ]; then
    update-ca-certificates 2>/dev/null
    export NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/proxy/ca.crt
    export REQUESTS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt
fi

if [ -f /usr/local/share/ca-certificates/proxy/tools.json ]; then
    cp /usr/local/share/ca-certificates/proxy/tools.json /workspace/.tools.json
fi

agents_template=/etc/opencode/AGENTS.template.md
agents_file=/workspace/AGENTS.md
cp "$agents_template" "$agents_file"
sed -i "s/BOX_DOMAIN_PLACEHOLDER/${BOX_DOMAIN:-<your-domain>}/g" "$agents_file"

# Inline the tools manifest into AGENTS.md so the model sees available tools
# directly in its instructions without needing to read a separate file.
if [ -f /workspace/.tools.json ]; then
    tools_file=/workspace/.tools.json
else
    echo "[]" > /tmp/empty-tools.json
    tools_file=/tmp/empty-tools.json
fi
# Replace placeholder line with actual tools JSON content.
# Using sed's 'r' command avoids issues with special characters in JSON.
# Apply to /workspace/AGENTS.md in-place (loaded via opencode.json instructions).
sed -i -e "/TOOLS_JSON_PLACEHOLDER/{ r $tools_file" -e "d; }" "$agents_file"
rm -f /tmp/empty-tools.json

# Create persistent data directory for app databases (SQLite, etc.)
mkdir -p /workspace/.data
export DATA_DIR=/workspace/.data

# Persist OpenCode sessions across container rebuilds by symlinking its
# data directory into the persistent /workspace volume.
mkdir -p /workspace/.opencode-data
mkdir -p /root/.local/share
ln -sfn /workspace/.opencode-data /root/.local/share/opencode

caddy start --config /etc/caddy/Caddyfile --adapter caddyfile

if [ -f /workspace/.boxapps.json ]; then
    sync_boxapps
fi

(
    while true; do
        changed_file="$(inotifywait -q -e close_write,create,delete,move --format '%f' /workspace)"
        if [ "$changed_file" = ".boxapps.json" ]; then
            sync_boxapps
        fi
    done
) &

# Use tmux so all browser tabs share a single opencode session.
# This prevents ttyd from spawning a new opencode process per connection.
exec ttyd -p 7681 -W --ping-interval 30 \
    tmux new-session -A -s main opencode
