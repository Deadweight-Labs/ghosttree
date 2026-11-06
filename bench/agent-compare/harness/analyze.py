#!/usr/bin/env python3
"""Auswertung: Erfolgsquoten je Arm mit Bootstrap-Intervallen, gepaarte
Kontraste über Aufgaben, Aufwand, Mechanismus-Nachweis.

Aufruf: analyze.py [--include-pilot] [--json out.json]
"""
import json
import os
import random
import re
import statistics
import sys
from collections import defaultdict
from datetime import datetime
from pathlib import Path

BASE = Path(os.environ.get("BENCH_BASE", Path.home() / "agentbench-data" / "agent-compare"))
ARMS = ["vanilla", "native", "claudemem", "ghosttree"]
PROJECT = sys.argv[sys.argv.index("--project") + 1] if "--project" in sys.argv else "ledgerly"
RESDIR = BASE / ("results" if PROJECT == "ledgerly" else "results-" + PROJECT)
KTASKS = ["F1", "F2", "F3", "F4", "F5", "F6"] if PROJECT == "ledgerly" else ["NF1", "NF2"]
# Bei zwei Aufgaben trägt ein Bootstrap über Aufgaben nicht: dann über Ketten (präregistriert).
BY_CHAIN = PROJECT != "ledgerly"
B = 10000
rng = random.Random(20261105)


EXCLUDED_CHAINS = []


def load(include_pilot=False):
    fol, learn, chains = [], [], {}
    for d in sorted(RESDIR.iterdir()):
        cj = d / "chain.json"
        if not cj.exists():
            continue
        meta = json.loads(cj.read_text())
        if meta.get("rep", 0) == 0 and not include_pilot:
            continue
        if meta.get("error"):
            # Abgebrochene Kette (Harnessfehler): komplett ausgeschlossen, Ersatzkette läuft neu
            EXCLUDED_CHAINS.append({"cid": meta["cid"], "arm": meta["arm"], "rep": meta["rep"], "error": meta["error"]})
            continue
        chains[meta["cid"]] = meta
        for line in (d / "records.jsonl").read_text().splitlines():
            r = json.loads(line)
            r["_dir"] = d
            (fol if r["kind"] == "followup" else learn).append(r)
    return fol, learn, chains


def inv_cost(r):
    return sum((i.get("cost_usd") or 0) for i in r["invocations"])


def inv_tokens(r):
    t = 0
    for i in r["invocations"]:
        u = i.get("usage") or {}
        t += sum(u.get(k, 0) or 0 for k in ("input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "output_tokens"))
    return t


def valid(r):
    """Präregistrierte Gültigkeit: Modellregel eingehalten, kein endgültiger Infrastrukturfehler."""
    if r.get("infra_failure"):
        return False, "infra"
    for i in r["invocations"]:
        if not (i.get("model_check") or {}).get("ok"):
            return False, "model:" + str((i.get("model_check") or {}).get("reason"))
    return True, ""


def mean(xs):
    return sum(xs) / len(xs) if xs else float("nan")


def ci(samples):
    s = sorted(samples)
    return s[int(0.025 * len(s))], s[int(0.975 * len(s)) - 1]


def chain_means(rows_arm, key="success"):
    d = defaultdict(list)
    for x in rows_arm:
        d[x["cid"]].append(x[key])
    return [mean(v) for v in d.values()]


def boot_chain(xs):
    out = [mean([rng.choice(xs) for _ in xs]) for _ in range(B)]
    return ci(out)


def boot_chain_contrast(xa, xb):
    out = [mean([rng.choice(xa) for _ in xa]) - mean([rng.choice(xb) for _ in xb]) for _ in range(B)]
    return mean(xa) - mean(xb), ci(out)


def boot_rate(by_task):
    """by_task: {task: [0/1,...]}; Aufgaben ziehen, dann Ketten innerhalb der Aufgabe."""
    tasks = list(by_task)
    out = []
    for _ in range(B):
        vals = []
        for t in (rng.choice(tasks) for _ in tasks):
            xs = by_task[t]
            vals.append(mean([rng.choice(xs) for _ in xs]))
        out.append(mean(vals))
    return ci(out)


def boot_contrast(a, b):
    tasks = [t for t in a if t in b and a[t] and b[t]]
    out = []
    for _ in range(B):
        d = []
        for t in (rng.choice(tasks) for _ in tasks):
            xa, xb = a[t], b[t]
            d.append(mean([rng.choice(xa) for _ in xa]) - mean([rng.choice(xb) for _ in xb]))
        out.append(mean(d))
    point = mean([mean(a[t]) - mean(b[t]) for t in tasks])
    return point, ci(out)


OBS_RE = re.compile(r"^\[(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d\.\d+)\].*Observer turn usage \{([^}]*)\}")


def observer_usage(logdir, t_from, t_to=None):
    tot = defaultdict(int)
    if not logdir.exists():
        return dict(tot)
    for f in logdir.glob("*.log"):
        for line in f.read_text(errors="replace").splitlines():
            m = OBS_RE.match(line)
            if not m:
                continue
            ts = datetime.strptime(m.group(1), "%Y-%m-%d %H:%M:%S.%f").timestamp()
            if ts < t_from or (t_to and ts > t_to):
                continue
            for kv in m.group(2).split(","):
                k, _, v = kv.strip().partition("=")
                if v.isdigit():
                    tot[k] += int(v)
            tot["turns"] += 1
    return dict(tot)


def main():
    include_pilot = "--include-pilot" in sys.argv
    fol, learn, chains = load(include_pilot)
    report = {"n_chains": defaultdict(int), "excluded": [], "arms": {}, "contrasts": {}, "per_task": {}, "control": {}}
    for c in chains.values():
        report["n_chains"][c["arm"]] += 1
    rows = []
    for r in fol:
        ok, why = valid(r)
        if not ok:
            report["excluded"].append({"cid": r["cid"], "arm": r["arm"], "task": r["task"], "why": why})
            continue
        g = r["grade"]
        tx = r.get("transcript") or {}
        inv = r["invocations"][-1]
        rows.append({"arm": r["arm"], "rep": r["rep"], "cid": r["cid"], "task": r["task"], "success": int(bool(g["success"])),
                     "func": int(bool(g["func"])), "know": None if g["know"] is None else int(bool(g["know"])),
                     "cost": inv_cost(r), "tokens": inv_tokens(r), "turns": inv.get("num_turns") or 0,
                     "tool_calls": tx.get("tool_calls", 0), "wall": inv.get("wall_s") or 0,
                     "kw_injected": tx.get("kw_injected"), "kw_memory_tool": tx.get("kw_memory_tool"),
                     "kw_tool_result": tx.get("kw_tool_result"), "memory_tool_calls": tx.get("memory_tool_calls", 0),
                     "max_turns": inv.get("subtype") == "error_max_turns", "timed_out": inv.get("timed_out"),
                     "attempt": r["attempt"], "removed": r.get("removed_memfiles") or []})
    succ = {a: defaultdict(list) for a in ARMS}
    for x in rows:
        if x["task"] in KTASKS:
            succ[x["arm"]][x["task"]].append(x["success"])
    for a in ARMS:
        ar = [x for x in rows if x["arm"] == a and x["task"] in KTASKS]
        if not ar:
            continue
        lo, hi = boot_chain(chain_means(ar)) if BY_CHAIN else boot_rate(succ[a])
        know = [x["know"] for x in ar if x["know"] is not None]
        report["arms"][a] = {
            "n": len(ar), "success": mean([x["success"] for x in ar]), "success_ci": [lo, hi],
            "func": mean([x["func"] for x in ar]), "know": mean(know),
            "cost_mean": mean([x["cost"] for x in ar]), "tokens_mean": mean([x["tokens"] for x in ar]),
            "turns_mean": mean([x["turns"] for x in ar]), "tool_calls_mean": mean([x["tool_calls"] for x in ar]),
            "wall_mean": mean([x["wall"] for x in ar]),
            "kw_injected": mean([1 if x["kw_injected"] else 0 for x in ar]),
            "kw_memory_tool": mean([1 if x["kw_memory_tool"] else 0 for x in ar]),
            "memory_tool_calls_mean": mean([x["memory_tool_calls"] for x in ar]),
            "max_turns": sum(1 for x in ar if x["max_turns"]), "timeouts": sum(1 for x in ar if x["timed_out"]),
            "retries": sum(1 for x in ar if x["attempt"] > 0),
        }
        ctl = [x for x in rows if x["arm"] == a and x["task"] == "F0"]
        if ctl:
            report["control"][a] = {"n": len(ctl), "success": mean([x["success"] for x in ctl]),
                                    "cost_mean": mean([x["cost"] for x in ctl]), "tokens_mean": mean([x["tokens"] for x in ctl]),
                                    "turns_mean": mean([x["turns"] for x in ctl]), "tool_calls_mean": mean([x["tool_calls"] for x in ctl]),
                                    "wall_mean": mean([x["wall"] for x in ctl])}
    # Effizienz-Kontraste (Kosten, Werkzeugaufrufe) gepaart über Aufgaben
    def by_task(a, key):
        d = defaultdict(list)
        for x in rows:
            if x["arm"] == a and x["task"] in KTASKS:
                d[x["task"]].append(x[key])
        return d
    for other in ["vanilla", "native", "claudemem"]:
        if other in report["arms"] and "ghosttree" in report["arms"]:
            if BY_CHAIN:
                ga = [x for x in rows if x["arm"] == "ghosttree" and x["task"] in KTASKS]
                oa = [x for x in rows if x["arm"] == other and x["task"] in KTASKS]
                p, (lo, hi) = boot_chain_contrast(chain_means(ga), chain_means(oa))
                pk, (klo, khi) = boot_chain_contrast(chain_means(ga, "know"), chain_means(oa, "know"))
                pc, (clo, chi) = boot_chain_contrast(chain_means(ga, "cost"), chain_means(oa, "cost"))
                pt, (tlo, thi) = boot_chain_contrast(chain_means(ga, "tool_calls"), chain_means(oa, "tool_calls"))
                report["contrasts"][f"ghosttree-{other}"] = {"success": [p, lo, hi], "know": [pk, klo, khi],
                                                            "cost": [pc, clo, chi], "tool_calls": [pt, tlo, thi]}
                continue
            p, (lo, hi) = boot_contrast(succ["ghosttree"], succ[other])
            pk, (klo, khi) = boot_contrast(by_task("ghosttree", "know"), by_task(other, "know")) if all(
                x["know"] is not None for x in rows if x["task"] in KTASKS) else (None, (None, None))
            pc, (clo, chi) = boot_contrast(by_task("ghosttree", "cost"), by_task(other, "cost"))
            pt, (tlo, thi) = boot_contrast(by_task("ghosttree", "tool_calls"), by_task(other, "tool_calls"))
            report["contrasts"][f"ghosttree-{other}"] = {"success": [p, lo, hi], "know": [pk, klo, khi],
                                                        "cost": [pc, clo, chi], "tool_calls": [pt, tlo, thi]}
    for a in ["native", "claudemem"]:
        if a in report["arms"] and "vanilla" in report["arms"]:
            if BY_CHAIN:
                p, (lo, hi) = boot_chain_contrast(chain_means([x for x in rows if x["arm"] == a and x["task"] in KTASKS]),
                                                  chain_means([x for x in rows if x["arm"] == "vanilla" and x["task"] in KTASKS]))
            else:
                p, (lo, hi) = boot_contrast(succ[a], succ["vanilla"])
            report["contrasts"][f"{a}-vanilla"] = {"success": [p, lo, hi]}
    for t in (["F0"] if PROJECT == "ledgerly" else []) + KTASKS:
        report["per_task"][t] = {}
        for a in ARMS:
            xs = [x for x in rows if x["arm"] == a and x["task"] == t]
            if xs:
                report["per_task"][t][a] = {"success": f"{sum(x['success'] for x in xs)}/{len(xs)}",
                                            "func": f"{sum(x['func'] for x in xs)}/{len(xs)}",
                                            "know": f"{sum(x['know'] or 0 for x in xs)}/{len(xs)}" if t != "F0" else "-",
                                            "kw_injected": f"{sum(1 for x in xs if x['kw_injected'])}/{len(xs)}",
                                            "kw_memtool": f"{sum(1 for x in xs if x['kw_memory_tool'])}/{len(xs)}"}
    # Lernkosten und Zustandsnachweis je Kette
    lc = defaultdict(list)
    for r in learn:
        if r["cid"] in chains:
            lc[(r["arm"], r["cid"])].append(inv_cost(r))
    report["learning_cost_per_chain"] = {a: mean([sum(v) for (arm, _), v in lc.items() if arm == a]) for a in ARMS}
    report["state_evidence"] = {cid: {"arm": c["arm"], "rep": c["rep"], **(c.get("state_evidence") or {})} for cid, c in chains.items()}
    # claude-mem-Observer
    obs = {}
    for cid, c in chains.items():
        if c["arm"] != "claudemem":
            continue
        d = RESDIR / cid
        tot = defaultdict(int)
        lrecs = [r for r in learn if r["cid"] == cid]
        if lrecs:
            last = max(lrecs, key=lambda r: r["ts"])
            for k, v in observer_usage(d / f"{last['task']}.a{last['attempt']}.cmem-logs", 0).items():
                tot["learn_" + k] += v
        for r in fol:
            if r["cid"] != cid:
                continue
            t0 = r["invocations"][-1].get("t_start") or 0
            for k, v in observer_usage(d / f"{r['task']}.a{r['attempt']}.cmem-logs", t0).items():
                tot["followup_" + k] += v
        obs[cid] = dict(tot)
    report["claudemem_observer"] = obs
    report["removed_memfiles"] = defaultdict(int)
    for x in rows:
        if x["removed"]:
            report["removed_memfiles"][x["arm"]] += 1
    for r in learn:
        if r.get("removed_memfiles"):
            report["removed_memfiles"][r["arm"] + "_learning"] += 1
    out = json.dumps(report, indent=1, default=str)
    if "--json" in sys.argv:
        Path(sys.argv[sys.argv.index("--json") + 1]).write_text(out)
    print_report(report)


def f(x, nd=3):
    return "–" if x is None or (isinstance(x, float) and x != x) else (f"{x:+.{nd}f}" if isinstance(x, float) and x < 0 else f"{x:.{nd}f}")


def print_report(rep):
    print("Ketten:", dict(rep["n_chains"]), " ausgeschlossene Sessions:", len(rep["excluded"]), " ausgeschlossene Ketten:", EXCLUDED_CHAINS)
    for e in rep["excluded"]:
        print("   ", e)
    print("\nArm        n   Erfolg [95%]          func   know   USD/Sess Tokens   Züge  Tools  Zeit(s)  injiziert memTool")
    for a, s in rep["arms"].items():
        print(f"{a:10} {s['n']:3}  {s['success']:.3f} [{s['success_ci'][0]:.3f},{s['success_ci'][1]:.3f}]  {s['func']:.3f}  {s['know']:.3f}  "
              f"{s['cost_mean']:.3f}   {s['tokens_mean']:8.0f} {s['turns_mean']:5.1f} {s['tool_calls_mean']:5.1f}  {s['wall_mean']:6.1f}   "
              f"{s['kw_injected']:.2f}      {s['kw_memory_tool']:.2f}")
    print("\nKontraste (Punkt [95%]):")
    for k, v in rep["contrasts"].items():
        print(f"  {k:22} " + "  ".join(f"{m}={f(x[0])} [{f(x[1])},{f(x[2])}]" for m, x in v.items() if x[0] is not None))
    print("\nJe Aufgabe (Erfolg):")
    print("      " + "".join(f"{a:>12}" for a in ARMS))
    for t, d in rep["per_task"].items():
        print(f"  {t}  " + "".join(f"{d.get(a, {}).get('success', '-'):>12}" for a in ARMS))
    print("\nJe Aufgabe (Wissen injiziert / per Gedächtniswerkzeug):")
    for t, d in rep["per_task"].items():
        print(f"  {t}  " + "".join(f"{d.get(a, {}).get('kw_injected', '-') + '/' + d.get(a, {}).get('kw_memtool', '-'):>12}" for a in ARMS))
    print("\nKontrolle F0:", json.dumps(rep["control"], default=str))
    print("Lernkosten je Kette (USD):", {k: round(v, 3) for k, v in rep["learning_cost_per_chain"].items() if v == v})
    print("claude-mem Observer:", rep["claudemem_observer"])
    print("Entfernte Gedächtnisdateien (Sessions):", dict(rep["removed_memfiles"]))


if __name__ == "__main__":
    main()
