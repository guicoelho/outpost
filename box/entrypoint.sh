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

sed -i "s/BOX_DOMAIN_PLACEHOLDER/${BOX_DOMAIN:-<your-domain>}/g" /etc/opencode/AGENTS.md

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
# Apply to /etc/opencode/AGENTS.md in-place (loaded via opencode.json instructions).
sed -i -e "/TOOLS_JSON_PLACEHOLDER/{ r $tools_file" -e "d; }" /etc/opencode/AGENTS.md
# Also copy to /workspace for context discovery.
cp /etc/opencode/AGENTS.md /workspace/AGENTS.md
rm -f /tmp/empty-tools.json

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

exec ttyd -p 7681 -W opencode
