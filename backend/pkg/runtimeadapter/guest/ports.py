"""Discover actual listening TCP ports without invoking an external command."""
import json
import os
import socket

def is_docker_dns(address):
    raw = bytes.fromhex(address)
    # Linux proc represents each address word in host byte order.
    raw = b"".join(raw[i:i + 4][::-1] for i in range(0, len(raw), 4))
    if len(raw) == 16:
        if raw[:12] != b"\x00" * 10 + b"\xff\xff":
            return False
        raw = raw[12:]
    # Docker's embedded resolver listens on a random TCP port at this address.
    # Keep normal loopback listeners: the preview tunnel can reach them.
    return raw == socket.inet_aton("127.0.0.11")


def discover_ports(proc="/proc"):
    ports = {}
    for name in ("tcp", "tcp6"):
        with open(f"{proc}/net/{name}", encoding="ascii") as stream:
            for line in list(stream)[1:]:
                fields = line.split()
                if fields[3] == "0A":
                    address, port = fields[1].split(":")
                    if is_docker_dns(address):
                        continue
                    port = int(port, 16)
                    ports[port] = {"port": port, "process": "", "inode": fields[9]}
    inodes = {item["inode"]: item for item in ports.values()}
    for pid in os.listdir(proc):
        if not pid.isdigit():
            continue
        try:
            with open(f"{proc}/{pid}/comm", encoding="utf-8") as stream:
                process = stream.read(128).strip()
            for fd in os.listdir(f"{proc}/{pid}/fd"):
                target = os.readlink(f"{proc}/{pid}/fd/{fd}")
                if target.startswith("socket:[") and target[8:-1] in inodes:
                    inodes[target[8:-1]]["process"] = process
        except (OSError, UnicodeError):
            continue
    return [{k: v for k, v in item.items() if k != "inode"}
            for _, item in sorted(ports.items())]


if __name__ == "__main__":
    print(json.dumps(discover_ports()))
