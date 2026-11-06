#!/usr/bin/env python3
"""Belege für den Bericht: Systemprompt-Größe je Arm (erste Anfrage),
Werkzeugaufschlüsselung, Zustandsnachweis nach dem Lernen, Gedächtniszugriffe.

Aufruf: details.py [--project nurlearn]
"""
import json
import os
import statistics
import sys
from collections import Counter, defaultdict
from pathlib import Path

BASE = Path(os.environ.get("BENCH_BASE", Path.home() / "agentbench-data" / "agent-compare"))
PROJECT = sys.argv[sys.argv.index("--project") + 1] if "--project" in sys.argv else "ledgerly"
RESDIR = BASE / ("results" if PROJECT == "ledgerly" else "results-" + PROJECT)
ARMS = ["vanilla", "native", "claudemem", "ghosttree"]


def first_request_size(tx):
    for line in tx.read_text(errors="replace").splitlines():
        try:
            ev = json.loads(line)
        except Exception:
            continue
        if ev.get("type") == "assistant":
            u = (ev.get("message") or {}).get("usage") or {}
            return sum(u.get(k, 0) or 0 for k in ("input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"))
    return None


def main():
    chains = {}
    recs = []
    for d in RESDIR.iterdir():
        cj = d / "chain.json"
        if not cj.exists():
            continue
        m = json.loads(cj.read_text())
        if m.get("rep", 0) == 0 or m.get("error"):
            continue
        chains[m["cid"]] = m
        for l in (d / "records.jsonl").read_text().splitlines():
            r = json.loads(l)
            r["_dir"] = d
            recs.append(r)
    print(f"== {PROJECT}: {len(chains)} Ketten")
    # 1. Systemprompt-Größe der ersten Anfrage in der Kontroll- bzw. ersten Folgeaufgabe
    first_task = "F0" if PROJECT == "ledgerly" else "NF1"
    sizes = defaultdict(list)
    for r in recs:
        if r["kind"] == "followup" and r["task"] == first_task:
            tx = r["_dir"] / f"{r['task']}.a{r['attempt']}.transcript.jsonl"
            if tx.exists():
                s = first_request_size(tx)
                if s:
                    sizes[r["arm"]].append(s)
    print("\nKontext der ersten Anfrage (Tokens, Median [min,max]) in", first_task)
    for a in ARMS:
        if sizes[a]:
            print(f"  {a:10} {statistics.median(sizes[a]):8.0f} [{min(sizes[a])},{max(sizes[a])}] n={len(sizes[a])}")
    # 2. Instruktionsdateien und Isolationsverstöße
    inst = defaultdict(Counter)
    viol = 0
    for r in recs:
        for f in r.get("instruction_files") or []:
            inst[r["arm"]][f.split("/home/")[-1].split("/", 1)[-1] if "/home/" in f else f] += 1
        viol += len(r.get("isolation_violations") or [])
    print("\nGeladene Instruktionsdateien (Sessions):")
    for a in ARMS:
        print(f"  {a:10} {dict(inst[a]) or '-'}")
    print("Isolationsverstöße gesamt:", viol)
    # 3. Werkzeuge je Folgesession
    tools = defaultdict(Counter)
    n = Counter()
    for r in recs:
        if r["kind"] != "followup":
            continue
        n[r["arm"]] += 1
        for t, c in (r.get("transcript") or {}).get("tools", {}).items():
            tools[r["arm"]][t] += c
    print("\nWerkzeugaufrufe je Folgesession (Mittel):")
    for a in ARMS:
        if n[a]:
            print(f"  {a:10} " + ", ".join(f"{t}={c / n[a]:.2f}" for t, c in tools[a].most_common(8)))
    # 4. Gedächtniszugriffe
    print("\nFolgesessions mit Gedächtniszugriff (Spiegel/Memory/MCP) bzw. Wissen injiziert:")
    for a in ARMS:
        fs = [r for r in recs if r["kind"] == "followup" and r["arm"] == a and r["task"] not in ("F0",)]
        if fs:
            acc = sum(1 for r in fs if (r.get("transcript") or {}).get("memory_tool_calls", 0) > 0)
            inj = sum(1 for r in fs if (r.get("transcript") or {}).get("kw_injected"))
            kwm = sum(1 for r in fs if (r.get("transcript") or {}).get("kw_memory_tool"))
            print(f"  {a:10} Zugriff {acc}/{len(fs)}  Wissen injiziert {inj}/{len(fs)}  Wissen über Zugriff {kwm}/{len(fs)}")
    # 5. Zustandsnachweis nach dem Lernen
    print("\nZustand nach dem Lernen je Kette:")
    for cid, m in sorted(chains.items(), key=lambda x: (x[1]["arm"], x[1]["rep"])):
        ev = m.get("state_evidence") or {}
        bits = []
        if ev.get("auto_memory_files"):
            bits.append(f"auto-memory {len(ev['auto_memory_files'])} Dateien")
        if ev.get("repo_memfiles"):
            bits.append(f"repo {ev['repo_memfiles']}")
        if ev.get("cmem_counts"):
            bits.append(f"claude-mem {ev['cmem_counts']}")
        if ev.get("gt_counts"):
            c = ev["gt_counts"]
            bits.append("ghosttree " + ", ".join(f"{k}={v}" for k, v in c.items() if k in ("knowledge", "sessions", "file_descriptions", "pains", "ghost_files")))
        print(f"  {m['arm']:10} r{m['rep']} {cid}: " + ("; ".join(bits) or "–"))
    # 6. entfernte Gedächtnisdateien
    rm = Counter()
    for r in recs:
        if r.get("removed_memfiles"):
            rm[(r["arm"], r["kind"])] += 1
    print("\nSessions mit entfernten selbst angelegten Gedächtnisdateien:", dict(rm))


if __name__ == "__main__":
    main()
