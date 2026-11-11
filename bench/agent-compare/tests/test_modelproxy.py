"""Tests für den Modell-Proxy: laufen ohne Docker, ohne Netz und ohne echten Schlüssel (ein Schein-Upstream)."""
import http.client
import http.server
import json
import os
import shutil
import socket
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "harness"))
from modelproxy import ModelProxy  # noqa: E402

TOKEN = "lauf-kennzeichen"
FAKE_KEY_ENV = "BENCH_TEST_FAKE_UPSTREAM_KEY"
FAKE_KEY = "schein-schluessel-nur-im-test"


class UnixConnection(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("localhost")
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.connect(self.path)


class FakeUpstream(http.server.BaseHTTPRequestHandler):
    seen = []

    def log_message(self, format, *args):  # noqa: A002
        pass

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("content-length") or 0))
        FakeUpstream.seen.append({"path": self.path, "headers": {k.lower(): v for k, v in self.headers.items()}, "body": body})
        req = json.loads(body)
        if req.get("stream"):
            events = [
                {"type": "message_start", "message": {"usage": {"input_tokens": 11, "output_tokens": 1}}},
                {"type": "message_delta", "usage": {"output_tokens": 7}},
            ]
            data = "".join(f"event: {e['type']}\ndata: {json.dumps(e)}\n\n" for e in events).encode()
            self.send_response(200)
            self.send_header("content-type", "text/event-stream")
            self.end_headers()
            self.wfile.write(data)
            return
        data = json.dumps({"id": "msg_1", "usage": {"input_tokens": 5, "output_tokens": 3}}).encode()
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


class ProxyTest(unittest.TestCase):
    def setUp(self):
        FakeUpstream.seen = []
        self.dir = Path(tempfile.mkdtemp(prefix="mp-"))
        self.addCleanup(shutil.rmtree, self.dir, True)
        self.upstream = http.server.ThreadingHTTPServer(("127.0.0.1", 0), FakeUpstream)
        threading.Thread(target=self.upstream.serve_forever, daemon=True).start()
        self.addCleanup(self.upstream.server_close)
        self.addCleanup(self.upstream.shutdown)
        os.environ[FAKE_KEY_ENV] = FAKE_KEY
        self.addCleanup(os.environ.pop, FAKE_KEY_ENV, None)
        self.log = self.dir / "usage.jsonl"
        self.proxy = self.make()

    def make(self, **kw):
        p = ModelProxy(self.dir / "m.sock", "run-1", TOKEN, upstream=f"http://127.0.0.1:{self.upstream.server_address[1]}",
                       key_env=FAKE_KEY_ENV, usage_log=self.log, **kw).start()
        self.addCleanup(p.stop)
        return p

    def call(self, method, path, body=None, token: str | None = TOKEN, headers=None):
        conn = UnixConnection(str(self.dir / "m.sock"))
        h = dict(headers or {})
        if token is not None:
            h["authorization"] = f"Bearer {token}"
        conn.request(method, path, body=json.dumps(body) if body is not None else None, headers=h)
        r = conn.getresponse()
        data = r.read()
        conn.close()
        return r.status, data

    def test_ping_answers_without_model_call(self):
        status, data = self.call("GET", "/_bench/ping")
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(data)["run_id"], "run-1")
        self.assertEqual(FakeUpstream.seen, [])

    def test_wrong_or_missing_token_is_refused(self):
        self.assertEqual(self.call("GET", "/_bench/ping", token="falsch")[0], 401)
        self.assertEqual(self.call("GET", "/_bench/ping", token=None)[0], 401)
        self.assertEqual(self.call("POST", "/v1/messages", {"model": "claude-opus-5-5"}, token="falsch")[0], 401)
        self.assertEqual(FakeUpstream.seen, [])

    def test_only_messages_and_count_tokens_pass(self):
        for path in ("/v1/complete", "/v1/models", "/v1/files", "/v1/messages/batches"):
            self.assertEqual(self.call("POST", path, {"model": "claude-opus-5-5"})[0], 403, path)
        self.assertEqual(self.call("GET", "/v1/messages")[0], 403)
        self.assertEqual(FakeUpstream.seen, [])

    def test_model_outside_allowlist_is_refused(self):
        self.assertEqual(self.call("POST", "/v1/messages", {"model": "claude-opus-4-1"})[0], 403)
        self.assertEqual(self.call("POST", "/v1/messages", {"messages": []})[0], 403)
        self.assertEqual(FakeUpstream.seen, [])

    def test_forwards_with_upstream_key_not_run_token(self):
        status, data = self.call("POST", "/v1/messages?beta=true", {"model": "claude-opus-5-5", "messages": []},
                                 headers={"anthropic-version": "2023-06-01", "x-api-key": "vom-agenten"})
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(data)["id"], "msg_1")
        sent = FakeUpstream.seen[0]
        self.assertEqual(sent["path"], "/v1/messages?beta=true")
        self.assertEqual(sent["headers"]["x-api-key"], FAKE_KEY)
        self.assertNotIn(TOKEN, json.dumps(sent["headers"]))
        self.assertEqual(sent["headers"]["anthropic-version"], "2023-06-01")

    def test_usage_is_logged_per_request_without_the_key(self):
        self.call("POST", "/v1/messages", {"model": "claude-haiku-4-5"})
        self.call("POST", "/v1/messages/count_tokens", {"model": "claude-opus-5-5"})
        lines = [json.loads(line) for line in self.log.read_text().splitlines()]
        self.assertEqual([entry["path"] for entry in lines], ["/v1/messages", "/v1/messages/count_tokens"])
        self.assertEqual(lines[0]["input_tokens"], 5)
        self.assertEqual(lines[0]["run_id"], "run-1")
        self.assertNotIn(FAKE_KEY, self.log.read_text())
        self.assertEqual(self.proxy.summary(), {"requests": 2, "tokens": 16})

    def test_streaming_usage_is_summed(self):
        status, data = self.call("POST", "/v1/messages", {"model": "claude-opus-5-5", "stream": True})
        self.assertEqual(status, 200)
        self.assertIn(b"message_delta", data)
        entry = json.loads(self.log.read_text().splitlines()[0])
        self.assertEqual((entry["input_tokens"], entry["output_tokens"]), (11, 7))

    def test_request_limit_gives_429_without_retry(self):
        self.proxy.stop()
        self.proxy = self.make(max_requests=1)
        self.assertEqual(self.call("POST", "/v1/messages", {"model": "claude-opus-5-5"})[0], 200)
        status, data = self.call("POST", "/v1/messages", {"model": "claude-opus-5-5"})
        self.assertEqual(status, 429)
        self.assertEqual(json.loads(data)["error"]["type"], "rate_limit_error")
        self.assertEqual(len(FakeUpstream.seen), 1)

    def test_token_limit_gives_429(self):
        self.proxy.stop()
        self.proxy = self.make(max_tokens=5)
        self.assertEqual(self.call("POST", "/v1/messages", {"model": "claude-opus-5-5"})[0], 200)
        self.assertEqual(self.call("POST", "/v1/messages", {"model": "claude-opus-5-5"})[0], 429)

    def test_missing_upstream_key_is_a_503_not_a_forward(self):
        del os.environ[FAKE_KEY_ENV]
        self.assertFalse(self.proxy.has_key())
        self.assertEqual(self.call("POST", "/v1/messages", {"model": "claude-opus-5-5"})[0], 503)
        self.assertEqual(FakeUpstream.seen, [])
        with self.assertRaises(RuntimeError):
            self.proxy.require_key()

    def raw(self, request):
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        s.settimeout(5)
        s.connect(str(self.dir / "m.sock"))
        s.sendall(request)
        data = b""
        try:
            while chunk := s.recv(4096):
                data += chunk
        except socket.timeout:
            self.fail("der Proxy hängt: er liest ohne Grenze weiter")
        except ConnectionResetError:
            pass  # der Proxy schloss mit ungelesenen Bytes im Puffer; was er vorher schickte, steht in data
        finally:
            s.close()
        return data

    def test_negative_or_invalid_content_length_is_a_400_and_does_not_hang(self):
        for value in (b"-1", b"abc", b"-99999999999"):
            reply = self.raw(b"POST /v1/messages HTTP/1.0\r\nauthorization: Bearer " + TOKEN.encode()
                             + b"\r\ncontent-length: " + value + b"\r\n\r\n")
            self.assertTrue(reply.startswith(b"HTTP/1.0 400"), (value, reply[:60]))
        self.assertEqual(FakeUpstream.seen, [])

    def test_oversized_content_length_is_a_413_before_any_read(self):
        reply = self.raw(b"POST /v1/messages HTTP/1.0\r\nauthorization: Bearer " + TOKEN.encode()
                         + b"\r\ncontent-length: 99999999999\r\n\r\n")
        self.assertTrue(reply.startswith(b"HTTP/1.0 413"), reply[:60])

    def test_socket_is_private(self):
        self.assertEqual(os.stat(self.dir / "m.sock").st_mode & 0o077, 0)

    # ---------- Review-Befunde (PR #239) ----------
    def post_raw(self, body, extra_headers=b""):
        return self.raw(b"POST /v1/messages HTTP/1.0\r\nauthorization: Bearer " + TOKEN.encode() + b"\r\n" + extra_headers
                        + b"content-length: " + str(len(body)).encode() + b"\r\n\r\n" + body)

    def test_duplicate_json_keys_cannot_smuggle_a_model_past_the_allowlist(self):
        """M2: {"model":"x","model":"claude-…"}: das Prüfwerkzeug sah den letzten Schlüssel, der Upstream den ersten."""
        for body in (b'{"model": "claude-opus-4-1", "model": "claude-opus-5-5"}',
                     b'{"model": "claude-opus-5-5", "model": "claude-opus-4-1"}',
                     b'{"model": "claude-opus-5-5", "mod\\u0065l": "claude-opus-4-1"}',
                     b'{"model": "claude-opus-5-5", "metadata": {"a": 1, "a": 2}}'):
            reply = self.post_raw(body)
            self.assertTrue(reply.startswith(b"HTTP/1.0 400"), (body, reply[:60]))
        self.assertEqual(FakeUpstream.seen, [])

    def test_numbers_and_nesting_that_cannot_be_forwarded_are_refused(self):
        """1e999 would be forwarded as Infinity (no JSON), deep nesting broke json.dumps without an answer."""
        deep = b'{"model": "claude-opus-5-5", "x": ' + b"[" * 50000 + b"]" * 50000 + b"}"
        for body in (b'{"model": "claude-opus-5-5", "t": 1e999}', deep):
            reply = self.post_raw(body)
            self.assertTrue(reply.startswith(b"HTTP/1.0 400"), (body[:60], reply[:60]))
        self.assertEqual(FakeUpstream.seen, [])

    def test_the_upstream_always_gets_application_json(self):
        FakeUpstream.seen.clear()
        reply = self.post_raw(b'{"model": "claude-opus-5-5"}', b"content-type: text/x-agent\r\n")
        self.assertTrue(reply.startswith(b"HTTP/1.0 200"), reply[:80])
        self.assertEqual(FakeUpstream.seen[0]["headers"]["content-type"], "application/json")

    def test_upstream_gets_the_checked_request_not_the_raw_bytes(self):
        """M2: weitergeleitet wird json.dumps der geprüften Anfrage, nie die Rohbytes (auch nicht UTF-16 oder Leerraum)."""
        raw_bodies = [b'  {"model" :\n "claude-opus-5-5",   "x": [1,  2], "t": "\\u00e4"}  ',
                      '{"model": "claude-opus-5-5", "t": "ä"}'.encode("utf-16")]
        for body in raw_bodies:
            FakeUpstream.seen.clear()
            reply = self.post_raw(body)
            self.assertTrue(reply.startswith(b"HTTP/1.0 200"), reply[:80])
            sent = FakeUpstream.seen[0]["body"]
            self.assertEqual(sent, json.dumps(json.loads(body)).encode())
            self.assertNotEqual(sent, body)

    def test_model_must_match_completely(self):
        """m1: Vorsatz allein genügt nicht, nur Datum und [1m] dürfen folgen."""
        ok = ("claude-opus-5-5", "claude-opus-5-5-20260101", "claude-sonnet-5-5[1m]", "claude-haiku-4-5-20251001[1m]")
        bad = ("claude-opus-5-5-evil", "claude-opus-5-55", "claude-haiku-4-5xyz", "claude-opus-5-5-2026", "claude-opus-5-5\n",
               "claude-opus-5-5-20260101-x", "claude-opus-5-5[1m]x", "xclaude-opus-5-5")
        for m in ok:
            self.assertEqual(self.call("POST", "/v1/messages", {"model": m})[0], 200, m)
        FakeUpstream.seen.clear()
        for m in bad:
            self.assertEqual(self.call("POST", "/v1/messages", {"model": m})[0], 403, m)
        self.assertEqual(FakeUpstream.seen, [])

    def test_a_custom_model_pattern_must_match_completely_too(self):
        self.proxy.stop()
        self.proxy = self.make(models=r"claude-opus-5-5")
        self.assertEqual(self.call("POST", "/v1/messages", {"model": "claude-opus-5-5"})[0], 200)
        self.assertEqual(self.call("POST", "/v1/messages", {"model": "claude-opus-5-5-x"})[0], 403)

    def test_header_values_with_line_breaks_are_refused(self):
        """m4: eine gefaltete Kopfzeile trägt CR/LF im Wert; sie ginge sonst in den Upstream (oder bräche den Handler ab)."""
        reply = self.post_raw(b'{"model": "claude-opus-5-5"}', b"anthropic-version: 2023-06-01\r\n x-injected: 1\r\n")
        self.assertTrue(reply.startswith(b"HTTP/1.0 400"), reply[:80])
        self.assertEqual(FakeUpstream.seen, [])

    def test_put_and_delete_on_ping_are_405(self):
        """n1: Nur GET beantwortet den Ping."""
        for method in ("PUT", "DELETE", "PATCH"):
            self.assertEqual(self.call(method, "/_bench/ping")[0], 405, method)
        self.assertEqual(self.call("GET", "/_bench/ping")[0], 200)
        self.assertEqual(self.call("PUT", "/_bench/ping", token="falsch")[0], 401)

    def test_upstream_over_plain_http_only_to_loopback(self):
        """n2: Der Schlüssel geht nie unverschlüsselt in ein fremdes Netz."""
        for url in ("http://api.anthropic.com", "http://10.1.2.3:8080", "http://[2001:db8::1]", "ftp://127.0.0.1", "http://"):
            with self.assertRaises(ValueError, msg=url):
                ModelProxy(self.dir / "x.sock", "r", TOKEN, upstream=url)
        for url in ("https://api.anthropic.com", "http://127.0.0.1:9", "http://localhost:9", "http://[::1]:9", "http://127.5.5.5"):
            ModelProxy(self.dir / "x.sock", "r", TOKEN, upstream=url)

    def test_default_limits_bound_the_cost_and_the_environment_overrides_them(self):
        """m3: Ein Proxy ohne Angabe ist begrenzt (Anfragen und Tokens), nicht offen."""
        p = ModelProxy(self.dir / "x.sock", "r", TOKEN, upstream="http://127.0.0.1:9")
        self.assertGreater(p.max_requests, 0)
        self.assertGreater(p.max_tokens, 0)
        env = {"BENCH_PROXY_MAX_REQUESTS": "7", "BENCH_PROXY_MAX_TOKENS": "1234"}
        with mock.patch.dict(os.environ, env):
            q = ModelProxy(self.dir / "x.sock", "r", TOKEN, upstream="http://127.0.0.1:9")
            self.assertEqual((q.max_requests, q.max_tokens), (7, 1234))
            explicit = ModelProxy(self.dir / "x.sock", "r", TOKEN, upstream="http://127.0.0.1:9", max_requests=3, max_tokens=5)
            self.assertEqual((explicit.max_requests, explicit.max_tokens), (3, 5))
        for name, value in (("BENCH_PROXY_MAX_REQUESTS", "viele"), ("BENCH_PROXY_MAX_TOKENS", "-1"), ("BENCH_PROXY_MAX_REQUESTS", "0")):
            with mock.patch.dict(os.environ, {name: value}), self.assertRaises(ValueError, msg=f"{name}={value}"):
                ModelProxy(self.dir / "x.sock", "r", TOKEN, upstream="http://127.0.0.1:9")

    def test_a_slow_client_is_cut_off(self):
        """m2: Weder eine halbe Anfragezeile noch ein halber Rumpf hält eine Verbindung ewig offen."""
        self.proxy.stop()
        self.proxy = self.make(read_timeout=0.5)
        for partial in (b"POST /v1/messages HTTP/1.0\r\n",
                        b"POST /v1/messages HTTP/1.0\r\nauthorization: Bearer " + TOKEN.encode()
                        + b"\r\ncontent-length: 100\r\n\r\n{\"model\"") :
            t0 = time.time()
            s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            s.settimeout(8)
            s.connect(str(self.dir / "m.sock"))
            s.sendall(partial)
            data = b""
            try:
                while chunk := s.recv(4096):
                    data += chunk
            except socket.timeout:
                self.fail("der Proxy hält die Verbindung eines langsamen Clients offen")
            finally:
                s.close()
            self.assertLess(time.time() - t0, 5)
            self.assertTrue(data == b"" or data.startswith(b"HTTP/1.0 408"), data[:60])
        self.assertEqual(FakeUpstream.seen, [])

    def test_connections_are_capped(self):
        """m2: Mehr als max_connections gleichzeitige Verbindungen bekommen 503, ohne einen weiteren Thread."""
        self.proxy.stop()
        self.proxy = self.make(max_connections=2, read_timeout=30)
        idle = []
        for _ in range(2):
            s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            s.connect(str(self.dir / "m.sock"))
            idle.append(s)
        self.addCleanup(lambda: [s.close() for s in idle])
        reply = self.raw(b"GET /_bench/ping HTTP/1.0\r\nauthorization: Bearer " + TOKEN.encode() + b"\r\n\r\n")
        self.assertTrue(reply.startswith(b"HTTP/1.0 503"), reply[:60])
        idle.pop().close()  # ein Platz wird frei
        for _ in range(40):
            reply = self.raw(b"GET /_bench/ping HTTP/1.0\r\nauthorization: Bearer " + TOKEN.encode() + b"\r\n\r\n")
            if reply.startswith(b"HTTP/1.0 200"):
                break
            time.sleep(0.05)
        self.assertTrue(reply.startswith(b"HTTP/1.0 200"), reply[:60])

    def test_a_broken_pipe_is_handled_quietly(self):
        """n7: Der Agent bricht die Verbindung ab: kein Traceback auf stderr, andere Fehler bleiben sichtbar."""
        import contextlib
        import io
        server = self.proxy._server
        for exc in (BrokenPipeError, ConnectionResetError):
            err = io.StringIO()
            with contextlib.redirect_stderr(err):
                try:
                    raise exc()
                except exc:
                    server.handle_error(None, ("unix", 0))
            self.assertEqual(err.getvalue(), "", exc)
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            try:
                raise RuntimeError("echter Fehler")
            except RuntimeError:
                server.handle_error(None, ("unix", 0))
        self.assertIn("echter Fehler", err.getvalue())


if __name__ == "__main__":
    unittest.main()
