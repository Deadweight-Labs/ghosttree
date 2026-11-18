// The pairing page opens the installer's loopback in a second window; it only follows the
// installer's own callback (static/join.js), whatever address the server names.
import test from "node:test";
import assert from "node:assert/strict";
import { loadPage } from "./dom.mjs";

const target = () => loadPage(`<main></main>`, { scripts: ["join.js"] }).window.GTJoin.loopbackTarget;

test("the installer's callback is followed", () => {
  const ok = target();
  assert.equal(ok("http://127.0.0.1:40123/callback?code=ABC&state=xyz"), true);
  assert.equal(ok("http://127.0.0.1:1024/callback?error=access_denied&state=xyz"), true);
  assert.equal(ok("http://127.0.0.1:65535/callback?state=xyz"), true);
});

test("any other address on this machine is not", () => {
  const ok = target();
  for (const bad of [
    "http://127.0.0.1:80/callback?code=A&state=b", // below 1024
    "http://127.0.0.1:1023/callback?code=A&state=b",
    "http://127.0.0.1:65536/callback?code=A&state=b",
    "http://127.0.0.1/callback?code=A&state=b", // no port
    "http://127.0.0.1:8080/admin?code=A&state=b", // another path
    "http://127.0.0.1:8080/callback/../admin?code=A&state=b",
    "http://127.0.0.1:8080/callback?code=A&state=b&redirect=http://evil.example", // more than the three keys
    "http://127.0.0.1:8080/callback?code=A&state=b#frag",
    "https://127.0.0.1:8080/callback?code=A&state=b",
    "http://localhost:8080/callback?code=A&state=b",
    "http://127.0.0.1.evil.example:8080/callback?code=A&state=b",
    "http://user@127.0.0.1:8080/callback?code=A&state=b",
    "javascript:alert(1)",
    "//127.0.0.1:8080/callback",
    "",
  ]) assert.equal(ok(bad), false, bad);
});
