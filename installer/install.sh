#!/bin/sh
set -eu
case "$(uname -s)/$(uname -m)" in Linux/x86_64) ;; *) echo '需要 Linux amd64。' >&2; exit 1;; esac
base=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec "$base/jingjiaagent" install --offline --bundle "$base" "$@"
