#!/usr/bin/env bash
# Run in an expendable ubuntu:24.04 amd64 container with /out bind-mounted.
# Downloads packages only; it never installs or starts Docker on the host.
set -euo pipefail

. /etc/os-release
test "${ID}:${VERSION_ID}" = "ubuntu:24.04"
test "$(dpkg --print-architecture)" = "amd64"
test -d /out
if find /out -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
    echo "/out must be an empty output directory." >&2
    exit 1
fi

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates curl
install -m 0755 -d /etc/apt/keyrings
curl --fail --silent --show-error --location \
    https://download.docker.com/linux/ubuntu/gpg \
    -o /etc/apt/keyrings/docker.asc
chmod 0644 /etc/apt/keyrings/docker.asc
cat > /etc/apt/sources.list.d/docker.sources <<'EOF'
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: noble
Components: stable
Architectures: amd64
Signed-By: /etc/apt/keyrings/docker.asc
EOF
apt-get update

mkdir -p /out/debs/partial
: > /tmp/jingjiaagent-empty-dpkg-status
# Resolve against an empty package database so dependencies already present
# in this build container are also downloaded for the offline machine.
apt-get -y --download-only --no-install-recommends \
    -o Dir::State::status=/tmp/jingjiaagent-empty-dpkg-status \
    -o Dir::Cache::archives=/out/debs \
    install docker-ce docker-ce-cli containerd.io \
    docker-buildx-plugin docker-compose-plugin \
    python3 python3-cryptography openssh-server curl ca-certificates iptables \
    openssl tar gzip

for package_file in /out/debs/*.deb; do
    dpkg-deb --show --showformat='${Package}\t${Version}\t${Architecture}\n' \
        "$package_file"
done | sort > /out/package-versions.tsv
cp /etc/apt/sources.list.d/docker.sources /out/docker.sources
cp /etc/apt/keyrings/docker.asc /out/docker.asc
(
    cd /out
    sha256sum debs/*.deb package-versions.tsv docker.sources docker.asc > SHA256SUMS
)
printf 'Ubuntu 24.04 amd64 packages prepared in /out.\n'
