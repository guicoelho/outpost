#!/usr/bin/env bash
set -Eeuo pipefail

sync_boxapps() {
    /usr/local/bin/boxapps-sync.sh || true
}

if [ -f /usr/local/share/ca-certificates/proxy/ca.crt ]; then
    update-ca-certificates 2>/dev/null
    export NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/proxy/ca.crt
    export REQUESTS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt
fi

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
