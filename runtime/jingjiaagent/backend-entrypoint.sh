#!/bin/sh
# Docker Desktop file-backed secrets can appear mode 0777 inside Linux.
# Keep the installation key read-only, and hand the service a private copy.
set -eu
umask 077

source=/run/secrets/ad-secret-key-source
target=/run/secrets/ad-secret-key
fail() { printf '%s\n' 'AD secret provisioning failed; preserve and restore the original 32-byte installation key.' >&2; exit 1; }

[ "${JINGJIAAGENT_AD_SECRET_KEY_FILE:-$target}" = "$target" ] || fail
[ -f "$source" ] && [ ! -L "$source" ] || fail
[ "$(wc -c < "$source")" -eq 32 ] || fail

if [ -e "$target" ] || [ -L "$target" ]; then
    [ -f "$target" ] && [ ! -L "$target" ] || fail
    [ "$(stat -c %a "$target")" = 600 ] || fail
    cmp -s "$source" "$target" || fail
else
    temporary=$(mktemp /run/secrets/.ad-secret-key.XXXXXX)
    trap 'rm -f "$temporary"' EXIT HUP INT TERM
    cat "$source" > "$temporary"
    [ "$(wc -c < "$temporary")" -eq 32 ] || fail
    chmod 600 "$temporary"
    # Do not replace a key if another startup has already provisioned it.
    mv -T -n "$temporary" "$target"
    [ -f "$target" ] && [ ! -L "$target" ] || fail
    [ "$(stat -c %a "$target")" = 600 ] || fail
    cmp -s "$source" "$target" || fail
    trap - EXIT HUP INT TERM
    [ ! -e "$temporary" ] || rm -f "$temporary"
fi

exec /app/main "$@"
