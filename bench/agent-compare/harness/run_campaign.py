#!/usr/bin/env python3
"""Startet die gezählte Kampagne: Ketten je (Projekt, Arm, Wiederholung),
Arme reihum verschränkt (Reihenfolge je Wiederholung rotiert), höchstens
MAX_PAR Ketten gleichzeitig.

Aufruf: run_campaign.py [--ledgerly-reps 8] [--nurlearn-reps 6] [--max-par 12] [--start-rep 1]
"""
import argparse
import json
import os
import subprocess
import sys
import time
from pathlib import Path

HARNESS = Path(__file__).resolve().parent
LOGS = HARNESS.parent / "logs"
# claudemem läuft nicht in der Sandbox (Installation und Worker brauchen Netz), siehe bench.py SANDBOX_ARMS
ARMS = ["vanilla", "native", "ghosttree"]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--ledgerly-reps", type=int, default=8)
    ap.add_argument("--nurlearn-reps", type=int, default=6)
    ap.add_argument("--max-par", type=int, default=12)
    ap.add_argument("--start-rep", type=int, default=1)
    a = ap.parse_args()
    jobs = []
    for rep in range(a.start_rep, max(a.ledgerly_reps, a.nurlearn_reps) + 1):
        order = ARMS[(rep - 1) % len(ARMS):] + ARMS[:(rep - 1) % len(ARMS)]
        if rep <= a.ledgerly_reps:
            jobs += [("ledgerly", arm, rep) for arm in order]
        if rep <= a.nurlearn_reps:
            jobs += [("nurlearn", arm, rep) for arm in order]
    done = set()
    base = HARNESS.parent
    for proj, rd in (("ledgerly", base / "results"), ("nurlearn", base / "results-nurlearn")):
        for cj in rd.glob("*/chain.json"):
            m = json.loads(cj.read_text())
            if m.get("rep", 0) > 0 and not m.get("error") and m.get("post_learning_head"):
                done.add((proj, m["arm"], m["rep"]))
    skipped = [j for j in jobs if j in done]
    jobs = [j for j in jobs if j not in done]
    print(f"übersprungen (fertig): {len(skipped)}", flush=True)
    LOGS.mkdir(exist_ok=True)
    running = []
    t0 = time.time()
    print(f"{len(jobs)} Ketten, max {a.max_par} parallel", flush=True)
    while jobs or running:
        running = [(j, p) for j, p in running if p.poll() is None]
        while jobs and len(running) < a.max_par:
            proj, arm, rep = jobs.pop(0)
            env = dict(os.environ, BENCH_PROJECT=proj)
            log = open(LOGS / f"campaign-{proj}-{arm}-r{rep}.log", "w")
            p = subprocess.Popen([sys.executable, str(HARNESS / "bench.py"), "chain", arm, str(rep)], cwd=HARNESS,
                                 env=env, stdout=log, stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL)
            running.append(((proj, arm, rep), p))
            print(f"{time.strftime('%H:%M:%S')} start {proj} {arm} r{rep} (laufend {len(running)}, offen {len(jobs)})", flush=True)
            time.sleep(3)
        time.sleep(5)
    print(f"fertig nach {(time.time() - t0) / 60:.1f} min", flush=True)


if __name__ == "__main__":
    main()
