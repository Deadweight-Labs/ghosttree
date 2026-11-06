#!/usr/bin/env python3
"""Mehrsession-Vergleich von Gedächtnis-Mechanismen für Claude Code.

Eine Kette (chain) = ein Arm x eine Wiederholung: frisches HOME, frisches
Projekt, frischer Gedächtniszustand. Ablauf: Lern-Sessions L1..L3 (je zwei
Züge, zweiter per --resume), Schnappschuss, dann jede Folgeaufgabe F0..F6 als
frische Session auf dem wiederhergestellten Schnappschuss, verdeckt bewertet.

Aufruf:
  bench.py chain <arm> <rep> [--only-learning] [--tasks F1,F2]
  bench.py grade <taskid> <workdir> <snapshot_sha>
"""
import json
import os
import re
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import tarfile
import time
import urllib.request
from pathlib import Path

BASE = Path(os.environ.get("BENCH_BASE", Path.home() / "agentbench-data" / "agent-compare"))
HARNESS = Path(__file__).resolve().parent
HIDDEN = Path(os.environ.get("BENCH_HIDDEN", HARNESS.parent / "hidden"))
TEMPLATE = HARNESS.parent / "projects" / "ledgerly"
SHARED = BASE / "shared"
# Außerhalb des echten Benutzer-HOME: Claude Code lädt CLAUDE.md aus allen Elternverzeichnissen
# des Arbeitsverzeichnisses, und ~/.claude/CLAUDE.md des Benutzers wäre sonst als "Project" im Kontext.
RUNS = Path(os.environ.get("BENCH_RUNS", "/var/tmp/agentcmp/runs"))
RESULTS = BASE / "results"
# Agent-Aufruf: claude selbst oder ein Wrapper, der Anmeldung/Route setzt (ANTHROPIC_BASE_URL usw.).
CLAUPHIL = os.environ.get("BENCH_AGENT", "claude")
MODEL = "claude-opus-5-5"
MAX_TURNS = "80"
SESSION_TIMEOUT = 1800
CLAUDE_MEM_VERSION = "15.10.0"
REMOTE = "https://github.com/acme-billing/ledgerly.git"
ARMS = ("vanilla", "native", "claudemem", "ghosttree")
MEMFILE_NAMES = {"CLAUDE.md", "CLAUDE.local.md", "AGENTS.md"}
ALLOWED_HELPER = re.compile(r"^claude-(haiku-4-5|haiku-5-5|sonnet-5-5)")
INFRA_PAT = re.compile(r"API Error|overloaded|ECONNREFUSED|ECONNRESET|Connection error|socket hang up|"
                       r"fetch failed|502|503|504|529|Request timed out|rate limit", re.I)

PROJECTS = {
    "ledgerly": {"template": TEMPLATE, "prompts": HARNESS / "prompts.json",
                 "hidden": HIDDEN / "ledgerly", "remote": REMOTE, "dir": "ledgerly", "results": BASE / "results",
                 "repo_tests": ["./..."]},
    "nurlearn": {"template": Path(os.environ.get("BENCH_NURLEARN_TEMPLATE", BASE / "template" / "nurlearn")), "prompts": HARNESS / "prompts-nurlearn.json",
                 "hidden": HIDDEN / "nurlearn" / "tests", "remote": "https://github.com/NurRobin/NurLearn.git",
                 "dir": "NurLearn", "results": BASE / "results-nurlearn", "repo_tests": ["./..."]},
}
PROJECT = os.environ.get("BENCH_PROJECT", "ledgerly")
P = PROJECTS[PROJECT]
PROMPTS = json.loads(P["prompts"].read_text())
RESULTS = P["results"]


def log(chain, msg):
    line = f"{time.strftime('%H:%M:%S')} [{chain}] {msg}"
    print(line, flush=True)
    with open(BASE / "bench.log", "a") as f:
        f.write(line + "\n")


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def sh(cmd, env=None, cwd=None, check=True, timeout=None, capture=True):
    r = subprocess.run(cmd, env=env, cwd=cwd, timeout=timeout, text=True,
                       stdout=subprocess.PIPE if capture else None,
                       stderr=subprocess.STDOUT if capture else None, stdin=subprocess.DEVNULL)
    if check and r.returncode != 0:
        raise RuntimeError(f"{cmd!r} -> {r.returncode}: {(r.stdout or '')[-2000:]}")
    return r


class Chain:
    def __init__(self, arm, rep, cid=None):
        assert arm in ARMS, arm
        self.arm, self.rep = arm, rep
        self.cid = cid or secrets.token_hex(3)
        self.root = RUNS / self.cid
        self.home = self.root / "home"
        self.work = self.home / "src" / P["dir"]
        self.out = RESULTS / self.cid
        self.meta_path = self.root / "chain.json"
        self.meta = {"arm": arm, "rep": rep, "cid": self.cid, "project": PROJECT}
        self.gt_proc = None

    # ---------- Umgebung ----------
    def env(self):
        bindirs = [str(self.home / ".local" / "bin"), str(SHARED / "bin")]
        if self.arm == "ghosttree":
            bindirs.append(str(SHARED / "ctx-bin"))
        e = {
            "PATH": ":".join(bindirs + ["/usr/local/bin", "/usr/bin", "/bin"]),
            "HOME": str(self.home),
            "USER": "main", "LOGNAME": "main",
            "LANG": "C.UTF-8", "TERM": "dumb", "SHELL": "/bin/bash",
            # Kurz halten: Tests binden Unix-Sockets unter TMPDIR (sun_path max. 108 Bytes).
            "TMPDIR": f"/tmp/ac-{self.cid}",
            "GOTOOLCHAIN": "local",
            "GOCACHE": str(SHARED / "gocache"),
            "GOMODCACHE": str(SHARED / "gomod"),
            "GIT_CONFIG_NOSYSTEM": "1",
            "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
            "DISABLE_AUTOUPDATER": "1",
        }
        if self.arm != "native":
            e["CLAUDE_CODE_DISABLE_AUTO_MEMORY"] = "1"
        if self.arm == "claudemem":
            e["CLAUDE_MEM_WORKER_PORT"] = str(self.meta["cmem_port"])
            e["CI"] = "1"
            e["CLAUDE_MEM_ONLINE_OPTIN"] = "false"
            e["NPM_CONFIG_CACHE"] = str(SHARED / "npm-cache")
            e["UV_CACHE_DIR"] = str(SHARED / "uv-cache")
        return e

    def save_meta(self):
        self.meta_path.write_text(json.dumps(self.meta, indent=2))

    # ---------- Aufbau ----------
    def prepare(self):
        self.root.mkdir(parents=True)
        Path(f"/tmp/ac-{self.cid}").mkdir(exist_ok=True)
        if self.arm == "claudemem":
            self.meta["cmem_port"] = free_port()
        self.out.mkdir(parents=True, exist_ok=True)
        self.home.mkdir()
        (self.home / ".claude").mkdir()
        settings = {"autoMemoryEnabled": self.arm == "native"}
        (self.home / ".claude" / "settings.json").write_text(json.dumps(settings, indent=2))
        (self.home / ".gitconfig").write_text("[user]\n\tname = Dev\n\temail = dev@acme-billing.example\n"
                                              "[init]\n\tdefaultBranch = main\n")
        shutil.copytree(P["template"], self.work)
        env = self.env()
        for c in (["git", "init", "-q"], ["git", "add", "-A"],
                  ["git", "commit", "-q", "-m", "ledgerly: Stand vor der Arbeit"],
                  ["git", "remote", "add", "origin", P["remote"]]):
            sh(c, env=env, cwd=self.work)
        if self.arm == "claudemem":
            self.save_meta()
            self.install_claudemem()
        if self.arm == "ghosttree":
            self.install_ghosttree()
        user_md = self.home / ".claude" / "CLAUDE.md"
        self.meta["baseline_user_claude_md"] = user_md.read_text() if user_md.exists() else None
        self.meta["baseline_settings"] = json.loads((self.home / ".claude" / "settings.json").read_text())
        self.save_meta()
        log(self.cid, f"prepared arm={self.arm} rep={self.rep}")

    def install_claudemem(self):
        env = self.env()
        r = sh(["npx", "-y", f"claude-mem@{CLAUDE_MEM_VERSION}", "install", "--provider", "claude"],
               env=env, cwd=self.work, check=False, timeout=900)
        (self.root / "claudemem-install.log").write_text(r.stdout or "")
        self.meta["cmem_install_rc"] = r.returncode
        if r.returncode != 0:
            raise RuntimeError("claude-mem install failed, see claudemem-install.log")
        self.stop_chain_procs()
        # Der Observer verwirft geerbte ANTHROPIC_*-Variablen und liest Zugangsdaten nur aus
        # ~/.claude-mem/.env. Für Nutzer hinter einem Gateway ist das der dokumentierte Weg
        # (CLAUDE_MEM_CLAUDE_AUTH_METHOD=gateway); hier zeigt er auf denselben Proxy wie der Agent.
        cm = self.home / ".claude-mem"
        st = json.loads((cm / "settings.json").read_text())
        gw = os.environ.get("BENCH_CMEM_GATEWAY_URL")
        if gw:
            with open(cm / ".env", "a") as f:
                f.write(f"ANTHROPIC_BASE_URL={gw}\nANTHROPIC_AUTH_TOKEN={os.environ.get('BENCH_CMEM_GATEWAY_TOKEN', 'none')}\n")
            st["CLAUDE_MEM_CLAUDE_AUTH_METHOD"] = "gateway"
        st["CLAUDE_MEM_WORKER_PORT"] = str(self.meta["cmem_port"])
        (cm / "settings.json").write_text(json.dumps(st, indent=2))

    def gt_env(self):
        e = self.env()
        e["HOME"] = str(self.root / "gt-server-home")
        return e

    def install_ghosttree(self):
        gt = self.root / "gt"
        gt.mkdir()
        (self.root / "gt-server-home").mkdir()
        port = free_port()
        self.meta["gt_port"] = port
        db = gt / "ghosttree.db"
        ctx = str(SHARED / "ctx-bin" / "ctx")
        r = sh([ctx, "person", "add", "dev", "--db", str(db)], env=self.gt_env())
        m = re.search(r"token:\s*(\S+)", r.stdout)
        token = m.group(1) if m else None
        if not token:
            raise RuntimeError("no token in: " + r.stdout)
        (gt / "person-add.log").write_text(r.stdout.replace(token, "<token>"))
        self.start_gt_server()
        env = self.env()
        r1 = sh([ctx, "setup", "--server", f"http://127.0.0.1:{port}", "--token", token], env=env, cwd=self.work, check=False)
        r2 = sh([ctx, "install", "claude"], env=env, cwd=self.work, check=False)
        (gt / "install.log").write_text((r1.stdout or "").replace(token, "<token>") + "\n---\n" + (r2.stdout or ""))
        if r1.returncode != 0 or r2.returncode != 0:
            raise RuntimeError("ghosttree setup/install failed, see gt/install.log")

    def start_gt_server(self):
        gt = self.root / "gt"
        port = self.meta["gt_port"]
        logf = open(gt / "serve.log", "a")
        self.gt_proc = subprocess.Popen([str(SHARED / "ctx-bin" / "ctx"), "serve", "--db", str(gt / "ghosttree.db"),
                                         "--listen", f"127.0.0.1:{port}"], env=self.gt_env(), stdout=logf,
                                        stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL, start_new_session=True)
        for _ in range(100):
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=0.5):
                    return
            except OSError:
                time.sleep(0.2)
        raise RuntimeError("ctx serve did not come up")

    def stop_gt_server(self):
        if self.gt_proc and self.gt_proc.poll() is None:
            os.killpg(self.gt_proc.pid, signal.SIGTERM)
            try:
                self.gt_proc.wait(20)
            except subprocess.TimeoutExpired:
                os.killpg(self.gt_proc.pid, signal.SIGKILL)
        self.gt_proc = None

    def stop_chain_procs(self):
        """Beendet alle Prozesse, deren Umgebung HOME=<dieses HOME> trägt (Worker, Chroma, Watcher)."""
        needle = ("HOME=" + str(self.home)).encode() + b"\0"
        me = os.getpid()
        victims = []
        for p in Path("/proc").iterdir():
            if not p.name.isdigit() or int(p.name) == me:
                continue
            try:
                if needle in (p / "environ").read_bytes():
                    victims.append(int(p.name))
            except OSError:
                pass
        for sig in (signal.SIGTERM, signal.SIGKILL):
            for pid in victims:
                try:
                    os.kill(pid, sig)
                except OSError:
                    pass
            time.sleep(2 if sig == signal.SIGTERM else 0.5)
        return victims

    # ---------- Session ----------
    def run_claude(self, prompt, tag, resume=None):
        cmd = [CLAUPHIL, "--model", MODEL, "-p", prompt, "--output-format", "json",
               "--dangerously-skip-permissions", "--max-turns", MAX_TURNS, "--disallowedTools", "Agent"]
        if resume:
            cmd += ["--resume", resume]
        t0 = time.time()
        timed_out = False
        try:
            r = subprocess.run(cmd, env=self.env(), cwd=self.work, text=True, stdin=subprocess.DEVNULL,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=SESSION_TIMEOUT)
            stdout, stderr, rc = r.stdout, r.stderr, r.returncode
        except subprocess.TimeoutExpired as ex:
            stdout = ex.stdout.decode() if isinstance(ex.stdout, bytes) else (ex.stdout or "")
            stderr, rc, timed_out = "timeout", -9, True
        wall = time.time() - t0
        (self.out / f"{tag}.stdout.json").write_text(stdout or "")
        (self.out / f"{tag}.stderr.txt").write_text(stderr or "")
        try:
            res = json.loads(stdout)
        except Exception:
            res = {}
        rec = {"tag": tag, "rc": rc, "t_start": t0, "wall_s": round(wall, 1), "timed_out": timed_out,
               "session_id": res.get("session_id"), "is_error": res.get("is_error"),
               "subtype": res.get("subtype"), "terminal_reason": res.get("terminal_reason"),
               "api_error_status": res.get("api_error_status"), "num_turns": res.get("num_turns"),
               "cost_usd": res.get("total_cost_usd"), "usage": res.get("usage"),
               "modelUsage": res.get("modelUsage"), "result_head": (res.get("result") or "")[:400]}
        rec["infra_failure"] = self.is_infra_failure(rec, stdout, stderr)
        rec["model_check"] = model_check(res.get("modelUsage") or {})
        return rec

    @staticmethod
    def is_infra_failure(rec, stdout, stderr):
        if rec["timed_out"]:
            return False  # Zeitüberschreitung zählt als inhaltliches Scheitern (präregistriert)
        if rec["subtype"] == "error_max_turns":
            return False
        if not stdout.strip():
            return True
        status = rec.get("api_error_status")
        if isinstance(status, int) and (status >= 500 or status == 429):
            return True
        if rec["is_error"] and (rec["terminal_reason"] == "api_error" or INFRA_PAT.search(rec["result_head"] or "")):
            return True
        return False

    def post_session(self, tag):
        info = {}
        if self.arm == "claudemem":
            info["cmem_drain"] = self.wait_cmem_drain()
            logs = self.home / ".claude-mem" / "logs"
            if logs.exists():
                shutil.copytree(logs, self.out / f"{tag}.cmem-logs", dirs_exist_ok=True)
        if self.arm == "ghosttree":
            r = sh([str(SHARED / "ctx-bin" / "ctx"), "watch", "--once"], env=self.env(), cwd=self.work, check=False, timeout=600)
            info["gt_watch_rc"] = r.returncode
            (self.out / f"{tag}.gt-watch.log").write_text(r.stdout or "")
        removed = []
        if self.arm != "native":
            for p in self.work.rglob("*"):
                if ".git" in p.parts:
                    continue
                if p.name in MEMFILE_NAMES and p.is_file():
                    txt = p.read_text(errors="replace")
                    if self.arm == "claudemem" and "claude-mem-context" in txt:
                        continue
                    removed.append(str(p.relative_to(self.work)))
                    (self.out / f"{tag}.removed-{p.name}").write_text(txt)
                    p.unlink()
            # Agenten legen auf Bitte selbst Dateien im Auto-Memory-Pfad oder in der
            # Nutzer-CLAUDE.md an. Das ist der Mechanismus von Arm native, nicht dieses Arms.
            for memdir in self.home.glob(".claude/projects/*/memory"):
                files = [p for p in memdir.rglob("*") if p.is_file()]
                if files:
                    keep = self.out / f"{tag}.removed-automemory"
                    keep.mkdir(exist_ok=True)
                    for p in files:
                        shutil.copy(p, keep / p.name)
                    removed.append(f"~/{memdir.relative_to(self.home)}/ ({len(files)} Dateien)")
                shutil.rmtree(memdir)
            user_md = self.home / ".claude" / "CLAUDE.md"
            base = self.meta.get("baseline_user_claude_md")
            cur = user_md.read_text() if user_md.exists() else None
            if cur != base:
                (self.out / f"{tag}.changed-user-CLAUDE.md").write_text(cur or "<gelöscht>")
                removed.append("~/.claude/CLAUDE.md (auf Installationsstand zurückgesetzt)")
                if base is None:
                    user_md.unlink()
                else:
                    user_md.write_text(base)
        info["removed_memfiles"] = removed
        env = self.env()
        sh(["git", "add", "-A"], env=env, cwd=self.work)
        sh(["git", "-c", "user.name=Dev", "-c", "user.email=dev@acme-billing.example", "commit", "-q",
            "--allow-empty", "-m", f"Arbeitsstand nach {tag.split('.')[0]}"], env=env, cwd=self.work)
        info["head"] = sh(["git", "rev-parse", "HEAD"], env=env, cwd=self.work).stdout.strip()
        return info

    def wait_cmem_drain(self, timeout=900):
        port = self.meta["cmem_port"]
        t0 = time.time()
        last, idle_since = None, None
        while time.time() - t0 < timeout:
            try:
                with urllib.request.urlopen(f"http://127.0.0.1:{port}/api/processing-status", timeout=5) as r:
                    st = json.loads(r.read())
            except Exception as ex:
                st = {"error": str(ex)}
            last = st
            busy = bool(st.get("isProcessing")) or int(st.get("queueDepth") or 0) > 0
            if "error" in st:
                busy = False
            if not busy:
                idle_since = idle_since or time.time()
                if time.time() - idle_since > 15:
                    break
            else:
                idle_since = None
            time.sleep(3)
        return {"waited_s": round(time.time() - t0, 1), "last": last}

    # ---------- Schnappschüsse ----------
    def snapshot(self, name):
        if self.arm == "ghosttree":
            self.stop_gt_server()
        self.stop_chain_procs()
        snap = self.root / "snapshots"
        snap.mkdir(exist_ok=True)
        with tarfile.open(snap / f"{name}.tar", "w") as t:
            t.add(self.home, arcname="home")
            if self.arm == "ghosttree":
                t.add(self.root / "gt", arcname="gt")
        if self.arm == "ghosttree":
            self.start_gt_server()

    def restore(self, name):
        if self.arm == "ghosttree":
            self.stop_gt_server()
        self.stop_chain_procs()
        for attempt in range(6):
            try:
                shutil.rmtree(self.home)
                break
            except OSError:
                # Nachzügler (Worker, Chroma) schreiben noch: erneut beenden, kurz warten
                if attempt == 5:
                    raise
                self.stop_chain_procs()
                time.sleep(2)
        if self.arm == "ghosttree":
            shutil.rmtree(self.root / "gt")
        with tarfile.open(self.root / "snapshots" / f"{name}.tar") as t:
            t.extractall(self.root, filter="tar")
        if self.arm == "ghosttree":
            self.start_gt_server()

    # ---------- Ablauf ----------
    def learning(self):
        for L in PROMPTS["learning"]:
            for attempt in range(3):
                self.snapshot(f"pre-{L['id']}")
                recs = []
                sid = None
                infra = False
                for i, turn in enumerate(L["turns"]):
                    tag = f"{L['id']}.a{attempt}.t{i + 1}"
                    rec = self.run_claude(turn, tag, resume=sid)
                    recs.append(rec)
                    sid = rec["session_id"] or sid
                    log(self.cid, f"{tag} rc={rec['rc']} turns={rec['num_turns']} cost={rec['cost_usd']} infra={rec['infra_failure']} wall={rec['wall_s']}")
                    if rec["infra_failure"]:
                        infra = True
                        break
                if infra and attempt < 2:
                    self.restore(f"pre-{L['id']}")
                    time.sleep(30)
                    continue
                info = self.post_session(f"{L['id']}.a{attempt}")
                self.write_record({"kind": "learning", "task": L["id"], "attempt": attempt, "invocations": recs,
                                   "infra_failure": infra, **info})
                break
        self.snapshot("post-learning")
        self.meta["post_learning_head"] = sh(["git", "rev-parse", "HEAD"], env=self.env(), cwd=self.work).stdout.strip()
        self.meta["state_evidence"] = self.state_evidence()
        self.save_meta()

    def state_evidence(self):
        """Weist nach, dass der Gedächtniszustand des Arms existiert (Falle #1634)."""
        ev = {}
        mem = list(self.home.glob(".claude/projects/*/memory/*.md"))
        ev["auto_memory_files"] = [str(p.relative_to(self.home)) for p in mem]
        ev["repo_memfiles"] = [str(p.relative_to(self.work)) for p in self.work.rglob("*")
                               if p.name in MEMFILE_NAMES and ".git" not in p.parts]
        if self.arm == "claudemem":
            db = self.home / ".claude-mem" / "claude-mem.db"
            ev["cmem_db_bytes"] = db.stat().st_size if db.exists() else None
            ev["cmem_counts"] = sqlite_counts(db, ["observations", "session_summaries", "sdk_sessions", "user_prompts"])
        if self.arm == "ghosttree":
            db = self.root / "gt" / "ghosttree.db"
            ev["gt_db_bytes"] = db.stat().st_size if db.exists() else None
            ev["gt_counts"] = sqlite_counts(db, None)
        return ev

    def followups(self, only=None):
        head = self.meta["post_learning_head"]
        for F in PROMPTS["followups"]:
            if only and F["id"] not in only:
                continue
            for attempt in range(3):
                self.restore("post-learning")
                tag = f"{F['id']}.a{attempt}"
                rec = self.run_claude(F["prompt"], tag)
                log(self.cid, f"{tag} rc={rec['rc']} turns={rec['num_turns']} cost={rec['cost_usd']} infra={rec['infra_failure']} wall={rec['wall_s']}")
                if rec["infra_failure"] and attempt < 2:
                    time.sleep(30)
                    continue
                info = self.post_session(tag)
                patch = sh(["git", "diff", "--binary", head, "HEAD"], env=self.env(), cwd=self.work, check=False)
                (self.out / f"{tag}.patch").write_text(patch.stdout or "")
                grade = grade_task(F["id"], self.work, head, self.env())
                tx = transcript_stats(self.home, rec.get("session_id"), F["id"])
                self.write_record({"kind": "followup", "task": F["id"], "attempt": attempt, "invocations": [rec],
                                   "infra_failure": rec["infra_failure"], "grade": grade, "transcript": tx, **info})
                log(self.cid, f"{tag} graded func={grade['func']} know={grade['know']} success={grade['success']}")
                break
        self.restore("post-learning")
        self.stop_gt_server()
        self.stop_chain_procs()

    def write_record(self, rec):
        rec.update({"arm": self.arm, "rep": self.rep, "cid": self.cid, "ts": time.time()})
        files, violations = [], []
        for inv in rec.get("invocations") or []:
            for f in instruction_files(self.home, inv.get("session_id")):
                if f not in files:
                    files.append(f)
        for f in files:
            if f.startswith(str(self.work) + "/") or f == str(self.home / ".claude" / "CLAUDE.md"):
                continue
            if self.arm == "native" and f.startswith(str(self.home / ".claude" / "projects") + "/"):
                continue  # Auto-Memory (MEMORY.md) ist der Mechanismus dieses Arms
            violations.append(f)
        rec["instruction_files"] = files
        rec["isolation_violations"] = violations
        if violations:
            log(self.cid, f"ISOLATION VIOLATION {rec.get('task')}: {violations}")
        if rec.get("invocations"):
            sid = rec["invocations"][-1].get("session_id")
            if sid:
                for p in self.home.glob(f".claude/projects/*/{sid}.jsonl"):
                    shutil.copy(p, self.out / f"{rec['task']}.a{rec['attempt']}.transcript.jsonl")
        with open(self.out / "records.jsonl", "a") as f:
            f.write(json.dumps(rec) + "\n")


def sqlite_counts(db, tables):
    import sqlite3
    if not Path(db).exists():
        return None
    try:
        con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
        names = [r[0] for r in con.execute("select name from sqlite_master where type='table'")]
        out = {}
        for t in (tables or names):
            if t in names and not t.endswith(("_fts", "_data", "_idx", "_content", "_docsize", "_config")):
                try:
                    out[t] = con.execute(f'select count(*) from "{t}"').fetchone()[0]
                except Exception:
                    pass
        con.close()
        return {k: v for k, v in out.items() if v}
    except Exception as ex:
        return {"error": str(ex)}


def model_check(mu):
    """Präregistrierte Aliasregel: Hauptmodell = Modell mit den meisten Ausgabetokens;
    sein canonicalModel muss mit claude-opus-5-5 beginnen. Andere Modelle nur aus der Helferliste."""
    if not mu:
        return {"ok": False, "reason": "no modelUsage"}
    canon = {k: (v.get("canonicalModel") or k) for k, v in mu.items()}
    main = max(mu, key=lambda k: mu[k].get("outputTokens", 0))
    if not canon[main].startswith("claude-opus-5-5"):
        return {"ok": False, "reason": f"main model {main} -> {canon[main]}", "models": canon}
    bad = [c for k, c in canon.items() if k != main and not c.startswith("claude-opus-5-5") and not ALLOWED_HELPER.match(c)]
    if bad:
        return {"ok": False, "reason": f"unexpected helper {bad}", "models": canon}
    return {"ok": True, "main": canon[main], "models": canon}


# ---------- Bewertung ----------
SQL_STMT_SPLIT = re.compile(r";\s*")


def hidden_test(task):
    p = P["hidden"] / f"{task.lower()}_test.go"
    return p if p.exists() else p.with_name(p.name + ".txt")


def grade_task(task, work, snapshot_sha, env):
    tmp = Path(env["TMPDIR"]) / f"grade-{task}-{secrets.token_hex(3)}"
    shutil.copytree(work, tmp, ignore=shutil.ignore_patterns(".git"))
    out = {"task": task, "checks": {}}
    try:
        # Repo-eigene Tests (ohne verdeckte)
        r = sh(["go", "test", "-count=1"] + P["repo_tests"], env=env, cwd=tmp, check=False, timeout=900)
        out["checks"]["repo_tests"] = r.returncode == 0
        out["repo_tests_tail"] = (r.stdout or "")[-1500:]
        hid = tmp / "zzhidden"
        hid.mkdir()
        shutil.copy(hidden_test(task), hid / f"{task.lower()}_test.go")
        r = sh(["go", "test", "-count=1", "-json", "./zzhidden/"], env=env, cwd=tmp, check=False, timeout=600)
        tests = {}
        build_out = []
        for line in (r.stdout or "").splitlines():
            try:
                ev = json.loads(line)
            except Exception:
                build_out.append(line)
                continue
            if ev.get("Test") and ev.get("Action") in ("pass", "fail") and "/" not in ev["Test"]:
                tests[ev["Test"]] = ev["Action"] == "pass"
            if ev.get("Action") == "output" and not ev.get("Test"):
                build_out.append(ev.get("Output", ""))
        src = hidden_test(task).read_text()
        declared = re.findall(r"^func (Test\w+)\(", src, re.M)
        for name in declared:
            out["checks"][name] = tests.get(name, False)  # Kompilierfehler => alle falsch
        out["hidden_output_tail"] = "".join(build_out)[-1500:]
        func_ok = out["checks"]["repo_tests"] and all(v for k, v in out["checks"].items() if k.startswith("TestFunc_"))
        know_checks = {k: v for k, v in out["checks"].items() if k.startswith("TestKnow_")}
        if PROJECT == "ledgerly" and task == "F5":
            mc = migration_checks(work, snapshot_sha, env)
            out["migration"] = mc
            func_ok = func_ok and mc["new_vat_migration"]
            know_checks.update({"existing_unchanged": mc["existing_unchanged"], "no_not_null_without_default": mc["no_not_null_without_default"],
                                "down_present": mc["down_present"]})
        if PROJECT == "ledgerly" and task == "F6":
            gomod = (work / "go.mod").read_text()
            gosum = work / "go.sum"
            know_checks["stdlib_only"] = "require" not in gomod and (not gosum.exists() or not gosum.read_text().strip())
        out["know_checks"] = know_checks
        out["func"] = bool(func_ok)
        out["know"] = bool(all(know_checks.values())) if know_checks else None
        out["success"] = out["func"] and (out["know"] is None or out["know"])
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    return out


def migration_checks(work, snap, env):
    r = sh(["git", "diff", "--name-status", snap, "HEAD", "--", "migrations/"], env=env, cwd=work, check=False)
    changed_existing, added = [], []
    for line in (r.stdout or "").splitlines():
        parts = line.split("\t")
        st, path = parts[0], parts[-1]
        if st.startswith("A"):
            added.append(path)
        else:
            changed_existing.append(line)
    ups = [p for p in added if p.endswith(".sql") and not p.endswith(".down.sql")]
    vat_ups = [p for p in ups if "vat_id" in (work / p).read_text().lower()]
    violations = []
    for p in vat_ups:
        for stmt in SQL_STMT_SPLIT.split(re.sub(r"--[^\n]*", "", (work / p).read_text())):
            if "vat_id" in stmt.lower() and re.search(r"not\s+null", stmt, re.I) and not re.search(r"\bdefault\b", stmt, re.I):
                violations.append(stmt.strip()[:200])
    downs = [p[:-4] + ".down.sql" for p in vat_ups]
    return {"added": added, "changed_existing": changed_existing, "new_vat_migration": bool(vat_ups),
            "existing_unchanged": not changed_existing, "not_null_violations": violations,
            "no_not_null_without_default": bool(vat_ups) and not violations,
            "down_present": bool(vat_ups) and all((work / d).exists() for d in downs)}


KEYWORDS = {
    "F1": [r"RoundBankers", r"half[- ]to[- ]even", r"half[- ]even", r"Bankers"],
    "F2": [r"Europe/Berlin", r"\bBerlin\b"],
    "F3": [r"balance[^\n]{0,120}(cach|Cach)", r"(cach|Cach)[^\n]{0,120}balance", r"doppelt gezahlt"],
    "F4": [r"PublicID", r"öffentliche[n]? ID", r"public ID", r"Enumeration"],
    "F5": [r"NOT NULL[^\n]{0,80}DEFAULT", r"\.down\.sql", r"rollierend", r"rolling"],
    "F6": [r"X-Ledgerly-", r"Standardbibliothek", r"stdlib|standard library"],
    "F0": [],
    "NF1": [r"LC_ALL=C", r"PACK-FORMAT"],
    "NF2": [r"LC_ALL=C", r"PACK-FORMAT"],
}


def instruction_files(home, sid):
    """Pfade aller Instruktionsdateien (CLAUDE.md usw.), die Claude Code in die Session geladen hat."""
    out = []
    if not sid:
        return out
    for fn in home.glob(f".claude/projects/*/{sid}.jsonl"):
        for line in fn.read_text(errors="replace").splitlines():
            if '"instructions"' not in line:
                continue
            try:
                ev = json.loads(line)
            except Exception:
                continue
            a = ev.get("attachment") or {}
            if a.get("type") == "instructions":
                for f in a.get("files") or []:
                    if f.get("path") and f["path"] not in out:
                        out.append(f["path"])
    return out


def transcript_stats(home, sid, task):
    """Werkzeugaufrufe zählen und prüfen, ob das Projektwissen im Kontext ankam,
    getrennt nach Quelle: injiziert (Hooks/Systemanhänge), aus Werkzeugergebnissen
    (z. B. Gedächtnis-Suche oder Code), oder gar nicht."""
    st = {"tool_calls": 0, "tools": {}, "kw_injected": False, "kw_tool_result": False, "kw_memory_tool": False,
          "memory_tool_calls": 0, "injected_chars": 0}
    if not sid:
        return st
    files = list(home.glob(f".claude/projects/*/{sid}.jsonl"))
    if not files:
        return st
    pats = [re.compile(p) for p in KEYWORDS.get(task, [])]
    memtool = re.compile(r"^mcp__(ghosttree|plugin_claude-mem|claude-mem|mcp-search)|mem-search|^mcp__.*(search|observations|timeline)", re.I)
    # Gedächtniszugriff ohne MCP: ghosttree-Spiegel, Auto-Memory-Pfad, claude-mem-Daten
    memread = re.compile(r"\.ghosttree/|/\.claude/projects/[^\"]*/memory|\.claude-mem|MEMORY\.md")
    tool_names = {}
    for line in files[0].read_text(errors="replace").splitlines():
        try:
            ev = json.loads(line)
        except Exception:
            continue
        typ = ev.get("type")
        msg = ev.get("message") or {}
        content = msg.get("content")
        if typ == "assistant" and isinstance(content, list):
            for b in content:
                if b.get("type") == "tool_use":
                    st["tool_calls"] += 1
                    n = b.get("name", "")
                    st["tools"][n] = st["tools"].get(n, 0) + 1
                    inp = json.dumps(b.get("input"), ensure_ascii=False)
                    is_mem = bool(memtool.search(n)) or bool(memread.search(inp))
                    tool_names[b.get("id")] = "MEM" if is_mem else n
                    if is_mem:
                        st["memory_tool_calls"] += 1
        elif typ == "user" and isinstance(content, list):
            for b in content:
                if b.get("type") == "tool_result":
                    txt = json.dumps(b.get("content"), ensure_ascii=False)
                    hit = any(p.search(txt) for p in pats)
                    if hit:
                        st["kw_tool_result"] = True
                        if tool_names.get(b.get("tool_use_id")) == "MEM":
                            st["kw_memory_tool"] = True
        elif typ == "attachment":
            a = ev.get("attachment") or {}
            # Nur echte Einspeisungen: geladene Instruktionsdateien (CLAUDE.md, Auto-Memory)
            # und Kontext aus Hooks. Nicht: Echos eigener Edits oder rohe Hook-Ausgaben.
            if a.get("type") in ("instructions", "hook_additional_context", "hook_system_message"):
                txt = json.dumps(a, ensure_ascii=False)
                st["injected_chars"] += len(txt)
                if any(p.search(txt) for p in pats):
                    st["kw_injected"] = True
    return st


def regrade(cid):
    """Bewertet die Folgesessions einer Kette neu: Schnappschuss entpacken, Patch anwenden, prüfen."""
    out = RESULTS / cid
    meta = json.loads((out / "chain.json").read_text())
    head = meta["post_learning_head"]
    tarp = RUNS / cid / "snapshots" / "post-learning.tar"
    recs = [json.loads(l) for l in (out / "records.jsonl").read_text().splitlines()]
    res = []
    for r in recs:
        if r["kind"] != "followup":
            continue
        tmp = Path(f"/tmp/rg-{secrets.token_hex(3)}")
        tmp.mkdir()
        try:
            with tarfile.open(tarp) as t:
                t.extractall(tmp, filter="tar")
            work = tmp / "home" / "src" / P["dir"]
            env = dict(os.environ, HOME=str(tmp / "home"), TMPDIR=str(tmp), GOTOOLCHAIN="local", GOCACHE=str(SHARED / "gocache"),
                       GOMODCACHE=str(SHARED / "gomod"), GIT_CONFIG_NOSYSTEM="1")
            patch = out / f"{r['task']}.a{r['attempt']}.patch"
            if patch.exists() and patch.read_text().strip():
                sh(["git", "update-index", "-q", "--refresh"], env=env, cwd=work, check=False)
                sh(["git", "apply", "--binary", "--index", str(patch)], env=env, cwd=work)
                sh(["git", "-c", "user.name=Dev", "-c", "user.email=dev@x", "commit", "-qm", "regrade"], env=env, cwd=work)
            g = grade_task(r["task"], work, head, env)
            res.append({"task": r["task"], "attempt": r["attempt"], "old": {k: r["grade"][k] for k in ("func", "know", "success")},
                        "new": {k: g[k] for k in ("func", "know", "success")}, "grade": g})
        finally:
            shutil.rmtree(tmp, ignore_errors=True)
    (out / "regraded.jsonl").write_text("".join(json.dumps(x) + "\n" for x in res))
    for x in res:
        print(cid, x["task"], x["old"], "->", x["new"])


def main():
    args = sys.argv[1:]
    if args and args[0] == "chain":
        arm, rep = args[1], int(args[2])
        only = None
        if "--tasks" in args:
            only = set(args[args.index("--tasks") + 1].split(","))
        c = Chain(arm, rep)
        RESULTS.mkdir(parents=True, exist_ok=True)
        try:
            c.prepare()
            c.learning()
            if "--only-learning" not in args:
                c.followups(only)
            log(c.cid, "chain done")
        except Exception as ex:
            log(c.cid, f"CHAIN ERROR {ex!r}")
            c.meta["error"] = repr(ex)
            c.save_meta()
            raise
        finally:
            c.stop_gt_server()
            c.stop_chain_procs()
            c.save_meta()
            shutil.copy(c.meta_path, c.out / "chain.json")
    elif args and args[0] == "regrade":
        for cid in args[1:]:
            regrade(cid)
    elif args and args[0] == "grade":
        env = dict(os.environ, TMPDIR="/tmp", GOTOOLCHAIN="local")
        print(json.dumps(grade_task(args[1], Path(args[2]), args[3], env), indent=2))
    else:
        print(__doc__)
        sys.exit(2)


if __name__ == "__main__":
    main()
