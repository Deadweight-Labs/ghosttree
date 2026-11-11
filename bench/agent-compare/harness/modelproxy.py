#!/usr/bin/env python3
"""Modell-Proxy für die Sandbox: einziger Weg der Agenten zum Modell.

Der Proxy lauscht auf einem Unix-Socket, den der Harness in die Sandbox einhängt. Der Agent
spricht über 127.0.0.1 (socat in der Sandbox) mit ihm und trägt statt eines API-Schlüssels
ein Laufkennzeichen ein. Den echten Schlüssel liest der Proxy erst bei einer Anfrage aus
seiner eigenen Umgebung (Variable `key_env`); er steht nie in der Umgebung der Sandbox, nie
in einer Datei und nie im Protokoll.

Durchgelassen werden nur POST /v1/messages und POST /v1/messages/count_tokens mit gültigem
Laufkennzeichen und einem Modell aus der Erlaubnisliste. Je Lauf gelten Grenzen für Anfragen,
Tokens und gleichzeitige Anfragen (Rate-Gate). Der Verbrauch je Anfrage geht als JSON-Zeile
in `usage_log`. GET /_bench/ping antwortet ohne Modellaufruf und dient der Isolationsprobe.

Aufruf als Programm (der Harness benutzt die Klasse direkt):
  modelproxy.py --sock PFAD --run-id ID --token KENNZEICHEN [--upstream URL] [--usage-log DATEI]
"""
import argparse
import hmac
import http.client
import json
import os
import re
import socketserver
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler
from pathlib import Path

DEFAULT_UPSTREAM = "https://api.anthropic.com"
DEFAULT_MODELS = r"^claude-(opus-5-5|sonnet-5-5|haiku-(4-5|5-5))"
ALLOWED_POST = ("/v1/messages", "/v1/messages/count_tokens")
PING_PATH = "/_bench/ping"
MAX_BODY = 32 * 1024 * 1024
# Kopfzeilen, die vom Agenten zum Modell mitgehen. Anmeldung setzt der Proxy selbst.
FORWARD_REQUEST_HEADERS = ("anthropic-version", "anthropic-beta", "content-type", "accept", "user-agent",
                           "x-stainless-lang", "x-stainless-package-version", "x-app")
FORWARD_RESPONSE_HEADERS = ("content-type", "request-id", "retry-after", "x-should-retry", "anthropic-ratelimit-requests-remaining")


class ProxyError(Exception):
    def __init__(self, status, kind, message):
        super().__init__(message)
        self.status, self.kind, self.message = status, kind, message


class Usage:
    """Summiert die Token-Angaben einer Antwort, auch aus einem SSE-Strom."""

    FIELDS = ("input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens")

    def __init__(self):
        self.totals = dict.fromkeys(self.FIELDS, 0)
        self._buf = b""

    def add(self, usage):
        if not isinstance(usage, dict):
            return
        for k in self.FIELDS:
            v = usage.get(k)
            if isinstance(v, int):
                # message_start meldet die Eingabe, message_delta die laufende Ausgabe: der letzte Wert gilt je Feld
                self.totals[k] = max(self.totals[k], v)

    def feed_json(self, body):
        try:
            self.add(json.loads(body).get("usage"))
        except (ValueError, AttributeError):
            pass

    def feed_sse(self, chunk):
        self._buf += chunk
        *lines, self._buf = self._buf.split(b"\n")
        for line in lines:
            if not line.startswith(b"data:"):
                continue
            try:
                ev = json.loads(line[5:])
            except ValueError:
                continue
            if ev.get("type") == "message_start":
                self.add((ev.get("message") or {}).get("usage"))
            elif ev.get("type") == "message_delta":
                self.add(ev.get("usage"))

    @property
    def total(self):
        return sum(self.totals.values())


class ModelProxy:
    def __init__(self, sock_path, run_id, token, upstream=None, key_env=None, usage_log=None,
                 models=None, max_requests=5000, max_tokens=0, max_concurrent=4, auth_style=None):
        self.sock_path = str(sock_path)
        self.run_id = run_id
        self.token = token
        self.upstream = urllib.parse.urlsplit(upstream or os.environ.get("BENCH_UPSTREAM_URL") or DEFAULT_UPSTREAM)
        self.key_env = key_env or os.environ.get("BENCH_UPSTREAM_KEY_ENV") or "ANTHROPIC_API_KEY"
        self.auth_style = auth_style or os.environ.get("BENCH_UPSTREAM_AUTH") or "x-api-key"
        self.usage_log = Path(usage_log) if usage_log else None
        self.models = re.compile(models or os.environ.get("BENCH_PROXY_MODELS") or DEFAULT_MODELS)
        self.max_requests, self.max_tokens = max_requests, max_tokens
        self._gate = threading.BoundedSemaphore(max_concurrent)
        self._lock = threading.Lock()
        self.requests = 0
        self.tokens = 0
        self._server = None
        self._thread = None

    # ---------- Betrieb ----------
    def start(self):
        sock = Path(self.sock_path)
        sock.parent.mkdir(parents=True, exist_ok=True)
        if sock.exists() or sock.is_symlink():
            sock.unlink()
        proxy = self

        class Handler(_Handler):
            owner = proxy

        self._server = _UnixServer(self.sock_path, Handler)
        os.chmod(self.sock_path, 0o600)
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True, name="modelproxy")
        self._thread.start()
        return self

    def stop(self):
        if self._server:
            self._server.shutdown()
            self._server.server_close()
            self._server = None
        Path(self.sock_path).unlink(missing_ok=True)

    def has_key(self):
        return bool(os.environ.get(self.key_env))

    def require_key(self):
        """Der Harness prüft das vor einem echten Lauf; die Isolationsprobe braucht keinen Schlüssel."""
        if not self.has_key():
            raise RuntimeError(f"Umgebungsvariable {self.key_env} fehlt: der Proxy hat keinen Schlüssel für das Modell")

    # ---------- Prüfung einer Anfrage ----------
    def authorize(self, headers):
        auth = headers.get("authorization") or ""
        offered = [headers.get("x-api-key") or ""]
        if auth.lower().startswith("bearer "):
            offered.append(auth[7:].strip())
        if not any(c and hmac.compare_digest(c.encode(), self.token.encode()) for c in offered):
            raise ProxyError(401, "authentication_error", "unbekanntes Laufkennzeichen")

    def check_post(self, path, body):
        if path not in ALLOWED_POST:
            raise ProxyError(403, "permission_error", f"Pfad nicht erlaubt: {path}")
        try:
            req = json.loads(body)
        except ValueError:
            raise ProxyError(400, "invalid_request_error", "Anfrage ist kein JSON")
        model = req.get("model") if isinstance(req, dict) else None
        if not isinstance(model, str) or not self.models.match(model):
            raise ProxyError(403, "permission_error", f"Modell nicht erlaubt: {model!r}")
        return req

    def reserve(self):
        with self._lock:
            if self.requests >= self.max_requests:
                raise ProxyError(429, "rate_limit_error", "Anfragegrenze des Laufs erreicht")
            if self.max_tokens and self.tokens >= self.max_tokens:
                raise ProxyError(429, "rate_limit_error", "Tokengrenze des Laufs erreicht")
            self.requests += 1

    def record(self, entry):
        with self._lock:
            self.tokens += entry.get("tokens", 0)
        if self.usage_log:
            line = json.dumps({"run_id": self.run_id, "ts": round(time.time(), 3), **entry}, ensure_ascii=False)
            with self._lock, open(self.usage_log, "a") as f:
                f.write(line + "\n")

    def summary(self):
        with self._lock:
            return {"requests": self.requests, "tokens": self.tokens}

    # ---------- Weiterleitung ----------
    def upstream_headers(self, headers):
        out = {k: v for k, v in headers.items() if k.lower() in FORWARD_REQUEST_HEADERS}
        key = os.environ.get(self.key_env)
        if not key:
            raise ProxyError(503, "api_error", f"Umgebungsvariable {self.key_env} fehlt im Proxy")
        if self.auth_style == "bearer":
            out["authorization"] = f"Bearer {key}"
        else:
            out["x-api-key"] = key
        out["accept-encoding"] = "identity"
        return out

    def connect(self):
        host, port = self.upstream.hostname or "", self.upstream.port
        if self.upstream.scheme == "https":
            return http.client.HTTPSConnection(host, port, timeout=900)
        return http.client.HTTPConnection(host, port, timeout=900)


class _UnixServer(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True
    request_queue_size = 64


class _Handler(BaseHTTPRequestHandler):
    owner: "ModelProxy"  # setzt ModelProxy.start() in der Unterklasse
    protocol_version = "HTTP/1.0"  # Antwort endet mit dem Schließen der Verbindung: Streaming ohne Längenangabe

    def log_message(self, format, *args):  # noqa: A002 - Name aus der Basisklasse; der Proxy protokolliert nichts über stderr
        pass

    def address_string(self):
        return "unix"

    def _json(self, status, obj):
        data = json.dumps(obj).encode()
        self.send_response(status)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(data)))
        if status == 429:
            self.send_header("x-should-retry", "false")
        self.end_headers()
        self.wfile.write(data)

    def _error(self, ex):
        self._json(ex.status, {"type": "error", "error": {"type": ex.kind, "message": ex.message}})

    def do_GET(self):
        try:
            self.owner.authorize(self.headers)
            if self.path.split("?")[0] != PING_PATH:
                raise ProxyError(403, "permission_error", "nur POST /v1/messages und /v1/messages/count_tokens")
            self._json(200, {"proxy": "bench-model-proxy", "run_id": self.owner.run_id, **self.owner.summary()})
        except ProxyError as ex:
            self._error(ex)

    do_PUT = do_DELETE = do_PATCH = do_GET

    def do_POST(self):
        o = self.owner
        try:
            o.authorize(self.headers)
            path = self.path.split("?")[0]
            try:
                length = int(self.headers.get("content-length") or 0)
            except ValueError:
                length = -1
            if length < 0:  # rfile.read(-1) läse bis zum Ende der Verbindung, ohne Grenze
                raise ProxyError(400, "invalid_request_error", "Content-Length fehlt oder ist ungültig")
            if length > MAX_BODY:
                raise ProxyError(413, "request_too_large", "Anfrage zu groß")
            body = self.rfile.read(length)
            req = o.check_post(path, body)
            headers = o.upstream_headers(self.headers)
            o.reserve()
        except ProxyError as ex:
            return self._error(ex)
        t0 = time.time()
        usage = Usage()
        status = 502
        with o._gate:
            try:
                conn = o.connect()
                conn.request("POST", self.path, body=body, headers=headers)
                resp = conn.getresponse()
                status = resp.status
                self.send_response(status)
                for k in FORWARD_RESPONSE_HEADERS:
                    if resp.getheader(k):
                        self.send_header(k, resp.getheader(k))
                self.end_headers()
                streaming = "text/event-stream" in (resp.getheader("content-type") or "")
                whole = b""
                while True:
                    chunk = resp.read1(65536) if hasattr(resp, "read1") else resp.read(65536)
                    if not chunk:
                        break
                    self.wfile.write(chunk)
                    self.wfile.flush()
                    if streaming:
                        usage.feed_sse(chunk)
                    elif len(whole) < MAX_BODY:
                        whole += chunk
                if not streaming:
                    usage.feed_json(whole)
                conn.close()
            except (OSError, http.client.HTTPException) as ex:
                if status == 502:
                    try:
                        self._json(502, {"type": "error", "error": {"type": "api_error", "message": f"Upstream nicht erreichbar: {type(ex).__name__}"}})
                    except OSError:
                        pass
        o.record({"path": path, "model": req.get("model"), "status": status, "duration_s": round(time.time() - t0, 2),
                  "tokens": usage.total, **usage.totals})


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--sock", required=True)
    ap.add_argument("--run-id", required=True)
    ap.add_argument("--token", required=True)
    ap.add_argument("--upstream")
    ap.add_argument("--usage-log")
    ap.add_argument("--max-requests", type=int, default=5000)
    ap.add_argument("--max-tokens", type=int, default=0)
    a = ap.parse_args()
    p = ModelProxy(a.sock, a.run_id, a.token, upstream=a.upstream, usage_log=a.usage_log,
                   max_requests=a.max_requests, max_tokens=a.max_tokens)
    p.require_key()
    p.start()
    try:
        threading.Event().wait()
    except KeyboardInterrupt:
        p.stop()


if __name__ == "__main__":
    main()
