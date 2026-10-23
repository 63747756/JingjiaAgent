#!/bin/sh
set -eu
if [ -n "${JINGJIAAGENT_PLATFORM_CA:-}" ]; then
  umask 077
  trust_dir=$(mktemp -d /tmp/jingjiaagent-trust.XXXXXX)
  trap 'rm -rf "$trust_dir"' EXIT HUP INT TERM
  printf '%s' "$JINGJIAAGENT_PLATFORM_CA" | base64 -d > "$trust_dir/platform.pem"
  cat /etc/ssl/certs/ca-certificates.crt "$trust_dir/platform.pem" > "$trust_dir/bundle.pem"
  export NODE_EXTRA_CA_CERTS="$trust_dir/platform.pem" SSL_CERT_FILE="$trust_dir/bundle.pem" REQUESTS_CA_BUNDLE="$trust_dir/bundle.pem" CURL_CA_BUNDLE="$trust_dir/bundle.pem" GIT_SSL_CAINFO="$trust_dir/bundle.pem"
  node /opt/agent-compose-runtime/dist/cli.js "$@"
else
  exec node /opt/agent-compose-runtime/dist/cli.js "$@"
fi
