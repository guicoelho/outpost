#!/bin/sh
set -eu

CONFIG_FILE="/etc/box/config.yml"
TEMPLATE="/etc/caddy/Caddyfile.tmpl"
OUTPUT="/etc/caddy/Caddyfile"

# ---------------------------------------------------------------------------
# Extract allowed emails from config.yml.
# ---------------------------------------------------------------------------
emails=$(sed -n '/^allowed_users:/,/^[^ ]/{
    /^  *- /{
        s/^  *- *//
        s/ *#.*//
        s/^["'"'"']//
        s/["'"'"']$//
        p
    }
}' "$CONFIG_FILE" | grep -v '^$')

if [ -z "$emails" ]; then
    echo "FATAL: no allowed_users found in $CONFIG_FILE" >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# Build the Caddyfile from the template.
# Each email becomes:  not header X-Auth-Request-Email <email>
# ---------------------------------------------------------------------------
LINES_FILE=$(mktemp)
count=0
for email in $emails; do
    echo "        not header X-Auth-Request-Email ${email}" >> "$LINES_FILE"
    count=$((count + 1))
done

echo "INFO: loaded $count allowed user(s) from $CONFIG_FILE" >&2

# Replace placeholder in template and write the final Caddyfile.
sed -e "/__ALLOWED_USERS_LINES__/{
    r ${LINES_FILE}
    d
}" "$TEMPLATE" > "$OUTPUT"
rm -f "$LINES_FILE"

exec caddy run --config "$OUTPUT" --adapter caddyfile
