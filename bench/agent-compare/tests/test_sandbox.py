"""Tests für Sandbox, Isolationsprotokoll und den Abbruch ohne Isolation.

Alles hier läuft ohne Docker und ohne bubblewrap: geprüft werden die Beschreibung der Sandbox, die
Auswertung der Probe und der Ablauf „Probe, Agent, Probe“ mit einer Schein-Sandbox. Die Tests, die einen
echten Container oder bwrap brauchen, stehen in test_sandbox_live.py und sind dort mit skip markiert.
"""
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

HARNESS = Path(__file__).resolve().parent.parent / "harness"
sys.path.insert(0, str(HARNESS))
_TMP = tempfile.mkdtemp(prefix="bench-test-")
os.environ["BENCH_BASE"] = os.path.join(_TMP, "base")
os.environ["BENCH_RUNS"] = os.path.join(_TMP, "runs")
os.environ.pop("BENCH_SANDBOX", None)
import bench  # noqa: E402
import sandbox as sbx  # noqa: E402

HOST_HOME = "/home/someone"
ROOT = os.path.join(_TMP, "runs")


def make_spec(**kw):
    spec = dict(
        run_id="t1", host_home=HOST_HOME, allowed_roots=[ROOT],
        mounts=[sbx.Mount(f"{ROOT}/t1/home", sbx.SANDBOX_HOME, True), sbx.Mount(f"{ROOT}/t1/sock", sbx.RUN_DIR)],
        env={"PATH": "/usr/bin", "HOME": sbx.SANDBOX_HOME, sbx.TOKEN_ENV: "laufkennzeichen"},
        forwards=[(sbx.PROXY_PORT, f"{sbx.RUN_DIR}/model.sock")], uid=1000, gid=1000)
    spec.update(kw)
    return sbx.SandboxSpec(**spec)


def good_probe(spec, **override):
    names = list(sbx.REQUIRED_CHECKS) + (["git"] if spec.git else [])
    checks = {n: {"ok": True, "detail": "ok"} for n in names}
    checks.update(override)
    return json.dumps({"probe_version": 1, "uid": 1000, "checks": checks})


class VerifySpecTest(unittest.TestCase):
    def test_a_correct_spec_has_no_violations(self):
        self.assertEqual(sbx.verify_spec(make_spec()), [])

    def test_network_must_be_off(self):
        bad = sbx.verify_spec(make_spec(network="bridge"))
        self.assertTrue(any("Netz" in b for b in bad), bad)

    def test_host_home_mounted_is_refused(self):
        spec = make_spec(mounts=[sbx.Mount(HOST_HOME, "/work", True)])
        self.assertTrue(any("HOME des Hosts" in b for b in sbx.verify_spec(spec)), sbx.verify_spec(spec))

    def test_ancestor_of_host_home_is_refused(self):
        spec = make_spec(mounts=[sbx.Mount("/home", "/h")], allowed_roots=["/home"])
        self.assertTrue(any("HOME des Hosts" in b for b in sbx.verify_spec(spec)))

    def test_mount_at_host_home_path_inside_is_refused(self):
        spec = make_spec(mounts=[sbx.Mount(f"{ROOT}/t1/home", HOST_HOME + "/x", True)])
        self.assertTrue(any("HOME des Hosts" in b for b in sbx.verify_spec(spec)))

    def test_source_outside_allowed_roots_is_refused(self):
        spec = make_spec(mounts=[sbx.Mount("/etc", "/hostetc")])
        self.assertTrue(any("erlaubten Wurzeln" in b for b in sbx.verify_spec(spec)))

    def test_docker_socket_and_root_are_refused(self):
        for src in ("/var/run/docker.sock", "/"):
            spec = make_spec(mounts=[sbx.Mount(src, "/x")], allowed_roots=[src])
            self.assertTrue(sbx.verify_spec(spec), src)

    def test_environment_names_are_allowlisted_and_secret_names_refused(self):
        for name in ("ANTHROPIC_API_KEY", "GITHUB_TOKEN", "SSH_AUTH_SOCK", "AWS_SECRET_ACCESS_KEY", "FOO"):
            spec = make_spec(env={"PATH": "/usr/bin", name: "x"})
            self.assertTrue(any(name in b for b in sbx.verify_spec(spec)), name)

    def test_privilege_flags_cannot_be_relaxed(self):
        for kw in ({"read_only_root": False}, {"cap_drop_all": False}, {"no_new_privileges": False}, {"uid": 0}):
            self.assertTrue(sbx.verify_spec(make_spec(**kw)), kw)

    def test_forward_socket_must_live_under_run_dir(self):
        self.assertTrue(sbx.verify_spec(make_spec(forwards=[(1, "/etc/passwd")])))


class ProbeEvaluationTest(unittest.TestCase):
    def setUp(self):
        self.spec = make_spec()

    def test_all_required_checks_ok(self):
        self.assertTrue(sbx.evaluate_probe(good_probe(self.spec), self.spec)["ok"])

    def test_a_failed_check_fails_the_probe(self):
        rep = sbx.evaluate_probe(good_probe(self.spec, net_public={"ok": False, "detail": "erreicht"}), self.spec)
        self.assertFalse(rep["ok"])
        self.assertEqual(rep["failed"], ["net_public"])

    def test_a_missing_check_fails_the_probe(self):
        data = json.loads(good_probe(self.spec))
        del data["checks"]["dns"]
        rep = sbx.evaluate_probe(json.dumps(data), self.spec)
        self.assertEqual((rep["ok"], rep["failed"]), (False, ["dns"]))

    def test_ok_must_be_literally_true(self):
        rep = sbx.evaluate_probe(good_probe(self.spec, dns={"ok": "ja"}), self.spec)
        self.assertFalse(rep["ok"])

    def test_unreadable_output_fails_closed(self):
        for raw in ("", "kein json", "{}", '{"checks": 3}'):
            self.assertFalse(sbx.evaluate_probe(raw, self.spec)["ok"], raw)

    def test_git_check_is_required_when_configured(self):
        spec = make_spec(git={"repo": "/x", "absent_objects": ["a" * 40]})
        data = json.loads(good_probe(spec))
        del data["checks"]["git"]
        self.assertEqual(sbx.evaluate_probe(json.dumps(data), spec)["failed"], ["git"])


class FakeSandbox(sbx.Sandbox):
    """Schein-Sandbox: zählt, was ausgeführt wird, und liefert die Probe aus einer Liste."""
    kind = "fake"

    def __init__(self, spec, in_dir, probes):
        super().__init__(spec, in_dir)
        self.probes, self.agent_runs = list(probes), []

    def _start(self):
        pass

    def exec(self, argv, env=None, cwd=None, timeout=900):
        if argv[0] == "python3":
            return sbx.ExecResult(0, self.probes.pop(0), "")
        self.agent_runs.append(argv)
        return sbx.ExecResult(0, '{"result": "fertig"}', "")


class GuardedSessionTest(unittest.TestCase):
    def setUp(self):
        self.dir = Path(tempfile.mkdtemp(prefix="guard-", dir=_TMP))
        self.spec = make_spec()

    def box(self, probes):
        b = FakeSandbox(self.spec, self.dir / "in", probes)
        b.started = True
        return b

    def test_failed_probe_before_means_the_agent_never_runs(self):
        bad = good_probe(self.spec, net_public={"ok": False, "detail": "erreicht"})
        box = self.box([bad])
        with self.assertRaises(sbx.IsolationError):
            box.guarded(["claude", "--dangerously-skip-permissions"], self.dir / "i.json", 10)
        self.assertEqual(box.agent_runs, [])
        doc = json.loads((self.dir / "i.json").read_text())
        self.assertFalse(doc["ok"])
        self.assertEqual(list(doc["phases"]), ["before"])

    def test_failed_probe_after_raises_after_the_protocol_is_written(self):
        box = self.box([good_probe(self.spec), good_probe(self.spec, mounts={"ok": False, "detail": "neu"})])
        with self.assertRaises(sbx.IsolationError):
            box.guarded(["claude"], self.dir / "i.json", 10)
        doc = json.loads((self.dir / "i.json").read_text())
        self.assertEqual((doc["ok"], sorted(doc["phases"])), (False, ["after", "before"]))

    def test_passing_probes_write_a_protocol_with_hash(self):
        box = self.box([good_probe(self.spec), good_probe(self.spec)])
        result, log = box.guarded(["claude"], self.dir / "i.json", 10)
        self.assertEqual(result.rc, 0)
        doc = json.loads((self.dir / "i.json").read_text())
        self.assertTrue(doc["ok"])
        self.assertEqual(doc["sandbox"], "fake")
        self.assertEqual(len(doc["spec_sha256"]), 64)
        self.assertEqual(len(log.sha256()), 64)

    def test_unstarted_sandbox_does_not_execute(self):
        box = FakeSandbox(self.spec, self.dir / "in", [])
        with self.assertRaises(sbx.IsolationError):
            sbx.Sandbox.exec(box, ["id"])

    def test_start_refuses_a_bad_spec_before_anything_starts(self):
        class Boom(FakeSandbox):
            def _start(self):
                raise AssertionError("darf nicht starten")

        for bad in (make_spec(network="bridge"), make_spec(mounts=[sbx.Mount(HOST_HOME, "/h", True)])):
            with self.assertRaises(sbx.IsolationError):
                Boom(bad, self.dir / "in2", []).start()


class BackendDescriptionTest(unittest.TestCase):
    def setUp(self):
        self.dir = Path(tempfile.mkdtemp(prefix="desc-", dir=_TMP))

    def test_no_sandbox_kind_exists_without_isolation(self):
        for kind in ("", None, "none", "host", "off", "docker "[:-1] + "x"):
            with self.assertRaises(sbx.IsolationError, msg=repr(kind)):
                sbx.make_sandbox(kind, make_spec(), self.dir)

    def test_both_ways_sit_behind_one_interface(self):
        for kind in ("container", "bwrap"):
            box = sbx.make_sandbox(kind, make_spec(), self.dir / kind)
            self.assertIsInstance(box, sbx.Sandbox)
            self.assertEqual(box.kind, kind)

    def test_container_command_line_has_no_network_and_no_caps(self):
        box = sbx.make_sandbox("container", make_spec(), self.dir / "c", image="img:test")
        args = box.run_args()
        joined = " ".join(args)
        self.assertIn("--network none", joined)
        self.assertIn("--read-only", args)
        self.assertIn("--cap-drop ALL", joined)
        self.assertIn("no-new-privileges", joined)
        self.assertNotIn("-v", args)  # --mount scheitert bei fehlender Quelle, -v legt root-eigene Verzeichnisse an (#4251)
        self.assertNotIn("--privileged", args)
        self.assertNotIn(HOST_HOME, joined)
        mounts = [a for a in args if a.startswith("type=bind")]
        self.assertTrue(all(m.endswith(",readonly") or "dst=" + sbx.SANDBOX_HOME in m for m in mounts), mounts)

    def test_container_runtime_check_catches_a_tampered_start(self):
        box = sbx.make_sandbox("container", make_spec(), self.dir / "c", image="img:test")
        want = {m.dst: m for m in box.all_mounts()}
        raw = {"State": {"Running": True},
               "HostConfig": {"NetworkMode": "none", "ReadonlyRootfs": True, "CapDrop": ["ALL"], "CapAdd": None,
                              "SecurityOpt": ["no-new-privileges"], "Privileged": False, "Devices": [], "PortBindings": {},
                              "PidMode": "", "IpcMode": "private", "UsernsMode": "", "Memory": 8 << 30, "PidsLimit": 2048},
               "Config": {"User": "1000:1000", "Env": ["PATH=/usr/bin"]},
               "Mounts": [{"Type": "bind", "Source": m.src, "Destination": m.dst, "RW": m.rw} for m in want.values()]}
        self.assertEqual(box.verify_runtime({"raw": raw}), [])
        for path, value in (("HostConfig.NetworkMode", "bridge"), ("HostConfig.ReadonlyRootfs", False),
                            ("HostConfig.CapDrop", []), ("HostConfig.Privileged", True), ("Config.User", "0:0"),
                            ("Config.Env", ["PATH=/usr/bin", "ANTHROPIC_API_KEY=x"])):
            tampered = json.loads(json.dumps(raw))
            node, key = path.split(".")
            tampered[node][key] = value
            self.assertTrue(box.verify_runtime({"raw": tampered}), path)
        extra = json.loads(json.dumps(raw))
        extra["Mounts"].append({"Type": "bind", "Source": HOST_HOME, "Destination": "/h", "RW": True})
        self.assertTrue(box.verify_runtime({"raw": extra}))

    def test_bwrap_arguments_unshare_everything_and_clear_the_environment(self):
        box = sbx.make_sandbox("bwrap", make_spec(), self.dir / "b")
        args = box.bwrap_args("/")
        for flag in ("--unshare-all", "--die-with-parent", "--clearenv", "--remount-ro"):
            self.assertIn(flag, args)
        self.assertNotIn("--share-net", args)
        self.assertEqual(box.verify_runtime({}), [])
        self.assertEqual(box.launcher_env(), {"PATH": "/usr/bin:/bin"})  # bwrap selbst erbt nichts vom Harness

    def test_spec_hash_changes_with_the_mounts(self):
        a = sbx.make_sandbox("bwrap", make_spec(), self.dir / "a").fingerprint
        b = sbx.make_sandbox("bwrap", make_spec(mounts=[sbx.Mount(f"{ROOT}/t1/home", sbx.SANDBOX_HOME, True)]),
                             self.dir / "b").fingerprint
        self.assertNotEqual(a, b)


class HarnessWithoutIsolationTest(unittest.TestCase):
    """bench.py startet ohne nachgewiesene Isolation keine Sitzung und keinen Prozess."""

    def forbid_processes(self):
        def boom(*a, **k):
            raise AssertionError(f"Es darf kein Prozess entstehen: {a[:1]}")

        for target in ("subprocess.run", "subprocess.Popen"):
            p = mock.patch(target, boom)
            p.start()
            self.addCleanup(p.stop)

    def chain(self, kind):
        c = bench.Chain("vanilla", 1, cid="g-" + os.urandom(3).hex())
        c.sandbox_kind = kind
        return c

    def test_run_claude_aborts_when_no_sandbox_is_chosen(self):
        for kind in ("", "none", "host"):
            c = self.chain(kind)
            self.forbid_processes()
            with self.assertRaises(sbx.IsolationError):
                c.run_claude("Aufgabe", "F1.a0")
            mock.patch.stopall()

    def test_run_claude_aborts_when_the_probe_fails_and_runs_no_agent(self):
        c = self.chain("container")
        spec_holder = {}

        class FailingBox(FakeSandbox):
            def _start(self):
                spec_holder["started"] = True

        def new_sandbox(tag, **kw):
            return FailingBox(make_spec(), c.root / "in" / tag,
                              [good_probe(make_spec(), host_paths={"ok": False, "detail": "HOME des Hosts sichtbar"})])

        c.out.mkdir(parents=True, exist_ok=True)
        c.new_sandbox = new_sandbox
        box_runs = []
        orig = FailingBox.exec

        def spy(self, argv, *a, **k):
            if argv[0] != "python3":
                box_runs.append(argv)
            return orig(self, argv, *a, **k)

        with mock.patch.object(FailingBox, "exec", spy), self.assertRaises(sbx.IsolationError):
            c.run_claude("Aufgabe", "F1.a0")
        self.assertEqual(box_runs, [])  # --dangerously-skip-permissions wurde nie ausgeführt
        doc = json.loads((c.out / "F1.a0.isolation.json").read_text())
        self.assertFalse(doc["ok"])

    def test_precheck_refuses_a_missing_container_runtime(self):
        with mock.patch.object(bench, "SANDBOX_KIND", "container"), mock.patch.object(sbx, "docker_ready", return_value=False):
            with self.assertRaises(sbx.IsolationError):
                bench.precheck()

    def test_precheck_refuses_unknown_kinds(self):
        for kind in ("", "none", "off"):
            with mock.patch.object(bench, "SANDBOX_KIND", kind), self.assertRaises(sbx.IsolationError):
                bench.precheck()

    def test_claudemem_arm_is_not_available_in_the_sandbox(self):
        c = bench.Chain("claudemem", 1, cid="g-" + os.urandom(3).hex())
        self.forbid_processes()
        with self.assertRaises(sbx.IsolationError):
            c.prepare()

    def test_no_host_command_in_bench_touches_agent_files(self):
        """git, ctx und go test laufen nur über die Sandbox: bench.py ruft sie nicht direkt per subprocess auf."""
        src = (HARNESS / "bench.py").read_text()
        for needle in ('sh(["git"', 'sh([ctx, "setup"', '"go", "test"], env='):
            self.assertNotIn(needle, src)
        self.assertEqual(src.count('"--dangerously-skip-permissions"'), 1, "das Flag steht genau einmal, in run_claude")


class ChainEnvironmentTest(unittest.TestCase):
    def test_agent_environment_has_only_allowed_names_and_no_inherited_secrets(self):
        with mock.patch.dict(os.environ, {"ANTHROPIC_API_KEY": "nur-im-test", "SSH_AUTH_SOCK": "/x", "GITHUB_TOKEN": "t",
                                          "BENCH_UPSTREAM_API_KEY": "k"}):
            for arm in ("vanilla", "native", "ghosttree"):
                env = bench.Chain(arm, 1, cid="e-" + arm).env()
                self.assertLessEqual(set(env), sbx.ALLOWED_ENV_NAMES, arm)
                self.assertNotIn("nur-im-test", env.values())
                self.assertNotIn("ANTHROPIC_API_KEY", env)
                self.assertEqual(env["HOME"], sbx.SANDBOX_HOME)

    def test_agent_gets_a_run_token_not_a_key(self):
        c = bench.Chain("vanilla", 1, cid="e-token")
        env = c.env()
        self.assertEqual(env[sbx.TOKEN_ENV], c.token)
        self.assertTrue(env["ANTHROPIC_BASE_URL"].startswith("http://127.0.0.1:"))

    def test_grading_sandbox_has_no_model_access(self):
        c = bench.Chain("vanilla", 1, cid="e-grade")
        box = c.new_sandbox("grade", model=False)
        self.assertNotIn("ANTHROPIC_BASE_URL", box.spec.env)
        self.assertNotIn(sbx.TOKEN_ENV, box.spec.env)
        self.assertEqual(box.spec.forwards, [])
        self.assertNotIn(sbx.RUN_DIR, [m.dst for m in box.spec.mounts])


class SafeReadTest(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp(prefix="safe-", dir=_TMP))
        self.outside = Path(tempfile.mkdtemp(prefix="outside-", dir=_TMP))
        (self.outside / "geheim.txt").write_text("nicht lesen")
        (self.root / "ok.txt").write_text("lesen")

    def test_regular_file_is_read(self):
        self.assertEqual(bench.safe_text(self.root / "ok.txt", self.root), "lesen")

    def test_symlink_to_a_file_outside_is_not_followed(self):
        (self.root / "link.txt").symlink_to(self.outside / "geheim.txt")
        self.assertIsNone(bench.safe_file(self.root / "link.txt", self.root))
        self.assertEqual(bench.safe_text(self.root / "link.txt", self.root, "x"), "x")

    def test_symlinked_directory_is_not_followed(self):
        (self.root / "dir").symlink_to(self.outside)
        self.assertIsNone(bench.safe_file(self.root / "dir" / "geheim.txt", self.root))
        self.assertEqual(bench.safe_glob(self.root, "dir/*.txt"), [])

    def test_path_outside_the_root_is_refused(self):
        self.assertIsNone(bench.safe_file(self.outside / "geheim.txt", self.root))

    def test_fifo_is_not_a_regular_file(self):
        os.mkfifo(self.root / "pipe")
        self.assertIsNone(bench.safe_file(self.root / "pipe", self.root))

    def test_walk_reports_symlinks_without_following_them(self):
        (self.root / "CLAUDE.md").symlink_to(self.outside / "geheim.txt")
        (self.root / "sub").mkdir()
        (self.root / "sub" / "AGENTS.md").write_text("x")
        found = {p.name: link for p, link in bench.safe_walk(self.root)}
        self.assertEqual(found.get("CLAUDE.md"), True)
        self.assertEqual(found.get("AGENTS.md"), False)


class HostCleanupSymlinkTest(unittest.TestCase):
    """Der Agent kann im HOME Symlinks auf Pfade des Hosts anlegen (nur Text). Die Nachbereitung auf dem Host
    darf ihnen nicht folgen: nichts außerhalb des HOME wird gelöscht, überschrieben oder gelesen."""

    def setUp(self):
        import tarfile  # noqa: F401
        self.c = bench.Chain("vanilla", 1, cid="s-" + os.urandom(3).hex())
        self.home = self.c.home
        (self.home / ".claude").mkdir(parents=True)
        self.work = self.c.work
        self.work.mkdir(parents=True)
        self.outside = Path(tempfile.mkdtemp(prefix="outside-", dir=_TMP))
        (self.outside / "wichtig.txt").write_text("unberührt")
        self.c.out.mkdir(parents=True, exist_ok=True)

        class R:
            stdout, stderr, rc = "abc123\n", "", 0

        patcher = mock.patch.object(bench.Chain, "tool", return_value=[R(), R(), R()])
        patcher.start()
        self.addCleanup(patcher.stop)

    def assert_untouched(self):
        self.assertEqual((self.outside / "wichtig.txt").read_text(), "unberührt")
        self.assertEqual(sorted(p.name for p in self.outside.iterdir()), ["wichtig.txt"])

    def test_symlinked_claude_directory_aborts_the_chain(self):
        (self.home / ".claude").rmdir()
        (self.home / ".claude").symlink_to(self.outside)
        with self.assertRaises(sbx.IsolationError):
            self.c.post_session("F1.a0")
        self.assert_untouched()

    def test_symlinked_src_directory_aborts_the_chain(self):
        for p in (self.work, self.home / "src"):
            if p.exists():
                (p.rmdir() if p.is_dir() else p.unlink())
        (self.home / "src").symlink_to(self.outside)
        (self.outside / "CLAUDE.md").write_text("fremd")
        with self.assertRaises(sbx.IsolationError):
            self.c.post_session("F1.a0")
        self.assertEqual((self.outside / "CLAUDE.md").read_text(), "fremd")

    def test_symlinked_project_directory_aborts_the_chain(self):
        (self.home / ".claude" / "projects").mkdir()
        (self.home / ".claude" / "projects" / "x").symlink_to(self.outside)
        with self.assertRaises(sbx.IsolationError):
            self.c.post_session("F1.a0")
        self.assert_untouched()

    def test_symlinked_memory_directory_is_unlinked_not_followed(self):
        proj = self.home / ".claude" / "projects" / "x"
        proj.mkdir(parents=True)
        (self.outside / "MEMORY.md").write_text("fremd")
        (proj / "memory").symlink_to(self.outside)
        self.c.post_session("F1.a0")
        self.assertFalse((proj / "memory").exists() or (proj / "memory").is_symlink())
        self.assertEqual((self.outside / "MEMORY.md").read_text(), "fremd")
        self.assertEqual((self.outside / "wichtig.txt").read_text(), "unberührt")

    def test_symlinked_claude_md_is_unlinked_and_baseline_written_as_a_regular_file(self):
        (self.outside / "CLAUDE.md").write_text("fremd")
        (self.home / ".claude" / "CLAUDE.md").symlink_to(self.outside / "CLAUDE.md")
        self.c.meta["baseline_user_claude_md"] = "Installationsstand"
        self.c.post_session("F1.a0")
        mine = self.home / ".claude" / "CLAUDE.md"
        self.assertFalse(mine.is_symlink())
        self.assertEqual(mine.read_text(), "Installationsstand")
        self.assertEqual((self.outside / "CLAUDE.md").read_text(), "fremd")

    def test_symlinked_memory_file_in_the_work_tree_is_unlinked_not_followed(self):
        (self.outside / "AGENTS.md").write_text("fremd")
        (self.work / "AGENTS.md").symlink_to(self.outside / "AGENTS.md")
        (self.work / "CLAUDE.md").write_text("gedächtnis")
        self.c.post_session("F1.a0")
        self.assertFalse((self.work / "AGENTS.md").is_symlink())
        self.assertFalse((self.work / "CLAUDE.md").exists())
        self.assertEqual((self.outside / "AGENTS.md").read_text(), "fremd")
        self.assertFalse((self.c.out / "F1.a0.removed-AGENTS.md").exists())  # den Inhalt hinter dem Link liest der Harness nicht

    def test_snapshot_filter_refuses_a_hardlink_to_somewhere_else(self):
        import io
        import tarfile
        buf = io.BytesIO()
        with tarfile.open(fileobj=buf, mode="w") as t:
            info = tarfile.TarInfo("home/h")
            info.type = tarfile.LNKTYPE
            info.linkname = "../../etc/passwd"
            t.addfile(info)
        buf.seek(0)
        dest = Path(tempfile.mkdtemp(prefix="extract-", dir=_TMP))
        with tarfile.open(fileobj=buf) as t, self.assertRaises(tarfile.TarError):
            t.extractall(dest, filter=bench.snapshot_filter)

    def test_snapshot_filter_does_not_write_through_an_extracted_symlink(self):
        import io
        import tarfile
        buf = io.BytesIO()
        with tarfile.open(fileobj=buf, mode="w") as t:
            link = tarfile.TarInfo("home/x")
            link.type = tarfile.SYMTYPE
            link.linkname = str(self.outside)
            t.addfile(link)
            data = b"ueberschrieben"
            f = tarfile.TarInfo("home/x/wichtig.txt")
            f.size = len(data)
            t.addfile(f, io.BytesIO(data))
        buf.seek(0)
        dest = Path(tempfile.mkdtemp(prefix="extract-", dir=_TMP))
        with tarfile.open(fileobj=buf) as t, self.assertRaises(tarfile.TarError):
            t.extractall(dest, filter=bench.snapshot_filter)
        self.assert_untouched()

    def test_regrade_patch_is_not_written_into_the_agent_home(self):
        src = (HARNESS / "bench.py").read_text()
        self.assertNotIn(".regrade.patch", src)
        self.assertIn('mounts=[sbx.Mount(str(pdir), "/bench-patch")]', src)


class ProbeScriptTest(unittest.TestCase):
    def test_probe_script_is_python311_stdlib_only(self):
        src = (HARNESS / "probe.py").read_text()
        compile(src, "probe.py", "exec")
        imports = {line.split()[1].split(".")[0] for line in src.splitlines() if line.startswith(("import ", "from "))}
        self.assertLessEqual(imports, set(sys.stdlib_module_names))

    def test_probe_script_reports_every_required_check(self):
        """Ohne Sandbox-Eigenschaften laufen die Proben trotzdem durch und melden jede geforderte Prüfung (auch als FEHLER)."""
        cfg = {"run_id": "x", "proxy_port": 9, "env_names": [], "forbidden_paths": [], "allowed_home_entries": [],
               "expected_mounts": {}, "implicit_mounts": [], "allowed_secret_like": [], "extra_targets": []}
        path = Path(tempfile.mkdtemp(dir=_TMP)) / "probe.json"
        path.write_text(json.dumps(cfg))
        r = subprocess.run([sys.executable, str(HARNESS / "probe.py"), str(path)], capture_output=True, text=True,
                           timeout=120, env={"PATH": os.environ.get("PATH", "")})
        data = json.loads(r.stdout)
        self.assertLessEqual(set(sbx.REQUIRED_CHECKS), set(data["checks"]))
        # Auf dem Host ist die Sandbox nicht an: mindestens der Proxy und die Wurzel müssen die Probe scheitern lassen
        self.assertFalse(sbx.evaluate_probe(r.stdout, make_spec())["ok"])


if __name__ == "__main__":
    unittest.main()
