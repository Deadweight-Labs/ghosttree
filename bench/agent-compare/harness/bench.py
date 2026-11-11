#!/usr/bin/env python3
"""Mehrsession-Vergleich von Gedächtnis-Mechanismen für Claude Code.

Eine Kette (chain) = ein Arm x eine Wiederholung: frisches HOME, frisches
Projekt, frischer Gedächtniszustand. Ablauf: Lern-Sessions L1..L3 (je zwei
Züge, zweiter per --resume), Schnappschuss, dann jede Folgeaufgabe F0..F6 als
frische Session auf dem wiederhergestellten Schnappschuss, verdeckt bewertet.

Jede Agentensitzung läuft in einer Sandbox ohne Netz (sandbox.py): Container (Standard) oder
bubblewrap, gewählt mit BENCH_SANDBOX. Der Modellzugang geht nur über den Proxy auf einem
Unix-Socket (modelproxy.py). Vor und nach jeder Sitzung steht ein Isolationsprotokoll
(<tag>.isolation.json); besteht die Probe nicht, startet kein Agent. Alles, was Dateien aus dem
Agenten-HOME ausführt (git, ctx, go test), läuft ebenfalls in der Sandbox.

Aufruf:
  bench.py chain <arm> <rep> [--only-learning] [--tasks F1,F2]
  bench.py probe [--keep]            Isolationsprobe ohne Modellaufruf, schreibt isolation.json
  bench.py cleanup                   entfernt Sandbox-Container nach Label
  bench.py grade <taskid> <workdir> <snapshot_sha>
  bench.py regrade <kette>...
"""
import json
import os
import re
import secrets
import shutil
import signal
import socket
import stat
import subprocess
import sys
import tarfile
import time
import urllib.request
from pathlib import Path

HARNESS = Path(__file__).resolve().parent
sys.path.insert(0, str(HARNESS))
import sandbox as sbx  # noqa: E402
from modelproxy import ModelProxy  # noqa: E402

BASE = Path(os.environ.get("BENCH_BASE", Path.home() / "agentbench-data" / "agent-compare"))
HIDDEN = Path(os.environ.get("BENCH_HIDDEN", HARNESS.parent / "hidden"))
TEMPLATE = HARNESS.parent / "projects" / "ledgerly"
SHARED = BASE / "shared"
# Außerhalb des echten Benutzer-HOME: Die Sandbox sieht es ohnehin nicht, aber so bleibt auch der
# Host-Pfad der Läufe frei von persönlichen Dateien.
RUNS = Path(os.environ.get("BENCH_RUNS", "/var/tmp/agentcmp/runs"))
RESULTS = BASE / "results"
# Agent-Aufruf IN der Sandbox: claude aus dem Image (bzw. bei bwrap vom Host).
AGENT = os.environ.get("BENCH_AGENT", "claude")
SANDBOX_KIND = os.environ.get("BENCH_SANDBOX", "container")
MODEL = "claude-opus-5-5"
MAX_TURNS = "80"
SESSION_TIMEOUT = 1800
REMOTE = "https://github.com/acme-billing/ledgerly.git"
ARMS = ("vanilla", "native", "claudemem", "ghosttree")
SANDBOX_ARMS = ("vanilla", "native", "ghosttree")
MEMFILE_NAMES = {"CLAUDE.md", "CLAUDE.local.md", "AGENTS.md"}
ALLOWED_HELPER = re.compile(r"^claude-(haiku-4-5|haiku-5-5|sonnet-5-5)")
INFRA_PAT = re.compile(r"API Error|overloaded|ECONNREFUSED|ECONNRESET|Connection error|socket hang up|"
                       r"fetch failed|502|503|504|529|Request timed out|rate limit", re.I)
# git auf Repos, die der Agent beschrieben hat, läuft nur in der Sandbox und ohne Hooks des Repos.
GIT = ["git", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "safe.directory=*"]
GIT_ID = ["-c", "user.name=Dev", "-c", "user.email=dev@acme-billing.example"]
CTX_BIN_DIR = "/opt/bench/ctx-bin"
GOMOD_DIR = "/opt/bench/gomod"
GOCACHE_DIR = "/var/cache/gocache"

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
PROMPTS = json.loads(P["prompts"].read_text()) if P["prompts"].exists() else {}
RESULTS = P["results"]


def log(chain, msg):
    line = f"{time.strftime('%H:%M:%S')} [{chain}] {msg}"
    print(line, flush=True)
    BASE.mkdir(parents=True, exist_ok=True)
    with open(BASE / "bench.log", "a") as f:
        f.write(line + "\n")


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def sh(cmd, env=None, cwd=None, check=True, timeout=None, capture=True):
    """Führt einen vertrauenswürdigen Host-Befehl aus (kein Agenten-Code, keine Agenten-Dateien)."""
    r = subprocess.run(cmd, env=env, cwd=cwd, timeout=timeout, text=True,
                       stdout=subprocess.PIPE if capture else None,
                       stderr=subprocess.STDOUT if capture else None, stdin=subprocess.DEVNULL)
    if check and r.returncode != 0:
        raise RuntimeError(f"{cmd!r} -> {r.returncode}: {(r.stdout or '')[-2000:]}")
    return r


# ---------- Dateien aus dem Agenten-HOME lesen (R9) ----------
def safe_file(path, root):
    """Pfad einer regulären Datei unterhalb von root, sonst None. Der Harness folgt keinem Symlink aus der Sandbox."""
    root_real = os.path.realpath(root)
    rel = os.path.relpath(os.path.abspath(path), os.path.abspath(root))
    if rel.startswith(".."):
        return None
    expected = os.path.join(root_real, rel)
    try:
        if os.path.realpath(expected) != expected or not stat.S_ISREG(os.lstat(expected).st_mode):
            return None
    except OSError:
        return None
    return Path(expected)


def safe_text(path, root, default=""):
    p = safe_file(path, root)
    return p.read_text(errors="replace") if p else default


def safe_glob(root, pattern):
    return [p for p in sorted(Path(root).glob(pattern)) if safe_file(p, root)]


def safe_walk(root, skip=(".git",)):
    """Reguläre Dateien unter root und die Namen von Symlinks, ohne Symlinks zu folgen. Liefert (Pfad, ist_symlink)."""
    for dirpath, dirnames, filenames in os.walk(root, followlinks=False):
        dirnames[:] = [d for d in dirnames if d not in skip]
        for n in dirnames + filenames:
            p = Path(dirpath, n)
            if p.is_symlink():
                yield p, True
            elif n in filenames and safe_file(p, root):
                yield p, False


def rm_tree(path):
    path = Path(path)
    if path.is_symlink():
        path.unlink()
    elif path.exists():
        shutil.rmtree(path)


class Chain:
    def __init__(self, arm, rep, cid=None):
        assert arm in ARMS, arm
        self.arm, self.rep = arm, rep
        self.cid = cid or secrets.token_hex(3)
        self.root = RUNS / self.cid
        self.home = self.root / "home"
        self.work = self.home / "src" / P["dir"]
        self.iwork = f"{sbx.SANDBOX_HOME}/src/{P['dir']}"  # derselbe Ort aus Sicht der Sandbox
        self.out = RESULTS / self.cid
        self.sock = self.root / "sock"
        self.meta_path = self.root / "chain.json"
        self.sandbox_kind = SANDBOX_KIND
        self.token = secrets.token_hex(16)  # Laufkennzeichen für den Proxy, kein API-Schlüssel
        self.proxy = None
        self.gt_fwd = None
        self.seq = 0
        self.meta = {"arm": arm, "rep": rep, "cid": self.cid, "project": PROJECT, "sandbox": self.sandbox_kind}
        self.gt_proc = None

    # ---------- Sandbox ----------
    def env(self):
        """Umgebung des Agenten in der Sandbox. Nichts wird vom Harness geerbt; jeder Name steht in sandbox.ALLOWED_ENV_NAMES."""
        bindirs = [f"{sbx.SANDBOX_HOME}/.local/bin", "/opt/bench/bin"]
        if self.arm == "ghosttree":
            bindirs.append(CTX_BIN_DIR)
        e = {
            "PATH": ":".join(bindirs + ["/usr/local/go/bin", "/usr/local/bin", "/usr/bin", "/bin"]),
            "HOME": sbx.SANDBOX_HOME,
            "USER": "agent", "LOGNAME": "agent",
            "LANG": "C.UTF-8", "TERM": "dumb", "SHELL": "/bin/bash",
            "TMPDIR": "/tmp",
            "GOTOOLCHAIN": "local",
            "GOCACHE": GOCACHE_DIR,
            "GOPROXY": "off",
            "GIT_CONFIG_NOSYSTEM": "1",
            "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
            "DISABLE_AUTOUPDATER": "1",
            "ANTHROPIC_BASE_URL": f"http://127.0.0.1:{sbx.PROXY_PORT}",
            sbx.TOKEN_ENV: self.token,
        }
        if (SHARED / "gomod").is_dir():
            e["GOMODCACHE"] = GOMOD_DIR
        if self.arm != "native":
            e["CLAUDE_CODE_DISABLE_AUTO_MEMORY"] = "1"
        return e

    def host_env(self):
        """Umgebung für vertrauenswürdige Prozesse auf dem Host (ctx serve): knapp und ohne geerbte Zugangsdaten."""
        return {"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": str(self.root / "gt-server-home"), "LANG": "C.UTF-8",
                "TMPDIR": str(self.root / "tmp"), "GIT_CONFIG_NOSYSTEM": "1"}

    def mounts(self):
        ms = [sbx.Mount(str(self.home), sbx.SANDBOX_HOME, True), sbx.Mount(str(self.sock), sbx.RUN_DIR),
              sbx.Mount(str(self.root / "gocache"), GOCACHE_DIR, True)]
        if (SHARED / "gomod").is_dir():
            ms.append(sbx.Mount(str(SHARED / "gomod"), GOMOD_DIR))
        if self.arm == "ghosttree":
            ms.append(sbx.Mount(str(SHARED / "ctx-bin"), CTX_BIN_DIR))
        return ms

    def new_sandbox(self, tag, extra_mounts=(), gocache=None, home=None, model=True, roots=()):
        """Beschreibt und erzeugt eine Sandbox (startet sie nicht). Ein unbekannter Weg wirft IsolationError.
        model=False (Bewertung): weder Proxy-Socket noch Laufkennzeichen, Agentencode erreicht das Modell nicht.
        home / gocache ersetzen HOME und Go-Cache der Kette (die Bewertung nimmt eigene, leere)."""
        self.seq += 1
        replaced = {sbx.SANDBOX_HOME: home, GOCACHE_DIR: gocache}
        mounts = [m for m in self.mounts() if replaced.get(m.dst) is None and (model or m.dst != sbx.RUN_DIR)]
        mounts += [sbx.Mount(str(src), dst, True) for dst, src in replaced.items() if src is not None]
        files = []
        if self.sandbox_kind == "bwrap":
            claude = shutil.which(AGENT)
            if not claude:
                raise RuntimeError(f"{AGENT} nicht im PATH: bwrap nutzt den claude des Hosts")
            real = os.path.realpath(claude)
            mounts.append(sbx.Mount(real, "/opt/bench/bin/claude"))
            files.append(real)
        env = self.env()
        forwards = [(sbx.PROXY_PORT, f"{sbx.RUN_DIR}/model.sock")]
        if self.arm == "ghosttree" and self.meta.get("gt_port"):
            forwards.append((self.meta["gt_port"], f"{sbx.RUN_DIR}/gt.sock"))
        if not model:
            forwards = []
            for name in ("ANTHROPIC_BASE_URL", sbx.TOKEN_ENV):
                env.pop(name)
        extra = [t.split(":") for t in os.environ.get("BENCH_PROBE_EXTRA_TARGETS", "").split(",") if ":" in t]
        spec = sbx.SandboxSpec(
            run_id=self.cid, mounts=mounts + list(extra_mounts), env=env, host_home=str(Path.home()),
            allowed_roots=[str(RUNS), str(SHARED), str(HIDDEN), *map(str, roots)], allowed_files=files, forwards=forwards,
            uid=os.getuid(), gid=os.getgid(), workdir=self.iwork, extra_probe_targets=[[h, int(p)] for h, p in extra])
        tagname = re.sub(r"[^A-Za-z0-9_.-]", "_", tag)
        return sbx.make_sandbox(self.sandbox_kind, spec, self.root / "in" / f"{self.seq:03d}-{tagname}")

    def tool(self, *cmds, cwd=None, check=True, timeout=900, tag="tool"):
        """Führt Befehle nacheinander in einer frischen Sandbox aus (git, ctx, Aufräumen)."""
        out = []
        with self.new_sandbox(tag) as box:
            for argv in cmds:
                r = box.exec(argv, cwd=cwd or self.iwork, timeout=timeout)
                if check and r.rc != 0:
                    raise RuntimeError(f"{argv!r} -> {r.rc}: {(r.stdout + r.stderr)[-2000:]}")
                out.append(r)
        return out

    def ensure_proxy(self):
        if self.proxy:
            return self.proxy
        self.sock.mkdir(parents=True, exist_ok=True)
        self.out.mkdir(parents=True, exist_ok=True)
        self.proxy = ModelProxy(self.sock / "model.sock", self.cid, self.token, usage_log=self.out / "proxy-usage.jsonl").start()
        return self.proxy

    def cleanup(self):
        """Sandbox-Container dieser Kette entfernen (nach Kennung), Proxy und Weiterleitungen beenden."""
        if self.sandbox_kind == "container":
            sbx.remove_containers(self.cid)
        self.stop_gt_forward()
        if self.proxy:
            self.proxy.stop()
            self.proxy = None

    def save_meta(self):
        self.meta_path.write_text(json.dumps(self.meta, indent=2))

    # ---------- Aufbau ----------
    def prepare(self):
        if self.arm not in SANDBOX_ARMS:
            raise sbx.IsolationError(f"Arm {self.arm} läuft nicht in der Sandbox (Installation und Worker brauchen Netz); "
                                     f"erlaubt: {', '.join(SANDBOX_ARMS)}")
        self.root.mkdir(parents=True, exist_ok=True)
        (self.root / "tmp").mkdir(exist_ok=True)
        (self.root / "gocache").mkdir(exist_ok=True)
        self.out.mkdir(parents=True, exist_ok=True)
        self.home.mkdir()
        (self.home / ".claude").mkdir()
        settings = {"autoMemoryEnabled": self.arm == "native"}
        (self.home / ".claude" / "settings.json").write_text(json.dumps(settings, indent=2))
        (self.home / ".gitconfig").write_text("[user]\n\tname = Dev\n\temail = dev@acme-billing.example\n"
                                              "[init]\n\tdefaultBranch = main\n")
        shutil.copytree(P["template"], self.work)
        self.ensure_proxy()
        self.tool(["sh", "-c", f"[ -d /opt/gowarm/cache ] && cp -a /opt/gowarm/cache/. {GOCACHE_DIR}/ || true"], tag="warm")
        self.tool(["git", "init", "-q"], ["git", "add", "-A"],
                  [*GIT, *GIT_ID, "commit", "-q", "-m", "ledgerly: Stand vor der Arbeit"],
                  ["git", "remote", "add", "origin", P["remote"]], tag="prepare")
        if self.arm == "ghosttree":
            self.install_ghosttree()
        user_md = self.home / ".claude" / "CLAUDE.md"
        self.meta["baseline_user_claude_md"] = safe_text(user_md, self.home, None) if user_md.exists() else None
        self.meta["baseline_settings"] = json.loads(safe_text(self.home / ".claude" / "settings.json", self.home))
        self.save_meta()
        log(self.cid, f"prepared arm={self.arm} rep={self.rep} sandbox={self.sandbox_kind}")

    def gt_env(self):
        return self.host_env()

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
        r1, r2 = self.tool([f"{CTX_BIN_DIR}/ctx", "setup", "--server", f"http://127.0.0.1:{port}", "--token", token],
                           [f"{CTX_BIN_DIR}/ctx", "install", "claude"], check=False, tag="gt-install")
        (gt / "install.log").write_text((r1.stdout + r1.stderr).replace(token, "<token>") + "\n---\n" + r2.stdout + r2.stderr)
        if r1.rc != 0 or r2.rc != 0:
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
                    break
            except OSError:
                time.sleep(0.2)
        else:
            raise RuntimeError("ctx serve did not come up")
        # Der Server ist für die Sandbox nur über diesen Socket erreichbar (socat in der Sandbox macht daraus 127.0.0.1).
        self.sock.mkdir(parents=True, exist_ok=True)
        self.gt_fwd = subprocess.Popen(["socat", f"UNIX-LISTEN:{self.sock}/gt.sock,fork,mode=600,unlink-early",
                                        f"TCP:127.0.0.1:{port}"], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                       stderr=subprocess.DEVNULL, start_new_session=True)

    def stop_gt_forward(self):
        if self.gt_fwd and self.gt_fwd.poll() is None:
            self.gt_fwd.terminate()
            try:
                self.gt_fwd.wait(10)
            except subprocess.TimeoutExpired:
                self.gt_fwd.kill()
        self.gt_fwd = None

    def stop_gt_server(self):
        self.stop_gt_forward()
        if self.gt_proc and self.gt_proc.poll() is None:
            os.killpg(self.gt_proc.pid, signal.SIGTERM)
            try:
                self.gt_proc.wait(20)
            except subprocess.TimeoutExpired:
                os.killpg(self.gt_proc.pid, signal.SIGKILL)
        self.gt_proc = None

    # ---------- Session ----------
    def run_claude(self, prompt, tag, resume=None):
        """Eine Agentensitzung. --dangerously-skip-permissions gibt es nur hier, und nur hinter der Sandbox:
        new_sandbox wirft IsolationError, bevor ein Prozess entsteht, und guarded prüft vor dem Agenten."""
        cmd = [AGENT, "--model", MODEL, "-p", prompt, "--output-format", "json",
               "--dangerously-skip-permissions", "--max-turns", MAX_TURNS, "--disallowedTools", "Agent"]
        if resume:
            cmd += ["--resume", resume]
        box = self.new_sandbox(tag)
        usage0 = self.proxy.summary() if self.proxy else {"requests": 0, "tokens": 0}
        t0 = time.time()
        ilog = None
        try:
            with box:
                res, ilog = box.guarded(cmd, self.out / f"{tag}.isolation.json", SESSION_TIMEOUT, cwd=self.iwork)
        except sbx.IsolationError as ex:
            log(self.cid, f"ISOLATION FAILED {tag}: {ex}")
            raise
        stdout, stderr, rc, timed_out = res.stdout, res.stderr, res.rc, res.timed_out
        wall = time.time() - t0
        (self.out / f"{tag}.stdout.json").write_text(stdout or "")
        (self.out / f"{tag}.stderr.txt").write_text(stderr or "")
        try:
            parsed = json.loads(stdout)
        except Exception:
            parsed = {}
        usage1 = self.proxy.summary() if self.proxy else usage0
        rec = {"tag": tag, "rc": rc, "t_start": t0, "wall_s": round(wall, 1), "timed_out": timed_out,
               "session_id": parsed.get("session_id"), "is_error": parsed.get("is_error"),
               "subtype": parsed.get("subtype"), "terminal_reason": parsed.get("terminal_reason"),
               "api_error_status": parsed.get("api_error_status"), "num_turns": parsed.get("num_turns"),
               "cost_usd": parsed.get("total_cost_usd"), "usage": parsed.get("usage"),
               "modelUsage": parsed.get("modelUsage"), "result_head": (parsed.get("result") or "")[:400],
               "isolation_sha256": ilog.sha256() if ilog else None,
               "proxy_usage": {k: usage1[k] - usage0[k] for k in usage1}}
        rec["infra_failure"] = self.is_infra_failure(rec, stdout, stderr)
        rec["model_check"] = model_check(parsed.get("modelUsage") or {})
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
        if self.arm == "ghosttree":
            r = self.tool([f"{CTX_BIN_DIR}/ctx", "watch", "--once"], check=False, timeout=600, tag="gt-watch")[0]
            info["gt_watch_rc"] = r.rc
            (self.out / f"{tag}.gt-watch.log").write_text(r.stdout + r.stderr)
        removed = []
        if self.arm != "native":
            for p, is_link in safe_walk(self.work):
                if p.name in MEMFILE_NAMES:
                    rel = str(p.relative_to(self.work))
                    if not is_link:
                        (self.out / f"{tag}.removed-{p.name}").write_text(safe_text(p, self.work))
                    removed.append(rel + (" (Symlink)" if is_link else ""))
                    p.unlink()
            # Agenten legen auf Bitte selbst Dateien im Auto-Memory-Pfad oder in der
            # Nutzer-CLAUDE.md an. Das ist der Mechanismus von Arm native, nicht dieses Arms.
            for memdir in self.home.glob(".claude/projects/*/memory"):
                files = [p for p, link in safe_walk(memdir, skip=()) if not link]
                if files:
                    keep = self.out / f"{tag}.removed-automemory"
                    keep.mkdir(exist_ok=True)
                    for p in files:
                        (keep / p.name).write_text(safe_text(p, memdir))
                    removed.append(f"~/{memdir.relative_to(self.home)}/ ({len(files)} Dateien)")
                rm_tree(memdir)
            user_md = self.home / ".claude" / "CLAUDE.md"
            base = self.meta.get("baseline_user_claude_md")
            cur = safe_text(user_md, self.home, None) if (user_md.exists() or user_md.is_symlink()) else None
            if cur != base:
                (self.out / f"{tag}.changed-user-CLAUDE.md").write_text(cur or "<gelöscht>")
                removed.append("~/.claude/CLAUDE.md (auf Installationsstand zurückgesetzt)")
                if user_md.is_symlink() or base is None:
                    user_md.unlink(missing_ok=True)
                if base is not None:
                    user_md.write_text(base)
        info["removed_memfiles"] = removed
        _, _, head = self.tool(["git", "add", "-A"],
                               [*GIT, *GIT_ID, "commit", "-q", "--allow-empty", "-m", f"Arbeitsstand nach {tag.split('.')[0]}"],
                               ["git", "rev-parse", "HEAD"], tag="commit")
        info["head"] = head.stdout.strip()
        return info

    # ---------- Schnappschüsse ----------
    def snapshot(self, name):
        if self.arm == "ghosttree":
            self.stop_gt_server()
        self.cleanup_sandboxes()
        snap = self.root / "snapshots"
        snap.mkdir(exist_ok=True)
        with tarfile.open(snap / f"{name}.tar", "w") as t:
            t.add(self.home, arcname="home")
            if self.arm == "ghosttree":
                t.add(self.root / "gt", arcname="gt")
        if self.arm == "ghosttree":
            self.start_gt_server()

    def cleanup_sandboxes(self):
        if self.sandbox_kind == "container":
            sbx.remove_containers(self.cid)

    def restore(self, name):
        if self.arm == "ghosttree":
            self.stop_gt_server()
        self.cleanup_sandboxes()
        shutil.rmtree(self.home)
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
        self.meta["post_learning_head"] = self.tool(["git", "rev-parse", "HEAD"], tag="head")[0].stdout.strip()
        self.meta["state_evidence"] = self.state_evidence()
        self.save_meta()

    def state_evidence(self):
        """Weist nach, dass der Gedächtniszustand des Arms existiert (Falle #1634)."""
        ev = {}
        mem = safe_glob(self.home, ".claude/projects/*/memory/*.md")
        ev["auto_memory_files"] = [str(p.relative_to(self.home)) for p in mem]
        ev["repo_memfiles"] = [str(p.relative_to(self.work)) for p, link in safe_walk(self.work)
                               if p.name in MEMFILE_NAMES and not link]
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
                patch = self.tool(["git", "diff", "--binary", head, "HEAD"], check=False, tag="patch")[0]
                (self.out / f"{tag}.patch").write_text(patch.stdout or "")
                grade = grade_task(F["id"], self.work, head, SandboxGradeRun(self, self.work))
                tx = transcript_stats(self.home, rec.get("session_id"), F["id"])
                self.write_record({"kind": "followup", "task": F["id"], "attempt": attempt, "invocations": [rec],
                                   "infra_failure": rec["infra_failure"], "grade": grade, "transcript": tx, **info})
                log(self.cid, f"{tag} graded func={grade['func']} know={grade['know']} success={grade['success']}")
                break
        self.restore("post-learning")
        self.stop_gt_server()
        self.cleanup_sandboxes()

    def write_record(self, rec):
        rec.update({"arm": self.arm, "rep": self.rep, "cid": self.cid, "ts": time.time()})
        files, violations = [], []
        for inv in rec.get("invocations") or []:
            for f in instruction_files(self.home, inv.get("session_id")):
                if f not in files:
                    files.append(f)
        # Pfade im Transkript sind Pfade der Sandbox
        own_md = f"{sbx.SANDBOX_HOME}/.claude/CLAUDE.md"
        for f in files:
            if f.startswith(self.iwork + "/") or f == own_md:
                continue
            if self.arm == "native" and f.startswith(f"{sbx.SANDBOX_HOME}/.claude/projects/"):
                continue  # Auto-Memory (MEMORY.md) ist der Mechanismus dieses Arms
            violations.append(f)
        rec["instruction_files"] = files
        rec["isolation_violations"] = violations
        if violations:
            log(self.cid, f"ISOLATION VIOLATION {rec.get('task')}: {violations}")
        if rec.get("invocations"):
            sid = rec["invocations"][-1].get("session_id")
            if sid:
                for p in safe_glob(self.home, f".claude/projects/*/{sid}.jsonl"):
                    shutil.copyfile(p, self.out / f"{rec['task']}.a{rec['attempt']}.transcript.jsonl")
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
GRADE_SRC, GRADE_HIDDEN, GRADE_DIR = "/grade-src", "/grade-hidden", "/tmp/g"


def hidden_test(task):
    p = P["hidden"] / f"{task.lower()}_test.go"
    return p if p.exists() else p.with_name(p.name + ".txt")


class SandboxGradeRun:
    """Bewertung eines Agentenstands: Der Stand wird nur lesbar in eine Sandbox ohne Netz gehängt, dort kopiert
    (die Kopie macht cp -a in der Sandbox, nicht der Harness), die verdeckten Tests kommen nur dort hinein (R5/R6)."""

    def __init__(self, chain, work):
        self.chain, self.work = chain, Path(work)
        self.hidden = None

    def place_hidden(self, src, name):
        self.hidden = (Path(src), name)

    def _box(self):
        mounts = [sbx.Mount(str(self.work), GRADE_SRC)]
        if self.hidden:
            mounts.append(sbx.Mount(str(self.hidden[0]), f"{GRADE_HIDDEN}/{self.hidden[1]}"))
        gocache = self.chain.root / "gocache-grade"  # nie vom Agenten benutzt
        gocache.mkdir(exist_ok=True)
        home = self.chain.root / "grade-home"  # leer: Agentencode bei der Bewertung sieht das HOME der Kette nicht
        rm_tree(home)
        home.mkdir()
        return self.chain.new_sandbox("grade", extra_mounts=mounts, gocache=gocache, home=home, model=False,
                                      roots=[self.work])

    def _prep(self, hidden):
        script = (f"set -e; rm -rf {GRADE_DIR}; mkdir -p {GRADE_DIR}; cp -a {GRADE_SRC}/. {GRADE_DIR}/; "
                  f"rm -rf {GRADE_DIR}/.git {GRADE_DIR}/zzhidden; ")
        if hidden and self.hidden:
            script += f"mkdir {GRADE_DIR}/zzhidden; cp {GRADE_HIDDEN}/{self.hidden[1]} {GRADE_DIR}/zzhidden/{self.hidden[1]}; "
        return script + f"cd {GRADE_DIR}; exec \"$@\""

    def go(self, argv, timeout, hidden=False):
        """Führt argv im frischen Abbild des Stands aus; hidden=True legt vorher die verdeckten Tests dazu."""
        with self._box() as box:
            if (self.chain.root / "gocache-grade").is_dir() and not any((self.chain.root / "gocache-grade").iterdir()):
                box.exec(["sh", "-c", f"[ -d /opt/gowarm/cache ] && cp -a /opt/gowarm/cache/. {GOCACHE_DIR}/ || true"])
            r = box.exec(["bash", "-c", self._prep(hidden), "_", *argv], cwd="/tmp", timeout=timeout)
        return self._checked(r), r.stdout + r.stderr

    def git(self, *args):
        with self._box() as box:
            r = box.exec([*GIT, "-C", GRADE_SRC, *args], cwd="/tmp", timeout=120)
        return self._checked(r), r.stdout

    @staticmethod
    def _checked(r):
        """Ein Fehler der Sandbox selbst (docker exec scheitert) ist kein „Aufgabe nicht gelöst“."""
        if r.rc in (125, 126, 127) and ("OCI runtime" in r.stderr or "docker" in r.stderr or "bwrap" in r.stderr):
            raise RuntimeError(f"Bewertung in der Sandbox nicht möglich: {r.stderr.strip()[-400:]}")
        return r.rc


class HostGradeRun:
    """Nur für vertrauenswürdige Stände (validate_ledgerly.py: Referenzlösungen, kein Agenten-Code)."""

    def __init__(self, work, env):
        self.work, self.env, self.tmp = Path(work), env, None

    def place_hidden(self, src, name):
        self.hidden = (Path(src), name)

    def go(self, argv, timeout, hidden=False):
        tmp = Path(self.env["TMPDIR"]) / f"grade-{secrets.token_hex(3)}"
        shutil.copytree(self.work, tmp, ignore=shutil.ignore_patterns(".git"))
        try:
            if hidden:
                hid = tmp / "zzhidden"
                hid.mkdir()
                shutil.copy(self.hidden[0], hid / self.hidden[1])
            r = subprocess.run(argv, env=self.env, cwd=tmp, text=True, timeout=timeout, stdin=subprocess.DEVNULL,
                               stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            return r.returncode, r.stdout
        finally:
            shutil.rmtree(tmp, ignore_errors=True)

    def git(self, *args):
        r = subprocess.run(["git", "-C", str(self.work), *args], env=self.env, text=True, stdin=subprocess.DEVNULL,
                           stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        return r.returncode, r.stdout


def grade_task(task, work, snapshot_sha, gr):
    out = {"task": task, "checks": {}}
    hidden = hidden_test(task)
    gr.place_hidden(hidden, f"{task.lower()}_test.go")
    # Repo-eigene Tests (ohne verdeckte)
    rc, text = gr.go(["go", "test", "-count=1"] + [t for t in P["repo_tests"]], 900)
    out["checks"]["repo_tests"] = rc == 0
    out["repo_tests_tail"] = text[-1500:]
    rc, text = gr.go(["go", "test", "-count=1", "-json", "./zzhidden/"], 600, hidden=True)
    tests = {}
    build_out = []
    for line in text.splitlines():
        try:
            ev = json.loads(line)
        except Exception:
            build_out.append(line)
            continue
        if ev.get("Test") and ev.get("Action") in ("pass", "fail") and "/" not in ev["Test"]:
            tests[ev["Test"]] = ev["Action"] == "pass"
        if ev.get("Action") == "output" and not ev.get("Test"):
            build_out.append(ev.get("Output", ""))
    src = hidden.read_text()
    declared = re.findall(r"^func (Test\w+)\(", src, re.M)
    for name in declared:
        out["checks"][name] = tests.get(name, False)  # Kompilierfehler => alle falsch
    out["hidden_output_tail"] = "".join(build_out)[-1500:]
    func_ok = out["checks"]["repo_tests"] and all(v for k, v in out["checks"].items() if k.startswith("TestFunc_"))
    know_checks = {k: v for k, v in out["checks"].items() if k.startswith("TestKnow_")}
    if PROJECT == "ledgerly" and task == "F5":
        mc = migration_checks(gr, snapshot_sha)
        out["migration"] = mc
        func_ok = func_ok and mc["new_vat_migration"]
        know_checks.update({"existing_unchanged": mc["existing_unchanged"], "no_not_null_without_default": mc["no_not_null_without_default"],
                            "down_present": mc["down_present"]})
    if PROJECT == "ledgerly" and task == "F6":
        _, gomod = gr.git("show", "HEAD:go.mod")
        has_sum = gr.git("cat-file", "-e", "HEAD:go.sum")[0] == 0
        gosum = gr.git("show", "HEAD:go.sum")[1] if has_sum else ""
        know_checks["stdlib_only"] = "require" not in gomod and not gosum.strip()
    out["know_checks"] = know_checks
    out["func"] = bool(func_ok)
    out["know"] = bool(all(know_checks.values())) if know_checks else None
    out["success"] = out["func"] and (out["know"] is None or out["know"])
    return out


def migration_checks(gr, snap):
    """Liest nur, was git im letzten Commit des Agenten verzeichnet (kein Lesen von Dateien des Agenten auf dem Host)."""
    _, diff = gr.git("diff", "--name-status", snap, "HEAD", "--", "migrations/")
    changed_existing, added = [], []
    for line in diff.splitlines():
        parts = line.split("\t")
        st, path = parts[0], parts[-1]
        if st.startswith("A"):
            added.append(path)
        else:
            changed_existing.append(line)

    def blob(path):
        return gr.git("show", f"HEAD:{path}")[1]

    ups = [p for p in added if p.endswith(".sql") and not p.endswith(".down.sql")]
    vat_ups = [p for p in ups if "vat_id" in blob(p).lower()]
    violations = []
    for p in vat_ups:
        for stmt in SQL_STMT_SPLIT.split(re.sub(r"--[^\n]*", "", blob(p))):
            if "vat_id" in stmt.lower() and re.search(r"not\s+null", stmt, re.I) and not re.search(r"\bdefault\b", stmt, re.I):
                violations.append(stmt.strip()[:200])
    downs = [p[:-4] + ".down.sql" for p in vat_ups]
    return {"added": added, "changed_existing": changed_existing, "new_vat_migration": bool(vat_ups),
            "existing_unchanged": not changed_existing, "not_null_violations": violations,
            "no_not_null_without_default": bool(vat_ups) and not violations,
            "down_present": bool(vat_ups) and all(gr.git("cat-file", "-e", f"HEAD:{d}")[0] == 0 for d in downs)}


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
    for fn in safe_glob(home, f".claude/projects/*/{sid}.jsonl"):
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
    files = safe_glob(home, f".claude/projects/*/{sid}.jsonl")
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
    """Bewertet die Folgesessions einer Kette neu: Schnappschuss entpacken, Patch anwenden, prüfen. Alles in der Sandbox."""
    out = RESULTS / cid
    meta = json.loads((out / "chain.json").read_text())
    head = meta["post_learning_head"]
    tarp = RUNS / cid / "snapshots" / "post-learning.tar"
    recs = [json.loads(line) for line in (out / "records.jsonl").read_text().splitlines()]
    res = []
    for r in recs:
        if r["kind"] != "followup":
            continue
        ch = Chain("vanilla", meta.get("rep", 0), cid=f"rg-{secrets.token_hex(3)}")  # nur die Sandbox, kein Gedächtnis
        ch.root.mkdir(parents=True)
        (ch.root / "gocache").mkdir()
        ch.sock.mkdir()
        ch.ensure_proxy()
        try:
            with tarfile.open(tarp) as t:
                t.extractall(ch.root, filter="tar")
            patch = out / f"{r['task']}.a{r['attempt']}.patch"
            if patch.exists() and patch.read_text().strip():
                shutil.copyfile(patch, ch.home / ".regrade.patch")
                ch.tool(["git", "update-index", "-q", "--refresh"], check=False, tag="regrade")
                ch.tool(["git", "apply", "--binary", "--index", f"{sbx.SANDBOX_HOME}/.regrade.patch"],
                        [*GIT, *GIT_ID, "commit", "-qm", "regrade"], tag="regrade")
            g = grade_task(r["task"], ch.work, head, SandboxGradeRun(ch, ch.work))
            res.append({"task": r["task"], "attempt": r["attempt"], "old": {k: r["grade"][k] for k in ("func", "know", "success")},
                        "new": {k: g[k] for k in ("func", "know", "success")}, "grade": g})
        finally:
            ch.cleanup()
            shutil.rmtree(ch.root, ignore_errors=True)
            shutil.rmtree(ch.out, ignore_errors=True)
    (out / "regraded.jsonl").write_text("".join(json.dumps(x) + "\n" for x in res))
    for x in res:
        print(cid, x["task"], x["old"], "->", x["new"])


# ---------- Vorabprüfung und Probelauf ----------
def precheck():
    """Ohne nutzbaren Sandbox-Weg gibt es keinen Lauf (geschlossen bei Fehler)."""
    kind = (SANDBOX_KIND or "").strip().lower()
    if kind not in sbx.KINDS:
        raise sbx.IsolationError(f"BENCH_SANDBOX={SANDBOX_KIND!r}: erlaubt sind {', '.join(sbx.KINDS)}. "
                                 "Es gibt keinen Weg ohne Sandbox.")
    if kind == "container" and not sbx.docker_ready():
        raise sbx.IsolationError("Container-Sandbox nicht bereit: Docker antwortet nicht oder das Image fehlt "
                                 f"(BENCH_IMAGE={os.environ.get('BENCH_IMAGE', 'agent-compare:local')}; bauen mit image/build.sh)")
    if kind == "bwrap" and not shutil.which(os.environ.get("BENCH_BWRAP", "bwrap")):
        raise sbx.IsolationError("bubblewrap (bwrap) ist nicht installiert")


def probe_run(keep=False):
    """Isolationsprobe ohne Modellaufruf: vanilla-Kette aufbauen, vor und nach `claude --version` proben."""
    precheck()
    c = Chain("vanilla", 0, cid="probe-" + secrets.token_hex(3))
    RESULTS.mkdir(parents=True, exist_ok=True)
    ok = False
    try:
        c.prepare()
        with c.new_sandbox("probe") as box:
            res, ilog = box.guarded([AGENT, "--version"], c.out / "isolation.json", 120, cwd=c.iwork)
        print(f"{AGENT} --version -> rc={res.rc} {res.stdout.strip()[:80]}")
        doc = json.loads(ilog.path.read_text())
        for phase, rep in doc["phases"].items():
            print(f"{phase}: {'bestanden' if rep['ok'] else 'NICHT bestanden'}")
            for name, chk in rep["checks"].items():
                print(f"  {'ok ' if chk['ok'] else 'FEHLER'} {name}: {chk['detail']}")
        print(f"Protokoll: {ilog.path}  sha256={ilog.sha256()}")
        ok = doc["ok"] and res.rc == 0
    finally:
        c.cleanup()
        if not keep:
            shutil.rmtree(c.root, ignore_errors=True)
    return 0 if ok else 1


def main():
    args = sys.argv[1:]
    if args and args[0] == "chain":
        arm, rep = args[1], int(args[2])
        only = None
        if "--tasks" in args:
            only = set(args[args.index("--tasks") + 1].split(","))
        precheck()
        c = Chain(arm, rep)
        RESULTS.mkdir(parents=True, exist_ok=True)
        try:
            c.ensure_proxy().require_key()
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
            c.cleanup()
            if c.meta_path.exists():
                c.save_meta()
                shutil.copy(c.meta_path, c.out / "chain.json")
    elif args and args[0] == "probe":
        sys.exit(probe_run("--keep" in args))
    elif args and args[0] == "cleanup":
        print("entfernt:", sbx.remove_containers() or "nichts")
    elif args and args[0] == "regrade":
        precheck()
        for cid in args[1:]:
            regrade(cid)
    elif args and args[0] == "grade":
        precheck()
        c = Chain("vanilla", 0, cid="grade-" + secrets.token_hex(3))
        c.root.mkdir(parents=True)
        (c.root / "gocache").mkdir()
        c.sock.mkdir()
        c.ensure_proxy()
        try:
            work = Path(args[2]).resolve()
            print(json.dumps(grade_task(args[1], work, args[3], SandboxGradeRun(c, work)), indent=2))
        finally:
            c.cleanup()
            shutil.rmtree(c.root, ignore_errors=True)
            shutil.rmtree(c.out, ignore_errors=True)
    else:
        print(__doc__)
        sys.exit(2)


if __name__ == "__main__":
    main()
