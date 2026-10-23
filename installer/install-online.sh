#!/bin/sh
set -eu
[ "$(uname -s)/$(uname -m)" = Linux/x86_64 ] || { echo '需要 Linux amd64。' >&2; exit 1; }
release=${1:?需要 HTTPS 发行目录地址}
shift
case "$release" in https://*) ;; *) echo '发行目录必须使用 HTTPS' >&2; exit 1;; esac
umask 077
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
curl --fail --silent --show-error --proto '=https' --proto-redir '=https' -o "$temporary/jingjiaagent" "$release/jingjiaagent"
printf '%s  %s\n' '__BINARY_SHA256__' "$temporary/jingjiaagent" | sha256sum -c -
chmod 700 "$temporary/jingjiaagent"
"$temporary/jingjiaagent" install --release-url "$release" "$@"
