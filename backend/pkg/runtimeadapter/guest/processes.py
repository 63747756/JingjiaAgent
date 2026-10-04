"""Read sandbox-local process metadata. Never read argv or process environments."""
import json
import os
import pathlib
import time


def snapshot(proc_root="/proc", limit=2048):
    root = pathlib.Path(proc_root)
    boot = None
    with (root / "stat").open(encoding="ascii") as stream:
        for line in stream:
            if line.startswith("btime "):
                boot = int(line.split()[1])
                break
    if boot is None:
        raise ValueError("missing process boot time")
    ticks = os.sysconf("SC_CLK_TCK")
    items = []
    for name in sorted((p.name for p in root.iterdir() if p.name.isdigit()), key=int):
        if int(name) == os.getpid():
            continue
        directory = root / name
        try:
            stat = (directory / "stat").read_text(encoding="utf-8")
            # comm may contain spaces and ')'; fields after the closing ')' start at field 3.
            fields = stat[stat.rindex(")") + 2:].split()
            started = boot + int(fields[19]) // ticks
            executable = os.readlink(directory / "exe")[:4096]
            # Cmdline intentionally carries only the executable: CLI arguments
            # can contain prompts, signed URLs, inline code and credentials.
            items.append({"pid": int(name), "exepath": executable,
                          "cmdline": executable, "start_time": started})
        except (OSError, UnicodeError, ValueError, IndexError):
            # A process can exit while /proc is being sampled; kernel threads have no exe.
            continue
        if len(items) > limit:
            raise ValueError("process snapshot exceeds limit")
    return {"processes": items, "processes_collected_at": int(time.time()),
            "arch": os.uname().machine, "hostname": os.uname().nodename}


if __name__ == "__main__":
    print(json.dumps(snapshot(), ensure_ascii=True))
