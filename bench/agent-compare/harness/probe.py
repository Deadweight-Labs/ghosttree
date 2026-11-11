#!/usr/bin/env python3
"""Isolationsprobe: läuft IN der Sandbox, als derselbe Benutzer und mit derselben Umgebung wie der Agent.

Aufruf: probe.py <konfiguration.json>. Ausgabe: ein JSON-Objekt {"checks": {name: {"ok", "detail"}}}.
Der Harness wertet die Ausgabe aus und schreibt isolation.json; diese Datei selbst bewertet nichts.
Nur Standardbibliothek (Python 3.11 im Image). Von der Umgebung werden nur Namen gelesen, nie Werte.
"""
import json
import os
import re
import socket
import subprocess
import sys
import threading

SECRET_NAME = re.compile(r"(API_?KEY|SECRET|PASSW|CREDENTIAL|PRIVATE|^SSH_|^AWS_|^GH_|^GITHUB_|TOKEN)", re.I)
PUBLIC_TARGETS = [("1.1.1.1", 443), ("8.8.8.8", 53), ("93.184.216.34", 80)]


def check(ok, detail=""):
    return {"ok": bool(ok), "detail": detail}


def unescape(path):
    return re.sub(r"\\([0-7]{3})", lambda m: chr(int(m.group(1), 8)), path)


def net_public(cfg):
    reached = []
    for host, port in PUBLIC_TARGETS + [tuple(t) for t in cfg.get("extra_targets", [])]:
        try:
            with socket.create_connection((host, port), timeout=2):
                reached.append(f"{host}:{port}")
        except OSError:
            pass
    return check(not reached, f"erreicht: {reached}" if reached else "keine Verbindung nach außen")


def net_interfaces(cfg):
    try:
        names = [line.split(":")[0].strip() for line in open("/proc/net/dev").read().splitlines()[2:]]
    except OSError as ex:
        return check(False, f"/proc/net/dev nicht lesbar: {ex}")
    extra = [n for n in names if n != "lo"]
    return check(not extra, f"Schnittstellen: {names}")


def dns(cfg):
    result = []

    def resolve():
        try:
            socket.getaddrinfo("example.com", 443)
            result.append(True)
        except OSError:
            result.append(False)

    t = threading.Thread(target=resolve, daemon=True)
    t.start()
    t.join(5)
    return check(not (result and result[0]), "Namensauflösung gelang" if result and result[0] else "keine Namensauflösung")


def host_paths(cfg):
    present = [p for p in cfg.get("forbidden_paths", []) if os.path.lexists(p)]
    try:
        home_entries = sorted(os.listdir("/home"))
    except OSError:
        home_entries = []
    stray = [e for e in home_entries if e not in cfg.get("allowed_home_entries", [])]
    return check(not present and not stray, f"sichtbar: {present}, /home: {home_entries}")


def env_names(cfg):
    names = sorted(os.environ)
    expected = sorted(cfg["env_names"])
    return check(names == expected, f"Namen: {names}" if names != expected else f"genau die erlaubten {len(names)} Namen")


def env_secrets(cfg):
    """Namen aller lesbaren Prozesse dieser Sandbox; ein Schlüssel hieße ANTHROPIC_API_KEY o. ä."""
    allowed = set(cfg.get("allowed_secret_like", []))
    bad = {}
    scanned = 0
    for pid in filter(str.isdigit, os.listdir("/proc")):
        try:
            raw = open(f"/proc/{pid}/environ", "rb").read()
        except OSError:
            continue
        scanned += 1
        for entry in raw.split(b"\0"):
            name = entry.split(b"=", 1)[0].decode(errors="replace")
            if name and SECRET_NAME.search(name) and name not in allowed:
                bad.setdefault(name, []).append(pid)
    return check(not bad, f"verdächtige Namen: {sorted(bad)}" if bad else f"{scanned} Prozesse ohne Schlüsselnamen")


def mounts(cfg):
    expected = cfg["expected_mounts"]  # {ziel: schreibbar}
    implicit = cfg.get("implicit_mounts", [])
    found = {}
    for line in open("/proc/self/mountinfo"):
        parts = line.split()
        found[unescape(parts[4])] = parts[5].split(",")
    unexpected = [m for m in found if m != "/" and m not in expected
                  and not any(m == p or m.startswith(p.rstrip("/") + "/") for p in implicit)]
    missing = [m for m in expected if m not in found]
    wrong_mode = [m for m, rw in expected.items() if m in found and (("rw" in found[m]) != bool(rw))]
    ok = not (unexpected or missing or wrong_mode)
    return check(ok, f"unerwartet {unexpected}, fehlt {missing}, falscher Modus {wrong_mode}" if not ok
                 else f"{len(expected)} erwartete Einhängungen")


def root_readonly(cfg):
    try:
        with open("/probe-write-test", "w"):
            pass
        os.unlink("/probe-write-test")
        return check(False, "Wurzel ist beschreibbar")
    except OSError as ex:
        return check(True, f"Schreiben in / scheitert ({ex.strerror})")


def privileges(cfg):
    status = dict(line.split(":\t", 1) for line in open("/proc/self/status").read().splitlines() if ":\t" in line)
    caps_zero = int(status.get("CapEff", "1").strip(), 16) == 0 and int(status.get("CapBnd", "1").strip(), 16) == 0
    nnp = status.get("NoNewPrivs", "0").strip() == "1"
    ok = os.getuid() != 0 and caps_zero and nnp
    return check(ok, f"uid={os.getuid()} Capabilities leer={caps_zero} no_new_privs={nnp}")


def http(port, method, path, token, body=None):
    import http.client
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
    headers = {"authorization": f"Bearer {token}", "content-type": "application/json"}
    conn.request(method, path, body=body, headers=headers)
    resp = conn.getresponse()
    data = resp.read()
    conn.close()
    return resp.status, data


def proxy(cfg):
    port, token = cfg["proxy_port"], os.environ.get("ANTHROPIC_AUTH_TOKEN", "")
    try:
        status, data = http(port, "GET", "/_bench/ping", token)
        info = json.loads(data)
    except Exception as ex:
        return check(False, f"Proxy nicht erreichbar: {type(ex).__name__}: {ex}")
    if status != 200 or info.get("run_id") != cfg["run_id"]:
        return check(False, f"Ping: Status {status}, Lauf {info.get('run_id')!r}")
    try:
        bad_status, _ = http(port, "GET", "/_bench/ping", "falsches-kennzeichen")
        model_status, _ = http(port, "POST", "/v1/messages", token, json.dumps({"model": "kein-erlaubtes-modell"}))
        path_status, _ = http(port, "POST", "/v1/complete", token, "{}")
    except Exception as ex:
        return check(False, f"Proxy-Gegenprobe gescheitert: {ex}")
    ok = bad_status == 401 and model_status == 403 and path_status == 403
    return check(ok, f"Ping ok; falsches Kennzeichen {bad_status} (401), fremdes Modell {model_status} (403), fremder Pfad {path_status} (403)")


def git_state(cfg):
    g = cfg.get("git")
    if not g:
        return None
    base = ["git", "-c", "safe.directory=*", "-c", "core.hooksPath=/dev/null", "-C", g["repo"]]

    def run(*args):
        return subprocess.run(base + list(args), capture_output=True, text=True, timeout=60)

    problems = []
    for sha in g.get("absent_objects", []):
        if run("cat-file", "-e", sha + "^{commit}").returncode == 0:
            problems.append(f"Objekt {sha[:12]} ist vorhanden")
    refs = run("for-each-ref", "--format=%(refname)")
    if refs.returncode != 0:
        problems.append("git for-each-ref scheitert")
    elif "allowed_refs" in g:
        extra = [r for r in refs.stdout.split() if r not in g["allowed_refs"]]
        if extra:
            problems.append(f"unerwartete Refs: {extra}")
    if "commit_count" in g:
        n = run("rev-list", "--all", "--count")
        if n.returncode != 0 or n.stdout.strip() != str(g["commit_count"]):
            problems.append(f"Commits: {n.stdout.strip()!r}, erwartet {g['commit_count']}")
    return check(not problems, "; ".join(problems) or "Repo enthält nur die erwarteten Objekte und Refs")


CHECKS = [("net_public", net_public), ("net_interfaces", net_interfaces), ("dns", dns), ("host_paths", host_paths),
          ("env_names", env_names), ("env_secrets", env_secrets), ("mounts", mounts), ("root_readonly", root_readonly),
          ("privileges", privileges), ("proxy", proxy), ("git", git_state)]


def main():
    cfg = json.load(open(sys.argv[1]))
    out = {}
    for name, fn in CHECKS:
        try:
            res = fn(cfg)
        except Exception as ex:  # eine kaputte Probe zählt als nicht bestanden
            res = check(False, f"Probe abgebrochen: {type(ex).__name__}: {ex}")
        if res is not None:
            out[name] = res
    print(json.dumps({"probe_version": 1, "uid": os.getuid(), "checks": out}))


if __name__ == "__main__":
    main()
