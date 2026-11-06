#!/usr/bin/env python3
"""Kurzauszug je Arm für eine Aufgabe: Bewertung, injizierter Wissensbezug,
Gedächtniszugriffe, Schlussantwort. Aufruf: examples.py F3 [--project nurlearn] [--rep 1]"""
import json
import os
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from bench import KEYWORDS  # noqa: E402

BASE = Path(os.environ.get("BENCH_BASE", Path.home() / "agentbench-data" / "agent-compare"))
task = sys.argv[1]
PROJECT = sys.argv[sys.argv.index("--project") + 1] if "--project" in sys.argv else "ledgerly"
REP = int(sys.argv[sys.argv.index("--rep") + 1]) if "--rep" in sys.argv else None
RES = BASE / ("results" if PROJECT == "ledgerly" else "results-" + PROJECT)
pats = [re.compile(p) for p in KEYWORDS.get(task, [])]
memread = re.compile(r"\.ghosttree/|/\.claude/projects/[^\"]*/memory|\.claude-mem|MEMORY\.md|mcp__")

for d in sorted(RES.iterdir()):
    cj = d / "chain.json"
    if not cj.exists():
        continue
    m = json.loads(cj.read_text())
    if m["rep"] == 0 or (REP and m["rep"] != REP):
        continue
    for l in (d / "records.jsonl").read_text().splitlines():
        r = json.loads(l)
        if r["kind"] != "followup" or r["task"] != task:
            continue
        g = r["grade"]
        print(f"\n### {m['arm']} r{m['rep']} ({d.name}): func={g['func']} know={g['know']} Kosten={r['invocations'][-1]['cost_usd']:.3f} Züge={r['invocations'][-1]['num_turns']}")
        tx = d / f"{task}.a{r['attempt']}.transcript.jsonl"
        for line in tx.read_text(errors="replace").splitlines():
            ev = json.loads(line)
            a = ev.get("attachment") or {}
            if a.get("type") in ("instructions", "hook_additional_context"):
                t = json.dumps(a, ensure_ascii=False)
                for p in pats:
                    mm = p.search(t)
                    if mm:
                        print(f"  INJIZIERT ({a.get('type')}/{a.get('hookEvent')}): …{t[max(0, mm.start() - 160):mm.end() + 120]}…".replace("\\n", " "))
                        break
            if ev.get("type") == "assistant":
                for b in (ev.get("message") or {}).get("content") or []:
                    if b.get("type") == "tool_use":
                        inp = json.dumps(b.get("input"), ensure_ascii=False)
                        if memread.search(b["name"] + inp):
                            print(f"  ZUGRIFF {b['name']}: {inp[:200]}")
        print("  ERGEBNIS:", r["invocations"][-1]["result_head"][:300].replace("\n", " "))
