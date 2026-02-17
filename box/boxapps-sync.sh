#!/usr/bin/env bash
set -Eeuo pipefail

MANIFEST_FILE="/workspace/.boxapps.json"
STATE_FILE="/workspace/.boxapps.state.json"
CADDYFILE="/etc/caddy/Caddyfile"

log() {
    printf '[boxapps-sync] %s\n' "$*"
}

start_app() {
    local name="$1"
    local path="$2"
    local start_cmd="$3"

    if [ ! -d "$path" ]; then
        log "Skipping '$name': path '$path' does not exist"
        return
    fi

    pm2 start bash --name "$name" --cwd "$path" -- -lc "$start_cmd" >/dev/null
    log "Started '$name'"
}

manifest_json='[]'
if [ -f "$MANIFEST_FILE" ]; then
    if ! manifest_json="$(jq -c '.' "$MANIFEST_FILE" 2>/dev/null)"; then
        log "Manifest is invalid JSON; treating as empty"
        manifest_json='[]'
    fi
fi

if ! jq -e 'type == "array"' >/dev/null <<<"$manifest_json"; then
    log "Manifest root is not an array; treating as empty"
    manifest_json='[]'
fi

desired_apps="$(jq -c '
    [
        .[]
        | select(
            (.name | type == "string") and
            (.name | test("^[A-Za-z0-9._-]+$")) and
            (.path | type == "string") and
            (.path | length > 0) and
            ((.port | type == "number") or ((.port | type == "string") and (.port | test("^[0-9]+$")))) and
            (.start | type == "string") and
            (.start | length > 0)
        )
        | {
            name,
            path,
            port: (.port | tonumber),
            start
        }
    ]
' <<<"$manifest_json")"

previous_state='{}'
if [ -f "$STATE_FILE" ]; then
    if ! previous_state="$(jq -c '.' "$STATE_FILE" 2>/dev/null)"; then
        previous_state='{}'
    fi
fi
if ! jq -e 'type == "object"' >/dev/null <<<"$previous_state"; then
    previous_state='{}'
fi

pm2_json='[]'
if ! pm2_json="$(pm2 jlist 2>/dev/null)"; then
    pm2_json='[]'
fi
if ! jq -e 'type == "array"' >/dev/null <<<"$pm2_json"; then
    pm2_json='[]'
fi

declare -A desired_names
new_state='{}'

while IFS= read -r app; do
    [ -z "$app" ] && continue

    name="$(jq -r '.name' <<<"$app")"
    path="$(jq -r '.path' <<<"$app")"
    port="$(jq -r '.port' <<<"$app")"
    start_cmd="$(jq -r '.start' <<<"$app")"

    desired_names["$name"]=1
    new_state="$(jq -c --arg name "$name" --argjson app "$app" '. + {($name): $app}' <<<"$new_state")"

    exists="$(jq -r --arg name "$name" 'map(select(.name == $name)) | length > 0' <<<"$pm2_json")"
    desired_changed="$(jq -r --arg name "$name" --argjson app "$app" '(.[$name] // null) != $app' <<<"$previous_state")"

    if [ "$exists" = "true" ]; then
        status="$(jq -r --arg name "$name" '.[] | select(.name == $name) | .pm2_env.status' <<<"$pm2_json")"

        if [ "$desired_changed" = "true" ]; then
            pm2 delete "$name" >/dev/null || true
            start_app "$name" "$path" "$start_cmd"
        elif [ "$status" != "online" ]; then
            pm2 restart "$name" >/dev/null || {
                pm2 delete "$name" >/dev/null || true
                start_app "$name" "$path" "$start_cmd"
            }
            log "Restarted '$name'"
        fi
    else
        start_app "$name" "$path" "$start_cmd"
    fi

done < <(jq -c '.[]' <<<"$desired_apps")

while IFS= read -r current_name; do
    [ -z "$current_name" ] && continue
    if [ -z "${desired_names[$current_name]+x}" ]; then
        pm2 delete "$current_name" >/dev/null || true
        log "Removed stale app '$current_name'"
    fi
done < <(jq -r '.[].name' <<<"$pm2_json")

printf '%s\n' "$new_state" > "$STATE_FILE"

{
    echo ':8080 {'
    while IFS= read -r app; do
        [ -z "$app" ] && continue
        name="$(jq -r '.name' <<<"$app")"
        port="$(jq -r '.port' <<<"$app")"
        echo "    handle /apps/${name}/* {"
        echo "        uri strip_prefix /apps/${name}"
        echo "        reverse_proxy localhost:${port}"
        echo '    }'
    done < <(jq -c '.[]' <<<"$desired_apps")
    echo '    handle {'
    echo '        reverse_proxy localhost:7681'
    echo '    }'
    echo '}'
} > "$CADDYFILE"

if ! caddy reload --config "$CADDYFILE" --adapter caddyfile >/dev/null; then
    log "Failed to reload Caddy"
    exit 1
fi

pm2 save --force >/dev/null || true
