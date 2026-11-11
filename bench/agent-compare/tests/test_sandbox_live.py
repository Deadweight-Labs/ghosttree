"""Tests mit echter Sandbox. Ohne Docker (bzw. ohne das Image) und ohne nutzbares bubblewrap werden sie übersprungen.

Der Container-Test braucht das Image: `image/build.sh` baut `agent-compare:local`, mit BENCH_IMAGE lässt sich ein
anderes angeben. Kein Test ruft das Modell auf: Der Proxy beantwortet nur seinen Ping, `claude` läuft nur mit --version.
"""
import json
import os
import secrets
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

HARNESS = Path(__file__).resolve().parent.parent / "harness"
sys.path.insert(0, str(HARNESS))
_TMP = tempfile.mkdtemp(prefix="bench-live-")
os.environ.setdefault("BENCH_BASE", os.path.join(_TMP, "base"))
os.environ.setdefault("BENCH_RUNS", os.path.join(_TMP, "runs"))
import bench  # noqa: E402
import sandbox as sbx  # noqa: E402
from modelproxy import ModelProxy  # noqa: E402


def bwrap_usable():
    try:
        return subprocess.run(["bwrap", "--unshare-all", "--ro-bind", "/", "/", "--", "true"],
                              capture_output=True, timeout=30).returncode == 0
    except (OSError, subprocess.TimeoutExpired):
        return False


class LiveMixin:
    kind = ""
    sandbox_kwargs: dict = {}

    def setUp(self):
        self.root = Path(tempfile.mkdtemp(prefix="live-", dir=_TMP))
        (self.root / "home").mkdir()
        (self.root / "sock").mkdir()
        self.token = secrets.token_hex(8)
        self.proxy = ModelProxy(self.root / "sock" / "model.sock", "live", self.token).start()
        self.addCleanup(self.proxy.stop)
        self.host_home = str(Path.home())
        self.box = self.make_box()
        self.addCleanup(self.box.stop)
        self.box.start()

    def make_box(self, cls=None):
        # wie beim Harness: der beschreibbare Ordner des Agenten steht im PATH vorn
        env = {"PATH": f"{sbx.SANDBOX_HOME}/.local/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin", "HOME": sbx.SANDBOX_HOME, "LANG": "C.UTF-8",
               "TMPDIR": "/tmp", "ANTHROPIC_BASE_URL": f"http://127.0.0.1:{sbx.PROXY_PORT}", sbx.TOKEN_ENV: self.token}
        spec = sbx.SandboxSpec(
            run_id="live", env=env, host_home=self.host_home, allowed_roots=[str(self.root)],
            mounts=[sbx.Mount(str(self.root / "home"), sbx.SANDBOX_HOME, True), sbx.Mount(str(self.root / "sock"), sbx.RUN_DIR)],
            forwards=[(sbx.PROXY_PORT, f"{sbx.RUN_DIR}/model.sock")], uid=os.getuid(), gid=os.getgid())
        return (cls or sbx.KINDS[self.kind])(spec, self.root / f"in-{secrets.token_hex(2)}", **self.sandbox_kwargs)

    def test_probe_passes_with_every_check(self):
        report = self.box.probe()
        self.assertTrue(report["ok"], report)
        self.assertLessEqual(set(sbx.REQUIRED_CHECKS), set(report["checks"]))

    def test_no_connection_to_the_outside(self):
        code = ("import socket,sys\n"
                "for h,p in (('1.1.1.1',443),('8.8.8.8',53),('93.184.216.34',80)):\n"
                "    try:\n"
                "        socket.create_connection((h,p),timeout=3); print('ERREICHT',h); sys.exit(1)\n"
                "    except OSError: pass\n"
                "try:\n"
                "    socket.getaddrinfo('example.com',443); print('DNS'); sys.exit(1)\n"
                "except OSError: pass\n")
        r = self.box.exec(["python3", "-c", code], timeout=60)
        self.assertEqual((r.rc, r.stdout), (0, ""), r.stderr)

    def test_host_home_is_not_visible(self):
        r = self.box.exec(["sh", "-c", f"test ! -e {self.host_home} && ls /home"])
        self.assertEqual(r.rc, 0, r.stderr)
        self.assertEqual(r.stdout.split(), ["agent"])

    def test_host_environment_is_not_visible_in_any_process(self):
        with mock.patch.dict(os.environ, {"BENCH_LIVE_LEAK_CANARY": "kanarienvogel"}):
            r = self.box.exec(["sh", "-c", "cat /proc/*/environ 2>/dev/null | tr '\\0' '\\n'"])
        self.assertNotIn("kanarienvogel", r.stdout)
        self.assertNotIn("BENCH_LIVE_LEAK_CANARY", r.stdout)
        names = {line.split("=", 1)[0] for line in r.stdout.splitlines() if "=" in line}
        self.assertFalse({"ANTHROPIC_API_KEY", "SSH_AUTH_SOCK", "GITHUB_TOKEN"} & names, names)

    def test_root_is_read_only_but_home_is_writable(self):
        r = self.box.exec(["sh", "-c", "touch /nope 2>&1; echo $?; touch $HOME/ja && echo ok"])
        lines = r.stdout.split()
        self.assertNotEqual(lines[-2], "0", r.stdout)
        self.assertEqual(lines[-1], "ok")
        self.assertTrue((self.root / "home" / "ja").exists())

    def test_session_writes_protocol_before_and_after(self):
        res, log = self.box.guarded(["python3", "-c", "print('agent lief')"], self.root / "isolation.json", 60)
        self.assertEqual(res.stdout.strip(), "agent lief")
        doc = json.loads((self.root / "isolation.json").read_text())
        self.assertTrue(doc["ok"])
        self.assertEqual(sorted(doc["phases"]), ["after", "before"])
        self.assertEqual(doc["sandbox"], self.kind)
        self.assertEqual(log.sha256(), __import__("hashlib").sha256((self.root / "isolation.json").read_bytes()).hexdigest())

    def test_ping_over_the_loopback_forward_reaches_the_proxy(self):
        code = ("import http.client,os,json\n"
                f"c=http.client.HTTPConnection('127.0.0.1',{sbx.PROXY_PORT},timeout=10)\n"
                "c.request('GET','/_bench/ping',headers={'authorization':'Bearer '+os.environ['ANTHROPIC_AUTH_TOKEN']})\n"
                "r=c.getresponse(); print(r.status, json.loads(r.read())['run_id'])\n")
        r = self.box.exec(["python3", "-c", code], timeout=60)
        self.assertEqual(r.stdout.split(), ["200", "live"], r.stderr)

    # ---------- Review-Befunde (PR #239) ----------
    def test_a_python3_planted_in_the_home_cannot_forge_the_probe(self):
        """M1: Der PATH des Agenten beginnt mit dem beschreibbaren ~/.local/bin; ein dortiges python3 sagte „alles ok“."""
        home = self.root / "home"
        fake = home / ".local" / "bin" / "python3"
        fake.parent.mkdir(parents=True)
        checks = {n: {"ok": True, "detail": "gefaelscht"} for n in sbx.REQUIRED_CHECKS}
        fake.write_text("#!/bin/sh\necho forged > \"$HOME/fake-ran\"\n"
                        f"echo '{json.dumps({'probe_version': 1, 'uid': 1000, 'checks': checks})}'\n")
        fake.chmod(0o755)
        # Beleg, dass der Angriff gegen den PATH des Agenten wirkt: dort läuft das falsche python3
        r = self.box.exec(["python3", "-c", "print('echt')"])
        self.assertIn("gefaelscht", r.stdout, "das falsche python3 antwortet statt des echten")
        self.assertTrue((home / "fake-ran").exists())
        (home / "fake-ran").unlink()
        report = self.box.probe()
        self.assertFalse((home / "fake-ran").exists(), "die Probe hat das falsche python3 gestartet")
        self.assertTrue(report["ok"], report)
        self.assertNotEqual(report["checks"]["dns"]["detail"], "gefaelscht")

    ATTACK = r'''
import subprocess, sys, time
child = r"""
import os, signal, socket, time
for pid in filter(str.isdigit, os.listdir("/proc")):
    try:
        cmd = open("/proc/%s/cmdline" % pid, "rb").read().decode(errors="replace")
    except OSError:
        continue
    needle = "TCP-LISTEN:" + "1831" + "7"  # nicht zusammenhängend im eigenen Text, sonst träfe es diesen Prozess
    if needle in cmd and int(pid) != os.getpid():
        try:
            os.kill(int(pid), signal.SIGKILL)
        except OSError:
            pass
time.sleep(0.5)
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", 18317))
s.listen(16)
while True:
    c, _ = s.accept()
    data = c.recv(65536).decode(errors="replace")
    open(os.environ["HOME"] + "/fake-answered", "a").write("x")
    if "falsches-kennzeichen" in data:
        status, body = 401, "{}"
    elif data.startswith("GET /_bench/ping"):
        status, body = 200, '{"proxy": "gefaelscht", "run_id": "live", "requests": 0, "tokens": 0}'
    else:
        status, body = 403, "{}"
    c.sendall(("HTTP/1.0 %d x\r\ncontent-length: %d\r\n\r\n%s" % (status, len(body), body)).encode())
    c.close()
"""
subprocess.Popen([sys.executable, "-c", child], start_new_session=True, stdin=subprocess.DEVNULL,
                 stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
time.sleep(2)
'''

    def test_a_background_process_of_the_session_cannot_answer_the_after_probe(self):
        """M1: Ein mit setsid gestarteter Prozess überlebt das exec und besetzt Port 18317 für die Nachher-Probe."""
        marker = self.root / "home" / "fake-answered"
        res, log = self.box.guarded(["python3", "-c", self.ATTACK], self.root / "isolation.json", 60)
        self.assertEqual(res.rc, 0, res.stderr)
        self.assertFalse(marker.exists(), "der Hintergrundprozess der Sitzung hat die Nachher-Probe beantwortet")
        doc = json.loads((self.root / "isolation.json").read_text())
        self.assertTrue(doc["ok"], doc)
        self.assertEqual(sorted(doc["phases"]), ["after", "before"])
        self.assertFalse(self.box.started)

    def test_bench_probe_run_writes_isolation_json(self):
        if self.kind == "bwrap" and not shutil.which("claude"):
            self.skipTest("claude nicht im PATH (bwrap nutzt den des Hosts)")
        with mock.patch.object(bench, "SANDBOX_KIND", self.kind), mock.patch.dict(os.environ, {"BENCH_KEEP_TEST": "1"}):
            rc = bench.probe_run(keep=True)
        self.assertEqual(rc, 0)
        logs = sorted(Path(bench.RESULTS).glob("probe-*/isolation.json"))
        self.assertTrue(logs)
        doc = json.loads(logs[-1].read_text())
        self.assertTrue(doc["ok"])
        self.assertTrue(all(rep["ok"] and set(sbx.REQUIRED_CHECKS) <= set(rep["checks"]) for rep in doc["phases"].values()))


class ChainLiveMixin:
    """Schritte des Harness (post_session, Bewertung) gegen eine echte Sandbox."""

    def make_chain(self):
        c = bench.Chain("vanilla", 0, cid="lv-" + secrets.token_hex(3))
        c.sandbox_kind = self.kind
        for d in (c.home / ".claude", c.work, c.root / "tmp", c.root / "gocache", c.sock, c.out):
            d.mkdir(parents=True, exist_ok=True)
        self.addCleanup(shutil.rmtree, c.root, True)
        self.addCleanup(shutil.rmtree, c.out, True)
        patcher = mock.patch.object(bench, "AGENT", "/usr/bin/true")  # bwrap hängt den claude des Hosts ein
        patcher.start()
        self.addCleanup(patcher.stop)
        return c

    def plant_fsmonitor(self, c):
        """Der Agent trägt in die Konfiguration seines Repos Befehle ein, die git bei add und commit ausführen würde:
        core.fsmonitor, einen clean-Filter (per .gitattributes) und ein gpg.program."""
        subprocess.run(["git", "init", "-q", str(c.work)], check=True, capture_output=True)
        home = sbx.SANDBOX_HOME
        for name, marker in (("fsmon.sh", "fsmon-ran"), ("filter.sh", "filter-ran"), ("gpg.sh", "gpg-ran")):
            hook = c.home / name
            hook.write_text(f"#!/bin/sh\necho ran >> {home}/{marker}\ncat\nexit 0\n")
            hook.chmod(0o755)
        for key, value in (("core.fsmonitor", f"{home}/fsmon.sh"), ("filter.leak.clean", f"{home}/filter.sh"),
                           ("filter.leak.smudge", "cat"), ("commit.gpgsign", "true"), ("gpg.program", f"{home}/gpg.sh")):
            subprocess.run(["git", "-C", str(c.work), "config", key, value], check=True, capture_output=True)
        (c.work / ".gitattributes").write_text("* filter=leak\n")
        (c.work / "datei.txt").write_text("x")
        return [c.home / m for m in ("fsmon-ran", "filter-ran")]

    def test_post_session_does_not_run_the_fsmonitor_or_filters_of_the_agents_repo(self):
        """M3: `git add -A` und `git rev-parse` in post_session liefen ohne Schutz vor der Konfiguration des Agenten."""
        c = self.make_chain()
        markers = self.plant_fsmonitor(c)
        c.tool(["git", "add", "-A"], tag="control")  # Beleg: ohne Schutz läuft der Befehl des Agenten
        for m in markers:
            self.assertTrue(m.exists(), f"der Angriff wirkt nicht ({m.name}): die Gegenprobe ist sinnlos")
            m.unlink()
        info = c.post_session("F1.a0")
        for m in markers + [c.home / "gpg-ran"]:
            self.assertFalse(m.exists(), f"git hat {m.name} des Agenten ausgeführt")
        self.assertRegex(info["head"], r"^[0-9a-f]{40}$")

    def test_grading_refuses_a_workdir_that_became_a_symlink(self):
        c = self.make_chain()
        secret = self.root / "secret"
        secret.mkdir()
        (secret / "geheim.txt").write_text("nur auf dem Host")
        shutil.rmtree(c.work)
        c.work.symlink_to(secret)
        with self.assertRaises(sbx.IsolationError):
            bench.SandboxGradeRun(c, c.work).go(["cat", f"{bench.GRADE_DIR}/geheim.txt"], 30)


@unittest.skipUnless(bwrap_usable(), "bubblewrap nicht nutzbar")
class BwrapLiveTest(ChainLiveMixin, LiveMixin, unittest.TestCase):
    kind = "bwrap"

    def test_user_namespaces_and_dangerous_syscalls_are_closed(self):
        """m5/m6: Ein Agent in bwrap kann keinen neuen User-Namensraum anlegen; der Kernel filtert (Seccomp: 2)."""
        code = ("import ctypes\n"
                "libc = ctypes.CDLL(None, use_errno=True)\n"
                "print('unshare', libc.unshare(0x10000000), ctypes.get_errno())\n"
                "print('mount', libc.mount(b'none', b'/tmp', b'tmpfs', 0, None), ctypes.get_errno())\n"
                "print([l for l in open('/proc/self/status') if l.startswith('Seccomp:')][0].split()[1])\n")
        r = self.box.exec(["python3", "-c", code])
        lines = r.stdout.split("\n")
        self.assertEqual(lines[0].split()[:2], ["unshare", "-1"], r.stdout + r.stderr)
        self.assertEqual(lines[1].split()[:2], ["mount", "-1"], r.stdout)
        self.assertEqual(lines[2], "2", "Seccomp-Filter fehlt")

    def test_the_filter_does_not_get_in_the_way_of_ordinary_tools(self):
        r = self.box.exec(["sh", "-c", "git --version && python3 -c 'import threading,subprocess;print(1)' && ls / >/dev/null && echo fertig"])
        self.assertTrue(r.stdout.endswith("fertig\n"), r.stdout + r.stderr)


@unittest.skipUnless(sbx.docker_ready(), "Docker antwortet nicht oder das Image fehlt (image/build.sh, BENCH_IMAGE)")
class ContainerLiveTest(ChainLiveMixin, LiveMixin, unittest.TestCase):
    kind = "container"

    def test_the_attack_does_forge_a_probe_that_stays_in_the_dead_sessions_container(self):
        """Gegenprobe zu M1: Wäre die Nachher-Probe im selben Container gelaufen, hätte der Hintergrundprozess sie beantwortet."""
        marker = self.root / "home" / "fake-answered"
        self.box.exec(["python3", "-c", self.ATTACK], timeout=60)
        report = self.box.probe()
        self.assertTrue(marker.exists(), "der Angriff wirkt nicht: der Test beweist nichts")
        self.assertTrue(report["ok"], "ohne frischen Container wäre die gefälschte Antwort durchgegangen")

    def test_tampered_start_with_network_is_refused_before_the_agent(self):
        class WithNetwork(sbx.ContainerSandbox):
            def run_args(self):
                args = super().run_args()
                args[args.index("--network") + 1] = "bridge"
                return args

        box = self.make_box(WithNetwork)
        self.addCleanup(box.stop)
        with self.assertRaisesRegex(sbx.IsolationError, "NetworkMode"):
            box.start()
        left = subprocess.run(["docker", "ps", "-aq", "--filter", f"name={box.name}"], capture_output=True, text=True)
        self.assertEqual(left.stdout.strip(), "")  # der Container mit Netz ist nach dem Abbruch entfernt

    def test_tampered_start_with_writable_root_is_refused(self):
        class WritableRoot(sbx.ContainerSandbox):
            def run_args(self):
                args = super().run_args()
                args.remove("--read-only")
                return args

        box = self.make_box(WritableRoot)
        self.addCleanup(box.stop)
        with self.assertRaises(sbx.IsolationError):
            box.start()

    def test_container_is_removed_by_label_not_by_pattern(self):
        self.assertTrue(self.box.name)
        removed = sbx.remove_containers("live")
        self.assertGreaterEqual(len(removed), 1)  # die Kennung „live“ teilen sich auch parallele Läufe dieser Tests
        left = subprocess.run(["docker", "ps", "-aq", "--filter", f"name={self.box.name}"], capture_output=True, text=True)
        self.assertEqual(left.stdout.strip(), "")


if __name__ == "__main__":
    unittest.main()
