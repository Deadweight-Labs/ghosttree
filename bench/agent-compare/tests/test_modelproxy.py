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
import unittest
from pathlib import Path

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

    def test_socket_is_private(self):
        self.assertEqual(os.stat(self.dir / "m.sock").st_mode & 0o077, 0)


if __name__ == "__main__":
    unittest.main()
