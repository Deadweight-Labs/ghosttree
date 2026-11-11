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
    names = list(sbx.REQUIRED_CHECKS)
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

    def test_a_mount_source_that_is_a_symlink_is_refused_even_inside_the_roots(self):
        """M3: Ein Symlink aus der Sandbox (src/ledgerly -> irgendein Verzeichnis) darf nie Quelle einer Einhängung sein,
        auch wenn das Ziel selbst unter einer erlaubten Wurzel liegt oder die Wurzel mit dem Ziel mitwandert."""
        base = Path(tempfile.mkdtemp(prefix="symsrc-", dir=_TMP))
        real, secret = base / "real", base / "secret"
        real.mkdir()
        secret.mkdir()
        (base / "link").symlink_to(secret)
        (base / "inner").symlink_to(real)
        (real / "sub").mkdir()
        for src, roots in ((base / "link", [str(base)]), (base / "link", [str(secret)]), (base / "inner", [str(base)]),
                           (base / "inner" / "sub", [str(base)])):
            spec = make_spec(mounts=[sbx.Mount(str(src), "/grade-src")], allowed_roots=roots)
            bad = sbx.verify_spec(spec)
            self.assertTrue(any("Symlink" in b for b in bad), (src, bad))
        ok = make_spec(mounts=[sbx.Mount(str(real), "/grade-src")], allowed_roots=[str(base)])
        self.assertEqual(sbx.verify_spec(ok), [])

    def test_the_run_id_must_be_a_plain_identifier(self):
        for rid in ("../x", "a b", "", "x;y", "a/b", "-rf"):
            self.assertTrue(any("Kennung" in b for b in sbx.verify_spec(make_spec(run_id=rid))), rid)
        self.assertEqual(sbx.verify_spec(make_spec(run_id="rg-1a2b3c")), [])


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

    def test_the_dead_git_check_is_gone(self):
        """n6: Das Feld git der Sandbox-Beschreibung wurde nie belegt; die Probe führt keinen git-Befehl aus."""
        self.assertNotIn("git", sbx.SandboxSpec.__dataclass_fields__)
        self.assertNotIn("git", sbx.REQUIRED_CHECKS)
        self.assertNotIn("git_state", (HARNESS / "probe.py").read_text())


class FakeSandbox(sbx.Sandbox):
    """Schein-Sandbox: zählt, was ausgeführt wird, und liefert die Probe aus einer Liste."""
    kind = "fake"

    def __init__(self, spec, in_dir, probes):
        super().__init__(spec, in_dir)
        self.probes, self.agent_runs = list(probes), []
        self.agent_envs = []
        self.events = []  # (Ereignis, Instanz) in der Reihenfolge des Ablaufs
        self.probe_calls = []

    def _start(self):
        self.events.append(("start", id(self)))

    def stop(self):
        self.events.append(("stop", id(self)))
        super().stop()

    def exec(self, argv, env=None, cwd=None, timeout=900):
        if any(str(a).endswith("probe.py") for a in argv):
            self.events.append(("probe", id(self)))
            self.probe_calls.append((list(argv), env))
            return sbx.ExecResult(0, self.probes.pop(0), "")
        self.events.append(("agent", id(self)))
        self.agent_runs.append(argv)
        self.agent_envs.append(env)
        return sbx.ExecResult(0, '{"result": "fertig"}', "")


class GuardedSessionTest(unittest.TestCase):
    def setUp(self):
        self.dir = Path(tempfile.mkdtemp(prefix="guard-", dir=_TMP))
        self.spec = make_spec(allowed_roots=[ROOT, str(self.dir)])  # die frische Instanz startet wirklich (verify_spec)

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

    def test_the_probe_runs_with_an_absolute_interpreter_and_a_path_without_the_home(self):
        """M1: `env -i PATH=… python3` fand ein python3 im beschreibbaren ~/.local/bin des Agenten."""
        spec = make_spec(env={"PATH": f"{sbx.SANDBOX_HOME}/.local/bin:/usr/bin", "HOME": sbx.SANDBOX_HOME,
                              sbx.TOKEN_ENV: "laufkennzeichen"})
        box = FakeSandbox(spec, self.dir / "in", [good_probe(spec)])
        box.started = True
        box.probe()
        argv, env = box.probe_calls[0]
        self.assertEqual(argv[:3], ["/usr/bin/python3", "-I", "-S"])
        self.assertTrue(os.path.isabs(argv[3]) and argv[3].endswith("probe.py"))
        self.assertIsNotNone(env, "die Probe bekommt eine eigene Umgebung statt der des Agenten")
        for entry in env["PATH"].split(":"):
            self.assertTrue(entry.startswith("/") and not entry.startswith(sbx.SANDBOX_HOME), env["PATH"])
        self.assertEqual(sorted(env), sorted(spec.env), "dieselben Namen wie beim Agenten, die Probe prüft sie")

    def test_the_after_probe_runs_in_a_fresh_instance_once_the_session_is_dead(self):
        """M1: Hintergrundprozesse der Sitzung überleben das exec. Vor der Nachher-Probe wird die Instanz beendet und
        mit denselben Einhängungen neu gestartet."""
        box = self.box([good_probe(self.spec), good_probe(self.spec)])
        box.guarded(["claude"], self.dir / "i.json", 10)
        kinds = [e[0] for e in box.events]
        self.assertEqual(kinds, ["probe", "agent", "stop", "start", "probe", "stop"])
        before, agent, stopped, started, after, _ = box.events
        self.assertEqual(before[1], agent[1])
        self.assertEqual(agent[1], stopped[1], "die Sitzungsinstanz wird beendet")
        self.assertNotEqual(started[1], agent[1], "die Nachher-Probe läuft nicht in der Instanz der Sitzung")
        self.assertEqual(started[1], after[1])
        self.assertFalse(box.started, "nach der Sitzung ist die Instanz nicht mehr benutzbar")

    def test_a_fresh_instance_that_breaks_the_rules_fails_the_session(self):
        class Tampered(FakeSandbox):
            def fresh(self):
                inst = super().fresh()
                inst.spec = make_spec(network="bridge")
                return inst

        box = Tampered(self.spec, self.dir / "in", [good_probe(self.spec), good_probe(self.spec)])
        box.started = True
        with self.assertRaises(sbx.IsolationError):
            box.guarded(["claude"], self.dir / "i.json", 10)

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

    def test_bwrap_closes_user_namespaces_and_carries_a_seccomp_filter(self):
        """m5: Ohne diese beiden bliebe der Kernel-Angriffsraum (neue User-Namensräume, bpf, io_uring …) offen."""
        box = sbx.make_sandbox("bwrap", make_spec(), self.dir / "b")
        with mock.patch.object(sbx, "bwrap_supports", return_value=True):
            args = box.bwrap_args("/", seccomp_fd=7)
            self.assertIn("--disable-userns", args)
            self.assertEqual(args[args.index("--seccomp") + 1], "7")
            self.assertEqual(box.verify_runtime({}), [])
            self.assertEqual(box.facts_summary()["userns_disabled"], True)
            for missing in ("--disable-userns", "--seccomp"):
                tampered = [a for a in box.bwrap_args("/", seccomp_fd=7) if a != missing]
                with mock.patch.object(sbx.BwrapSandbox, "bwrap_args", return_value=tampered):
                    self.assertTrue(any(missing in b for b in box.verify_runtime({})), missing)

    def test_bwrap_without_disable_userns_refuses_to_start(self):
        """Ohne --disable-userns bliebe der User-Namensraum offen: bwrap startet dann nicht (geschlossen bei Fehler)."""
        box = sbx.make_sandbox("bwrap", make_spec(), self.dir / "b")
        with mock.patch.object(sbx, "bwrap_supports", return_value=False), mock.patch.object(sbx.shutil, "which", return_value="/usr/bin/bwrap"):
            with self.assertRaisesRegex(sbx.IsolationError, "disable-userns"):
                box._start()
        with mock.patch.object(sbx, "bwrap_supports", return_value=True), mock.patch.object(sbx.shutil, "which", return_value="/usr/bin/bwrap"):
            box._start()

    @staticmethod
    def run_bpf(prog, arch_id, nr, arg0=0):
        """Kleiner Interpreter für klassisches BPF (nur die Befehle des Filters) über einer seccomp_data-Struktur."""
        import struct
        data = struct.pack("=IIQ6Q", nr, arch_id, 0, arg0, 0, 0, 0, 0, 0)
        insns = [struct.unpack("=HBBI", prog[k:k + 8]) for k in range(0, len(prog), 8)]
        a, pc = 0, 0
        while True:
            code, jt, jf, k = insns[pc]
            pc += 1
            if code == 0x20:
                a = struct.unpack_from("=I", data, k)[0]
            elif code == 0x06:
                return k
            elif code == 0x15:
                pc += jt if a == k else jf
            elif code == 0x35:
                pc += jt if a >= k else jf
            elif code == 0x45:
                pc += jt if a & k else jf
            else:
                raise AssertionError(f"unbekannter Befehl {code:#x}")

    def test_seccomp_filter_decisions(self):
        """Der Filter lässt Gewöhnliches durch, sperrt Namensräume und Co. und prüft clone auf CLONE_NEW*-Bits."""
        ALLOW, KILL, EPERM, ENOSYS = 0x7FFF0000, 0x80000000, 0x00050001, 0x00050026
        for arch, audit, plain in (("x86_64", 0xC000003E, {"read": 0, "execve": 59}), ("aarch64", 0xC00000B7, {"read": 63, "execve": 221})):
            with mock.patch("platform.machine", return_value=arch):
                prog = sbx.seccomp_filter()
            run = lambda nr, arg0=0: self.run_bpf(prog, audit, nr, arg0)  # noqa: E731
            for name, nr in plain.items():
                self.assertEqual(run(nr), ALLOW, (arch, name))
            for name, nr in {**sbx._SECCOMP_COMMON[arch], **sbx._SECCOMP_NEW_API}.items():
                self.assertEqual(run(nr), EPERM, (arch, name))
            clone = sbx._CLONE[arch]
            self.assertEqual(run(clone, 0x00010000 | 0x100), ALLOW, "clone für Threads")
            for flag in (0x10000000, 0x40000000, 0x20000000, 0x00020000, 0x08000000, 0x04000000, 0x02000000):
                self.assertEqual(run(clone, 0x100 | flag), EPERM, (arch, hex(flag)))
            self.assertEqual(run(sbx._CLONE3), ENOSYS)
            self.assertEqual(self.run_bpf(prog, 0x40000003, 0), KILL, "fremde Architektur")
            if arch == "x86_64":
                self.assertEqual(run(0x40000000 | 0), KILL, "x32-Schnittstelle")

    def test_container_runtime_check_refuses_an_unconfined_seccomp_profile(self):
        box = sbx.make_sandbox("container", make_spec(), self.dir / "c", image="img:test")
        raw = {"State": {"Running": True},
               "HostConfig": {"NetworkMode": "none", "ReadonlyRootfs": True, "CapDrop": ["ALL"], "CapAdd": None,
                              "SecurityOpt": ["no-new-privileges", "seccomp=unconfined"], "Privileged": False, "Devices": [],
                              "PortBindings": {}, "PidMode": "", "IpcMode": "private", "UsernsMode": "", "Memory": 8 << 30,
                              "PidsLimit": 2048},
               "Config": {"User": "1000:1000", "Env": ["PATH=/usr/bin"]},
               "Mounts": [{"Type": "bind", "Source": m.src, "Destination": m.dst, "RW": m.rw} for m in box.all_mounts()]}
        self.assertTrue(any("seccomp" in b.lower() for b in box.verify_runtime({"raw": raw})))

    def test_container_cleanup_without_an_id_removes_only_own_orphans(self):
        """n3: `bench.py cleanup` ohne Kennung entfernt keine Container anderer Benutzer und keine laufender Läufe."""
        calls = []

        def fake_run(cmd, **kw):
            calls.append(cmd)

            class R:
                returncode = 0
                stdout = ("aaa 4000000\n"          # Harness-Prozess existiert nicht mehr: verwaist
                          f"bbb {os.getpid()}\n"    # der Harness lebt: nicht anfassen
                          "ccc \n")                 # ohne Angabe: nicht eindeutig, nicht anfassen
                stderr = ""
            return R()

        with mock.patch.object(sbx.subprocess, "run", fake_run), mock.patch.object(sbx, "_pid_alive", lambda p: p == os.getpid()):
            removed = sbx.remove_containers()
        ps = calls[0]
        self.assertIn(f"label=bench.owner={os.getuid()}", ps)
        self.assertEqual(removed, ["aaa"])
        self.assertEqual(calls[1][-1], "aaa")
        calls.clear()
        with mock.patch.object(sbx.subprocess, "run", fake_run):
            sbx.remove_containers("kette1")
        self.assertIn("label=bench.run=kette1", calls[0])
        self.assertIn(f"label=bench.owner={os.getuid()}", calls[0])

    def test_containers_carry_owner_labels(self):
        box = sbx.make_sandbox("container", make_spec(), self.dir / "c", image="img:test")
        joined = " ".join(box.run_args())
        self.assertIn(f"bench.owner={os.getuid()}", joined)
        self.assertIn(f"bench.pid={os.getpid()}", joined)

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
            if not any(str(a).endswith("probe.py") for a in argv):
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


class ChainReviewFixesTest(unittest.TestCase):
    """Befunde des Sicherheitsreviews zu PR #239 (bench.py)."""

    def test_tool_steps_run_with_a_path_the_agent_cannot_write_to(self):
        """M1/Nachlauf: git, go und ctx der Werkzeugschritte dürfen nicht aus ~/.local/bin des Agenten kommen."""
        c = bench.Chain("ghosttree", 1, cid="p-" + os.urandom(3).hex())
        boxes = []

        def new_sandbox(tag, **kw):
            boxes.append(FakeSandbox(make_spec(), c.root / "in" / tag, []))
            return boxes[-1]

        c.new_sandbox = new_sandbox
        c.tool(["git", "status"], tag="t")
        env = boxes[0].agent_envs[0]
        self.assertIsNotNone(env)
        self.assertEqual(env["PATH"], c.trusted_path())
        for entry in env["PATH"].split(":"):
            self.assertFalse(entry.startswith(sbx.SANDBOX_HOME), env["PATH"])
        self.assertIn(bench.CTX_BIN_DIR, env["PATH"].split(":"))
        self.assertTrue(c.env()["PATH"].startswith(f"{sbx.SANDBOX_HOME}/.local/bin"), "der Agent behält seinen PATH")

    def test_every_git_call_on_an_agent_repo_switches_off_fsmonitor_and_hooks(self):
        """M3: `git add -A` lief ohne Schutz: core.fsmonitor aus der Konfiguration des Agenten wurde ausgeführt."""
        src = (HARNESS / "bench.py").read_text()
        import re
        bare = set(re.findall(r'\["git", "([A-Za-z][\w-]*)"', src))
        self.assertLessEqual(bare, {"init", "remote"}, f"git ohne GIT-Präfix: {bare}")
        self.assertIn("core.fsmonitor=false", " ".join(bench.GIT))
        self.assertIn("core.hooksPath=/dev/null", " ".join(bench.GIT))

        c = bench.Chain("vanilla", 1, cid="g-" + os.urandom(3).hex())
        (c.home / ".claude").mkdir(parents=True)
        c.work.mkdir(parents=True)
        c.out.mkdir(parents=True, exist_ok=True)
        calls = []

        class R:
            stdout, stderr, rc = "abc123\n", "", 0

        def record(self, *cmds, **kw):
            calls.extend(cmds)
            return [R() for _ in cmds]

        with mock.patch.object(bench.Chain, "tool", record):
            c.post_session("F1.a0")
        gits = [argv for argv in calls if argv[0] == "git"]
        self.assertTrue(gits)
        for argv in gits:
            for needed in ("core.fsmonitor=false", "core.hooksPath=/dev/null"):
                self.assertIn(needed, argv, argv)
        subs = {next(a for a in argv[1:] if not a.startswith("-") and "=" not in a and a != "/dev/null") for argv in gits}
        self.assertTrue({"add", "rev-parse"} <= subs, subs)

    def test_the_agents_git_config_is_replaced_before_git_runs(self):
        """filter.*.clean, gpg.program, core.pager … aus .git/config des Agenten führen Befehle aus; -c deckt nicht alle ab."""
        c = bench.Chain("vanilla", 1, cid="gc-" + os.urandom(3).hex())
        (c.work / ".git").mkdir(parents=True)
        outside = Path(tempfile.mkdtemp(prefix="outside-", dir=_TMP))
        (outside / "config").write_text("unberührt")
        (c.work / ".git" / "config").write_text('[filter "x"]\n\tclean = touch /home/agent/leak\n[core]\n\tpager = evil\n')
        (c.work / ".git" / "commondir").write_text(str(outside))
        bench.reset_git_config(c.work)
        text = (c.work / ".git" / "config").read_text()
        self.assertEqual(text, bench.GIT_CONFIG)
        self.assertNotIn("filter", text)
        self.assertFalse((c.work / ".git" / "commondir").exists())
        # config ist ein Symlink: er wird entfernt, nicht beschrieben
        (c.work / ".git" / "config").unlink()
        (c.work / ".git" / "config").symlink_to(outside / "config")
        bench.reset_git_config(c.work)
        self.assertFalse((c.work / ".git" / "config").is_symlink())
        self.assertEqual((outside / "config").read_text(), "unberührt")
        # .git ist ein Symlink oder eine gitfile
        import shutil
        shutil.rmtree(c.work / ".git")
        (c.work / ".git").symlink_to(outside)
        with self.assertRaises(sbx.IsolationError):
            bench.reset_git_config(c.work)
        (c.work / ".git").unlink()
        (c.work / ".git").write_text(f"gitdir: {outside}\n")
        with self.assertRaises(sbx.IsolationError):
            bench.reset_git_config(c.work)
        self.assertEqual((outside / "config").read_text(), "unberührt")

    def test_post_session_replaces_the_config_before_the_first_git_step(self):
        c = bench.Chain("vanilla", 1, cid="gd-" + os.urandom(3).hex())
        (c.home / ".claude").mkdir(parents=True)
        (c.work / ".git").mkdir(parents=True)
        c.out.mkdir(parents=True, exist_ok=True)
        (c.work / ".git" / "config").write_text('[filter "x"]\n\tclean = evil\n')
        seen = []

        class R:
            stdout, stderr, rc = "abc\n", "", 0

        def record(self, *cmds, **kw):
            seen.append((c.work / ".git" / "config").read_text())
            return [R() for _ in cmds]

        with mock.patch.object(bench.Chain, "tool", record):
            c.post_session("F1.a0")
        self.assertEqual(seen, [bench.GIT_CONFIG])

    def test_git_steps_ignore_the_global_config_and_attributes_of_the_agent_home(self):
        self.assertIn("core.attributesFile=/dev/null", " ".join(bench.GIT))
        self.assertIn("commit.gpgsign=false", " ".join(bench.GIT))
        c = bench.Chain("vanilla", 1, cid="ge-" + os.urandom(3).hex())
        boxes = []

        def new_sandbox(tag, **kw):
            boxes.append(FakeSandbox(make_spec(), c.root / "in" / tag, []))
            return boxes[-1]

        c.new_sandbox = new_sandbox
        c.tool(["git", "status"], tag="t")
        self.assertEqual(boxes[0].agent_envs[0]["GIT_CONFIG_GLOBAL"], "/dev/null")

    def test_ids_are_validated(self):
        """n4: Die Kennung landet in Pfaden, Namen und Labels."""
        for bad in ("../x", "a/b", "", "a b", "-x", "x;y", ".hidden", "a" * 100):
            self.assertFalse(bench.valid_id(bad), repr(bad))
            if bad:  # "" heißt: Kennung erzeugen
                with self.assertRaises(ValueError, msg=repr(bad)):
                    bench.Chain("vanilla", 0, cid=bad)
        for good in ("a1b2c3", "rg-a1b2c3", "probe-ff00aa", "e_token", "grade-0123ab"):
            self.assertTrue(bench.valid_id(good), good)

    def test_regrade_refuses_an_invalid_id_before_touching_anything(self):
        with mock.patch.object(bench, "RESULTS", Path(_TMP) / "res"):
            for bad in ("../../etc", "a/../b", ""):
                with self.assertRaises(ValueError, msg=repr(bad)):
                    bench.regrade(bad)

    def test_the_ghosttree_forward_does_not_inherit_the_harness_environment(self):
        """m7: socat für gt.sock erbte die Umgebung des Harness samt API-Schlüssel."""
        c = bench.Chain("ghosttree", 1, cid="f-" + os.urandom(3).hex())
        (c.root / "gt").mkdir(parents=True)
        (c.root / "gt-server-home").mkdir()
        c.meta["gt_port"] = 4711
        popens = []

        class P:
            def __init__(self, argv, **kw):
                popens.append((argv, kw))

        class Conn:
            def __enter__(self):
                return self

            def __exit__(self, *a):
                return False

        with mock.patch.dict(os.environ, {"ANTHROPIC_API_KEY": "nur-im-test"}), \
                mock.patch.object(bench.subprocess, "Popen", P), \
                mock.patch.object(bench.socket, "create_connection", lambda *a, **k: Conn()):
            c.start_gt_server()
        self.assertEqual(len(popens), 2)
        for argv, kw in popens:
            self.assertEqual(kw["env"], c.host_env(), argv)
            self.assertNotIn("ANTHROPIC_API_KEY", kw["env"])
        self.assertEqual(popens[1][0][0], "socat")

    def test_the_chain_proxy_starts_with_cost_limits(self):
        """m3: Chain.ensure_proxy bekommt Grenzen aus der Umgebung (oder die Standardwerte), nie einen offenen Proxy."""
        c = bench.Chain("vanilla", 0, cid="lim-" + os.urandom(3).hex())
        with mock.patch.dict(os.environ, {"BENCH_PROXY_MAX_REQUESTS": "11", "BENCH_PROXY_MAX_TOKENS": "999"}):
            proxy = c.ensure_proxy()
        self.addCleanup(c.cleanup)
        self.assertEqual((proxy.max_requests, proxy.max_tokens), (11, 999))
        d = bench.Chain("vanilla", 0, cid="lim2-" + os.urandom(3).hex())
        with mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop("BENCH_PROXY_MAX_REQUESTS", None)
            os.environ.pop("BENCH_PROXY_MAX_TOKENS", None)
            proxy2 = d.ensure_proxy()
        self.addCleanup(d.cleanup)
        self.assertGreater(proxy2.max_requests, 0)
        self.assertGreater(proxy2.max_tokens, 0)


class GradeMountTest(unittest.TestCase):
    """M3: Beim Bewerten lässt sich über einen Symlink im Stand des Agenten kein Verzeichnis des Hosts einhängen."""

    def setUp(self):
        self.c = bench.Chain("vanilla", 1, cid="m-" + os.urandom(3).hex())
        self.c.work.mkdir(parents=True)
        (self.c.root / "gocache-grade").mkdir()
        self.secret = Path(tempfile.mkdtemp(prefix="hostdir-", dir=_TMP))
        (self.secret / "geheim.txt").write_text("nur auf dem Host")
        self.started = []

        def boom(*a, **k):
            self.started.append(a[:1])
            raise AssertionError(f"Es darf kein Prozess entstehen: {a[:1]}")

        for target in ("subprocess.run", "subprocess.Popen"):
            p = mock.patch(target, boom)
            p.start()
            self.addCleanup(p.stop)

    def swap_work_for_symlink(self):
        import shutil
        shutil.rmtree(self.c.work)
        self.c.work.symlink_to(self.secret)

    def test_the_workdir_is_not_a_root_of_its_own(self):
        gr = bench.SandboxGradeRun(self.c, self.c.work)
        box = gr._box()
        self.assertNotIn(str(self.c.work), box.spec.allowed_roots)
        self.assertEqual(sbx.verify_spec(box.spec, box.base_mounts()), [])

    def test_a_symlinked_workdir_is_refused_before_the_sandbox_exists(self):
        self.swap_work_for_symlink()
        with self.assertRaises(sbx.IsolationError):
            bench.SandboxGradeRun(self.c, self.c.work)._box()
        with self.assertRaises(sbx.IsolationError):
            bench.SandboxGradeRun(self.c, self.c.work).go(["true"], 10)
        with self.assertRaises(sbx.IsolationError):
            bench.SandboxGradeRun(self.c, self.c.work).git("log")
        self.assertEqual(self.started, [])

    def test_a_symlinked_src_directory_is_refused_too(self):
        import shutil
        shutil.rmtree(self.c.home / "src")
        (self.c.home / "src").symlink_to(self.secret)
        (self.secret / "ledgerly").mkdir()
        with self.assertRaises(sbx.IsolationError):
            bench.SandboxGradeRun(self.c, self.c.work)._box()
        self.assertEqual(self.started, [])

    def test_verify_spec_still_refuses_when_the_symlink_appears_after_the_check(self):
        """Die Prüfung in _box und die in start() sind unabhängig: fällt eine aus, hält die andere."""
        self.swap_work_for_symlink()
        with mock.patch.object(bench, "require_real_dirs", lambda *a, **k: None):
            box = bench.SandboxGradeRun(self.c, self.c.work)._box()
            with self.assertRaises(sbx.IsolationError):
                box.start()
        self.assertEqual(self.started, [])

    def test_a_workdir_outside_the_runs_needs_roots_given_by_the_operator(self):
        """`bench.py grade <aufgabe> <verzeichnis>` nimmt ein Verzeichnis des Bedieners: das ist eine ausdrückliche Wurzel."""
        elsewhere = Path(tempfile.mkdtemp(prefix="operator-", dir="/var/tmp"))
        self.addCleanup(__import__("shutil").rmtree, elsewhere, True)
        box = bench.SandboxGradeRun(self.c, elsewhere)._box()
        self.assertTrue(any("erlaubten Wurzeln" in b for b in sbx.verify_spec(box.spec, box.base_mounts())))
        box = bench.SandboxGradeRun(self.c, elsewhere, trusted_roots=[elsewhere])._box()
        self.assertEqual(sbx.verify_spec(box.spec, box.base_mounts()), [])


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

    def test_symlink_planted_during_a_sandbox_step_is_still_caught(self):
        """Die Prüfung steht nach ctx watch: ein Symlink, der erst in diesem Schritt entsteht, wird gefunden."""
        gt = bench.Chain("ghosttree", 1, cid="s-gt-" + os.urandom(3).hex())
        (gt.home / ".claude").mkdir(parents=True)
        gt.work.mkdir(parents=True)
        gt.out.mkdir(parents=True, exist_ok=True)

        class R:
            stdout, stderr, rc = "", "", 0

        def plant(*cmds, **kw):
            (gt.home / ".claude" / "projects").symlink_to(self.outside)
            return [R()]

        with mock.patch.object(bench.Chain, "tool", side_effect=plant), self.assertRaises(sbx.IsolationError):
            gt.post_session("F1.a0")
        self.assert_untouched()

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

    def test_privilege_check_requires_a_seccomp_filter(self):
        """m6: Seccomp: 0 heißt, der Kernel filtert keine Systemaufrufe (bwrap ohne Filter, Docker mit seccomp=unconfined)."""
        import probe

        def status(sec):
            return f"CapEff:\t0000000000000000\nCapBnd:\t0000000000000000\nNoNewPrivs:\t1\nSeccomp:\t{sec}\n"

        for sec, ok in ((0, False), (1, True), (2, True)):
            with mock.patch("builtins.open", mock.mock_open(read_data=status(sec))), mock.patch.object(probe.os, "getuid", return_value=1000):
                res = probe.privileges({})
            self.assertEqual(res["ok"], ok, (sec, res))
        with mock.patch("builtins.open", mock.mock_open(read_data="CapEff:\t0\nCapBnd:\t0\nNoNewPrivs:\t1\n")), \
                mock.patch.object(probe.os, "getuid", return_value=1000):
            self.assertFalse(probe.privileges({})["ok"], "ohne Seccomp-Zeile ist es nicht nachgewiesen")

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
