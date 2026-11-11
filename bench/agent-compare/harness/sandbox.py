#!/usr/bin/env python3
"""Sandbox für Agentensitzungen des Benchmark-Harness (PAIN-56).

Eine Sandbox sperrt einen Agenten ein, der mit --dangerously-skip-permissions läuft: kein Netz,
nur lesbare Wurzel, ein Wegwerf-HOME, Modellzugang nur über einen Unix-Socket. Zwei Wege liegen
hinter derselben Schnittstelle (`Sandbox`): `ContainerSandbox` (Docker, Standard) und
`BwrapSandbox` (bubblewrap, ohne Daemon, für lokale Probeläufe). `make_sandbox` kennt keinen
dritten Weg: ohne Sandbox gibt es keine Sitzung.

Geschlossen bei Fehler, in drei Stufen:
  1. `verify_spec`: Die Beschreibung der Sandbox wird geprüft, bevor etwas startet.
  2. `inspect`/`verify_runtime`: Der Start wird nachgemessen (Docker: `docker inspect`).
  3. Isolationsprobe (probe.py) in der Sandbox, vor und nach der Sitzung. Der Harness wertet die
     Ausgabe aus und schreibt das Isolationsprotokoll; was die Probe nicht meldet, gilt als nicht bestanden.
Jede Stufe, die nicht besteht, wirft `IsolationError`, bevor der Agent läuft.
"""
import hashlib
import json
import os
import re
import secrets
import shutil
import subprocess
from dataclasses import dataclass, field
from pathlib import Path

HERE = Path(__file__).resolve().parent
IMAGE_DIR = HERE.parent / "image"
SANDBOX_HOME = "/home/agent"
IN_DIR = "/bench-in"
RUN_DIR = "/run/bench"
PROXY_PORT = 18317
TOKEN_ENV = "ANTHROPIC_AUTH_TOKEN"

# Nur diese Namen darf die Umgebung des Agenten tragen. Nichts wird vom Harness geerbt.
ALLOWED_ENV_NAMES = frozenset({
    "PATH", "HOME", "USER", "LOGNAME", "LANG", "TERM", "SHELL", "TMPDIR",
    "GOTOOLCHAIN", "GOCACHE", "GOMODCACHE", "GOPROXY", "GOFLAGS", "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "DISABLE_AUTOUPDATER", "CLAUDE_CODE_DISABLE_AUTO_MEMORY",
    "ANTHROPIC_BASE_URL", TOKEN_ENV,
})
SECRET_LIKE = re.compile(r"(API_?KEY|SECRET|PASSW|CREDENTIAL|PRIVATE|^SSH_|^AWS_|^GH_|^GITHUB_|TOKEN)", re.I)
REQUIRED_CHECKS = ("net_public", "net_interfaces", "dns", "host_paths", "env_names", "env_secrets",
                   "mounts", "root_readonly", "privileges", "proxy")
EXEC_TIMEOUT_RCS = (124, 137)


class IsolationError(RuntimeError):
    """Die Isolation ist nicht nachgewiesen. Der Agent darf nicht starten."""


@dataclass(frozen=True)
class Mount:
    src: str
    dst: str
    rw: bool = False


@dataclass
class SandboxSpec:
    run_id: str
    mounts: list
    env: dict
    host_home: str
    allowed_roots: list
    allowed_files: list = field(default_factory=list)
    forwards: list = field(default_factory=list)  # (Port im Netz der Sandbox, Socket in der Sandbox)
    network: str = "none"
    read_only_root: bool = True
    cap_drop_all: bool = True
    no_new_privileges: bool = True
    uid: int = 1000
    gid: int = 1000
    workdir: str = SANDBOX_HOME
    limits: dict = field(default_factory=lambda: {"cpus": 4, "memory": "8g", "pids": 2048})
    proxy_port: int = PROXY_PORT
    git: dict | None = None  # {"repo": ..., "absent_objects": [...], "allowed_refs": [...], "commit_count": n}
    extra_probe_targets: list = field(default_factory=list)  # [["host", port]], aus der Umgebung des Harness


@dataclass
class ExecResult:
    rc: int
    stdout: str
    stderr: str
    timed_out: bool = False


def _under(path, root):
    return path == root or path.startswith(root.rstrip("/") + "/")


def _real(path):
    return os.path.realpath(path)


def verify_spec(spec, extra_mounts=()):
    """Prüft die Beschreibung einer Sandbox, ohne etwas zu starten. Gibt die Verstöße zurück."""
    bad = []
    if spec.network != "none":
        bad.append(f"Netz ist nicht aus (network={spec.network!r})")
    if not spec.read_only_root:
        bad.append("Wurzel ist beschreibbar")
    if not spec.cap_drop_all:
        bad.append("Capabilities sind nicht entfernt")
    if not spec.no_new_privileges:
        bad.append("no-new-privileges fehlt")
    if spec.uid == 0:
        bad.append("Agent liefe als root")
    for name in spec.env:
        if name not in ALLOWED_ENV_NAMES:
            bad.append(f"Umgebungsvariable nicht erlaubt: {name}")
        elif SECRET_LIKE.search(name) and name != TOKEN_ENV:
            bad.append(f"Umgebungsvariable sieht nach Zugangsdaten aus: {name}")
    home = _real(spec.host_home)
    roots = [_real(r) for r in spec.allowed_roots]
    files = {_real(f) for f in spec.allowed_files}
    seen = set()
    for m in list(spec.mounts) + list(extra_mounts):
        src, dst = _real(m.src), os.path.normpath(m.dst)
        if not os.path.isabs(m.dst) or dst == "/":
            bad.append(f"Ziel der Einhängung ungültig: {m.dst}")
        if dst in seen:
            bad.append(f"Ziel doppelt eingehängt: {dst}")
        seen.add(dst)
        if _under(dst, home):
            bad.append(f"Ziel liegt im HOME des Hosts: {dst}")
        if src == "/" or _under(home, src):
            bad.append(f"Einhängung legt das HOME des Hosts offen: {m.src}")
        elif "docker.sock" in src:
            bad.append(f"Docker-Socket eingehängt: {m.src}")
        elif src not in files and not any(_under(src, r) for r in roots):
            bad.append(f"Quelle liegt außerhalb der erlaubten Wurzeln: {m.src}")
    for port, sock in spec.forwards:
        if not isinstance(port, int) or not sock.startswith(RUN_DIR + "/"):
            bad.append(f"Weiterleitung ungültig: {port} {sock}")
    return bad


def evaluate_probe(raw, spec):
    """Wertet die Ausgabe von probe.py aus. Fehlende oder gescheiterte Proben sind nicht bestanden."""
    try:
        data = json.loads(raw)
        checks = data["checks"]
        if not isinstance(checks, dict):
            raise TypeError("checks ist kein Objekt")
    except (ValueError, KeyError, TypeError):
        return {"ok": False, "failed": ["probe_output"], "checks": {}, "detail": "Ausgabe der Probe unlesbar"}
    required = list(REQUIRED_CHECKS) + (["git"] if spec.git else [])
    failed = [n for n in required if not (isinstance(checks.get(n), dict) and checks[n].get("ok") is True)]
    return {"ok": not failed, "failed": failed, "checks": checks, "uid": data.get("uid")}


def spec_fingerprint(spec, extra_mounts, extra=None):
    """Hash der Sandbox-Beschreibung: Einhängungen, Namen der Umgebung, Netz, Grenzen, Probe und Hülle."""
    doc = {
        "mounts": sorted((m.dst, m.rw) for m in list(spec.mounts) + list(extra_mounts)),
        "env_names": sorted(spec.env), "forwards": sorted(spec.forwards), "network": spec.network,
        "limits": spec.limits, "uid": spec.uid, "git": spec.git,
        "probe_sha256": hashlib.sha256((HERE / "probe.py").read_bytes()).hexdigest(),
        "shell_sha256": hashlib.sha256((IMAGE_DIR / "sandbox-run.sh").read_bytes()).hexdigest(),
        **(extra or {}),
    }
    return hashlib.sha256(json.dumps(doc, sort_keys=True).encode()).hexdigest()


class IsolationLog:
    """Das Isolationsprotokoll einer Sitzung: eine Datei, je Phase (before/after) das Ergebnis der Probe."""

    def __init__(self, path, sandbox):
        self.path, self.sandbox = Path(path), sandbox
        self.phases = {}

    def record(self, phase, report):
        self.phases[phase] = report
        doc = {"version": 1, "run_id": self.sandbox.spec.run_id, "sandbox": self.sandbox.kind,
               "spec_sha256": self.sandbox.fingerprint, "runtime": self.sandbox.facts_summary(),
               "ok": all(p["ok"] for p in self.phases.values()), "phases": self.phases}
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.path.write_text(json.dumps(doc, indent=2, sort_keys=True))

    def sha256(self):
        return hashlib.sha256(self.path.read_bytes()).hexdigest() if self.path.exists() else None


class Sandbox:
    """Schnittstelle. Unterklassen liefern Start, Aufrufhülle, Nachmessung und Ende."""

    kind = ""
    implicit_mounts = ("/proc", "/dev", "/sys", "/tmp")

    def __init__(self, spec, in_dir):
        self.spec = spec
        self.in_dir = Path(in_dir)
        self.started = False
        self.facts = {}

    # ---------- Beschreibung ----------
    def all_mounts(self):
        return list(self.spec.mounts) + self.base_mounts()

    def base_mounts(self):
        return [Mount(str(self.in_dir), IN_DIR), Mount(str(self.in_dir / "passwd"), "/etc/passwd"),
                Mount(str(self.in_dir / "group"), "/etc/group")]

    @property
    def fingerprint(self):
        return spec_fingerprint(self.spec, self.base_mounts(), self.fingerprint_extra())

    def fingerprint_extra(self):
        return {"kind": self.kind}

    def facts_summary(self):
        return {}

    def probe_config(self):
        s = self.spec
        forbidden = {s.host_home, "/var/run/docker.sock", "/run/docker.sock"}
        cfg = {"run_id": s.run_id, "proxy_port": s.proxy_port, "env_names": sorted(s.env),
               "forbidden_paths": sorted(forbidden), "allowed_home_entries": [Path(SANDBOX_HOME).name],
               "expected_mounts": {m.dst: m.rw for m in self.all_mounts()},
               "implicit_mounts": list(self.implicit_mounts), "allowed_secret_like": [TOKEN_ENV],
               "extra_targets": s.extra_probe_targets}
        if s.git:
            cfg["git"] = s.git
        return cfg

    def prepare_in(self):
        d = self.in_dir
        d.mkdir(parents=True, exist_ok=True)
        os.chmod(d, 0o755)
        shutil.copy(HERE / "probe.py", d / "probe.py")
        shutil.copy(IMAGE_DIR / "sandbox-run.sh", d / "sandbox-run.sh")
        (d / "probe.json").write_text(json.dumps(self.probe_config(), indent=2))
        (d / "forwards").write_text("".join(f"{p} {sock}\n" for p, sock in self.spec.forwards))
        s = self.spec
        (d / "passwd").write_text("root:x:0:0:root:/root:/bin/bash\n"
                                  f"agent:x:{s.uid}:{s.gid}:agent:{SANDBOX_HOME}:/bin/bash\n"
                                  "nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin\n")
        (d / "group").write_text(f"root:x:0:\nagent:x:{s.gid}:\nnogroup:x:65534:\n")

    # ---------- Ablauf ----------
    def start(self):
        problems = verify_spec(self.spec, self.base_mounts())
        if problems:
            raise IsolationError("Sandbox-Beschreibung verletzt die Regeln: " + "; ".join(problems))
        self.prepare_in()
        self._start()
        self.started = True
        try:
            self.facts = self.inspect()
            problems = self.verify_runtime(self.facts)
        except Exception:
            self.stop()
            raise
        if problems:
            self.stop()
            raise IsolationError("gestartete Sandbox verletzt die Regeln: " + "; ".join(problems))
        return self

    def __enter__(self):
        return self.start()

    def __exit__(self, *exc):
        self.stop()

    def exec(self, argv, env=None, cwd=None, timeout=900):
        """Führt argv in der Sandbox aus, mit der Umgebung des Agenten (nur die Namen aus spec.env)."""
        if not self.started:
            raise IsolationError("Sandbox ist nicht gestartet")
        env = self.spec.env if env is None else env
        inner = ["/bin/bash", f"{IN_DIR}/sandbox-run.sh", str(timeout), "env", "-i",
                 *[f"{k}={v}" for k, v in env.items()], *argv]
        try:
            r = subprocess.run(self._wrap(inner, cwd or self.spec.workdir), stdin=subprocess.DEVNULL,
                               capture_output=True, timeout=timeout + 90, env=self.launcher_env())
        except subprocess.TimeoutExpired as ex:
            return ExecResult(-9, (ex.stdout or b"").decode(errors="replace"), "timeout", True)
        return ExecResult(r.returncode, r.stdout.decode(errors="replace"), r.stderr.decode(errors="replace"),
                          r.returncode in EXEC_TIMEOUT_RCS)

    def probe(self):
        r = self.exec(["python3", f"{IN_DIR}/probe.py", f"{IN_DIR}/probe.json"], timeout=120)
        report = evaluate_probe(r.stdout, self.spec)
        if r.rc != 0 and report["ok"]:
            report = {"ok": False, "failed": ["probe_exit"], "checks": report["checks"], "detail": f"rc={r.rc}"}
        if not report["ok"] and "detail" not in report:
            report["detail"] = (r.stderr or "")[-500:]
        return report

    def guarded(self, argv, log_path, timeout, cwd=None):
        """Probe, Agent, Probe. Besteht eine Probe nicht, wird der Agent nicht (mehr) als gültig gewertet."""
        log = IsolationLog(log_path, self)
        before = self.probe()
        log.record("before", before)
        if not before["ok"]:
            raise IsolationError(f"Isolationsprobe vor der Sitzung nicht bestanden: {before['failed']}")
        result = self.exec(argv, cwd=cwd, timeout=timeout)
        after = self.probe()
        log.record("after", after)
        if not after["ok"]:
            raise IsolationError(f"Isolationsprobe nach der Sitzung nicht bestanden: {after['failed']}")
        return result, log

    # ---------- Unterklassen ----------
    def _start(self):
        raise NotImplementedError

    def _wrap(self, inner, cwd):
        raise NotImplementedError

    def launcher_env(self) -> dict | None:
        """Umgebung des Startprogramms (docker, bwrap). None heißt: erben."""
        return None

    def inspect(self):
        return {}

    def verify_runtime(self, facts):
        return []

    def stop(self):
        self.started = False


class ContainerSandbox(Sandbox):
    kind = "container"
    implicit_mounts = ("/proc", "/dev", "/sys", "/tmp", "/etc/hosts", "/etc/hostname", "/etc/resolv.conf",
                       "/usr/sbin/docker-init")

    def __init__(self, spec, in_dir, image=None, docker=None):
        super().__init__(spec, in_dir)
        self.image = image or os.environ.get("BENCH_IMAGE", "agent-compare:local")
        self.docker = docker or os.environ.get("BENCH_DOCKER", "docker")
        self.name = f"bench-{spec.run_id}-{secrets.token_hex(3)}"

    def fingerprint_extra(self):
        return {"kind": self.kind, "image": self.image, "image_id": self.facts.get("image_id")}

    def facts_summary(self):
        return {k: self.facts.get(k) for k in ("image", "image_id", "container", "network_mode") if k in self.facts}

    def run_args(self):
        s = self.spec
        args = [self.docker, "run", "-d", "--init", "--name", self.name, "--label", "bench.sandbox=1",
                "--label", f"bench.run={s.run_id}", "--network", s.network, "--read-only", "--cap-drop", "ALL",
                "--security-opt", "no-new-privileges", "--user", f"{s.uid}:{s.gid}",
                "--cpus", str(s.limits["cpus"]), "--memory", s.limits["memory"], "--memory-swap", s.limits["memory"],
                "--pids-limit", str(s.limits["pids"]), "--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=2g,mode=1777",
                "--entrypoint", "sleep"]
        for m in self.all_mounts():
            args += ["--mount", f"type=bind,src={m.src},dst={m.dst}" + ("" if m.rw else ",readonly")]
        return args + [self.image, "infinity"]

    def _docker(self, *args, check=True):
        r = subprocess.run([self.docker, *args], capture_output=True, text=True, stdin=subprocess.DEVNULL)
        if check and r.returncode != 0:
            raise IsolationError(f"docker {args[0]} gescheitert: {(r.stderr or r.stdout).strip()[-500:]}")
        return r

    def _start(self):
        r = subprocess.run(self.run_args(), capture_output=True, text=True, stdin=subprocess.DEVNULL)
        if r.returncode != 0:
            raise IsolationError(f"Container startet nicht: {(r.stderr or r.stdout).strip()[-500:]}")

    def _wrap(self, inner, cwd):
        s = self.spec
        return [self.docker, "exec", "-u", f"{s.uid}:{s.gid}", "-w", cwd, self.name, *inner]

    def inspect(self):
        raw = json.loads(self._docker("inspect", self.name).stdout)[0]
        return {"raw": raw, "image": self.image, "image_id": raw.get("Image"), "container": self.name,
                "network_mode": raw["HostConfig"].get("NetworkMode")}

    def verify_runtime(self, facts):
        raw, s = facts["raw"], self.spec
        host, cfg, bad = raw["HostConfig"], raw["Config"], []
        if not raw["State"].get("Running"):
            bad.append("Container läuft nicht")
        if host.get("NetworkMode") != "none":
            bad.append(f"NetworkMode ist {host.get('NetworkMode')!r}")
        if not host.get("ReadonlyRootfs"):
            bad.append("Wurzel des Containers ist beschreibbar")
        if "ALL" not in (host.get("CapDrop") or []) or host.get("CapAdd"):
            bad.append(f"Capabilities: drop={host.get('CapDrop')} add={host.get('CapAdd')}")
        if not any(o.startswith("no-new-privileges") for o in host.get("SecurityOpt") or []):
            bad.append("no-new-privileges fehlt")
        if host.get("Privileged") or host.get("Devices") or host.get("PortBindings") or host.get("PidMode") or \
                host.get("IpcMode") == "host" or host.get("UsernsMode") == "host":
            bad.append("Container ist privilegiert oder teilt Namensräume mit dem Host")
        if cfg.get("User") != f"{s.uid}:{s.gid}":
            bad.append(f"Benutzer im Container: {cfg.get('User')!r}")
        want = {m.dst: (_real(m.src), m.rw) for m in self.all_mounts()}
        got = {}
        for m in raw.get("Mounts", []):
            if m.get("Type") == "tmpfs" and m.get("Destination") == "/tmp":
                continue
            got[m.get("Destination")] = (_real(m.get("Source", "")) if m.get("Type") == "bind" else m.get("Type"), bool(m.get("RW")))
        if got != want:
            bad.append(f"Einhängungen weichen ab: erwartet {sorted(want)}, vorhanden {sorted(got)}")
        for name in (e.split("=", 1)[0] for e in cfg.get("Env") or []):
            if SECRET_LIKE.search(name):
                bad.append(f"Umgebung des Containers trägt {name}")
        if host.get("Memory") != _bytes(s.limits["memory"]) or host.get("PidsLimit") != s.limits["pids"]:
            bad.append("Grenzen für Speicher oder Prozesse fehlen")
        return bad

    def stop(self):
        if self.started or self.facts:
            subprocess.run([self.docker, "rm", "-f", self.name], capture_output=True, stdin=subprocess.DEVNULL)
        super().stop()


def _bytes(size):
    m = re.fullmatch(r"(\d+)([kmg]?)", size.lower())
    return int(m.group(1)) * {"": 1, "k": 1 << 10, "m": 1 << 20, "g": 1 << 30}[m.group(2)] if m else None


class BwrapSandbox(Sandbox):
    """bubblewrap: kein Daemon, kein root. Die Werkzeuge kommen vom Host (/usr), Grenzen für Speicher und
    Prozesse gibt es hier nicht. Gedacht für lokale Probeläufe; das Protokoll nennt den Weg."""

    kind = "bwrap"
    implicit_mounts = ("/proc", "/dev", "/tmp", "/usr", "/bin", "/sbin", "/lib", "/lib64")

    def __init__(self, spec, in_dir, bwrap=None):
        super().__init__(spec, in_dir)
        self.bwrap = bwrap or os.environ.get("BENCH_BWRAP", "bwrap")

    def facts_summary(self):
        return {"limits_enforced": False}

    def bwrap_args(self, cwd):
        a = [self.bwrap, "--unshare-all", "--die-with-parent", "--new-session", "--clearenv", "--hostname", "bench",
             "--setenv", "PATH", "/usr/bin:/bin", "--ro-bind", "/usr", "/usr"]
        for d in ("bin", "sbin", "lib", "lib64"):
            p = f"/{d}"
            if os.path.islink(p):
                a += ["--symlink", os.readlink(p), p]
            elif os.path.isdir(p):
                a += ["--ro-bind", p, p]
        a += ["--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp"]
        for m in self.all_mounts():
            a += ["--bind" if m.rw else "--ro-bind", m.src, m.dst]
        return a + ["--remount-ro", "/", "--chdir", cwd]

    def launcher_env(self) -> dict | None:
        # bwrap bleibt als Init-Prozess in der Sandbox und trägt seine eigene Umgebung; /proc/1/environ
        # wäre für den Agenten lesbar. --clearenv leert nur den Kindprozess. Also startet bwrap leer.
        return {"PATH": "/usr/bin:/bin"}

    def _start(self):
        if not shutil.which(self.bwrap):
            raise IsolationError("bubblewrap (bwrap) ist nicht installiert")

    def _wrap(self, inner, cwd):
        return self.bwrap_args(cwd) + ["--", *inner]

    def verify_runtime(self, facts):
        args = self.bwrap_args(self.spec.workdir)
        bad = [f"bwrap ohne {flag}" for flag in ("--unshare-all", "--clearenv", "--die-with-parent") if flag not in args]
        bad += [f"bwrap mit {flag}" for flag in ("--share-net", "--cap-add", "--unshare-all-try") if flag in args]
        return bad


KINDS = {"container": ContainerSandbox, "bwrap": BwrapSandbox}


def make_sandbox(kind, spec, in_dir, **kw):
    """Es gibt keine Sandbox „none“: ein unbekannter oder leerer Wert ist ein Fehler, kein Rückfall."""
    cls = KINDS.get((kind or "").strip().lower())
    if cls is None:
        raise IsolationError(f"keine Sandbox gewählt oder unbekannt: {kind!r} (erlaubt: {', '.join(KINDS)}). "
                             "Ohne Isolation startet der Harness keinen Agenten mit --dangerously-skip-permissions.")
    return cls(spec, in_dir, **kw)


def remove_containers(run_id=None, docker=None):
    """Entfernt Sandbox-Container nach Kennung (Label), nie nach einem Muster in Prozessen."""
    docker = docker or os.environ.get("BENCH_DOCKER", "docker")
    flt = ["--filter", "label=bench.sandbox=1"] + (["--filter", f"label=bench.run={run_id}"] if run_id else [])
    r = subprocess.run([docker, "ps", "-aq", *flt], capture_output=True, text=True, stdin=subprocess.DEVNULL)
    ids = r.stdout.split()
    if ids:
        subprocess.run([docker, "rm", "-f", *ids], capture_output=True, stdin=subprocess.DEVNULL)
    return ids


def docker_ready(image=None, docker=None):
    """True, wenn der Docker-Daemon antwortet und das Image vorhanden ist (für Tests und Vorabprüfung)."""
    docker = docker or os.environ.get("BENCH_DOCKER", "docker")
    image = image or os.environ.get("BENCH_IMAGE", "agent-compare:local")
    try:
        return subprocess.run([docker, "image", "inspect", image], capture_output=True, timeout=30,
                              stdin=subprocess.DEVNULL).returncode == 0
    except (OSError, subprocess.TimeoutExpired):
        return False
