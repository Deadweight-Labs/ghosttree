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
        env = {"PATH": "/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin", "HOME": sbx.SANDBOX_HOME, "LANG": "C.UTF-8",
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


@unittest.skipUnless(bwrap_usable(), "bubblewrap nicht nutzbar")
class BwrapLiveTest(LiveMixin, unittest.TestCase):
    kind = "bwrap"


@unittest.skipUnless(sbx.docker_ready(), "Docker antwortet nicht oder das Image fehlt (image/build.sh, BENCH_IMAGE)")
class ContainerLiveTest(LiveMixin, unittest.TestCase):
    kind = "container"

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
        self.assertEqual(len(removed), 1)


if __name__ == "__main__":
    unittest.main()
