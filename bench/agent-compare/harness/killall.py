#!/usr/bin/env python3
"""Beendet alle Benchmark-Prozesse: Kettenläufe, Agenten, Worker, ctx serve.
Erkennung über die Umgebung (HOME unter einem Runs-Verzeichnis) oder die Kommandozeile."""
import os
import signal
import sys
import time
from pathlib import Path

ROOTS = [os.environ.get("BENCH_RUNS", "/var/tmp/agentcmp/runs").encode()]
me = os.getpid()
victims = []
for p in Path("/proc").iterdir():
    if not p.name.isdigit() or int(p.name) in (me, os.getppid()):
        continue
    try:
        env = (p / "environ").read_bytes()
        cmd = (p / "cmdline").read_bytes()
    except OSError:
        continue
    hit = any(b"HOME=" + r in env for r in ROOTS) or b"bench.py\0chain" in cmd or \
        (b"ctx\0serve" in cmd and any(r.rsplit(b"/", 1)[0] in cmd for r in ROOTS))
    if hit:
        victims.append(int(p.name))
for sig in (signal.SIGTERM, signal.SIGKILL):
    for pid in victims:
        try:
            os.kill(pid, sig)
        except OSError:
            pass
    time.sleep(2)
print("killed", len(victims))
