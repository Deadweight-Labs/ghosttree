# Deployment

## Server

The unit runs with `DynamicUser=yes` and `StateDirectory=ghosttree`, so systemd
owns `/var/lib/ghosttree` (it migrates the directory to `/var/lib/private/`
on first start and fixes ownership).

```bash
make build-all
scp dist/ctx-linux-amd64 deploy/ghosttree.service <host>:/tmp/
ssh <host> 'sudo install -m755 /tmp/ctx-linux-amd64 /usr/local/bin/ctx
            sudo cp /tmp/ghosttree.service /etc/systemd/system/
            sudo systemctl daemon-reload
            sudo mkdir -p /var/lib/ghosttree'
```

Create the tokens **before the first start**. A root-owned write into the state
directory while the service is running would leave WAL files the dynamic user
cannot write; systemd only fixes ownership at start time.

```bash
ssh <host> 'sudo /usr/local/bin/ctx person add <name> --db /var/lib/ghosttree/ghosttree.db'
ssh <host> 'sudo systemctl enable --now ghosttree'
curl -s http://<private-host>:8474/api/health
```

## Session distillation

Distillation is deliberately a separate timer, and it spends money, so it is
enabled last. The unit reads its provider configuration from
`/etc/ghosttree/llm.json` and its key from a systemd credential, which keeps
the secret out of both the unit file and the state directory:

`api_key_file` is listed alongside the credential so the same configuration
works when run by hand, where there is no `$CREDENTIALS_DIRECTORY`:

```bash
sudo install -d -m755 /etc/ghosttree
sudo tee /etc/ghosttree/llm.json >/dev/null <<'JSON'
{"format":"openai","base_url":"https://llm.example.invalid/v1","model":"test-model",
 "credential":"llm-key","api_key_file":"/etc/ghosttree/llm-key"}
JSON
sudo install -m640 -o root -g ghosttree /dev/stdin /etc/ghosttree/llm-key <<<'sk-...'
```

### Running a command by hand

Not with `sudo -u ghosttree`, and not with `setpriv` either. The service user is
a DynamicUser: it exists for NSS only while the unit runs, `sudo` will not
resolve it by number, and the state directory lives under `/var/lib/private`,
which is root-only — the path only becomes reachable through the bind mount
systemd sets up for the unit. Rebuild that identity transiently instead:

```bash
sudo systemd-run --pipe --wait --collect --quiet \
  -p DynamicUser=yes -p User=ghosttree -p StateDirectory=ghosttree \
  -p LoadCredential=llm-key:/etc/ghosttree/llm-key \
  -p Environment=GHOSTTREE_LLM_CONFIG=/etc/ghosttree/llm.json \
  /usr/local/bin/ctx <command> --db /var/lib/ghosttree/ghosttree.db ...
```

`-p User=ghosttree` alongside `DynamicUser=yes` is what matters: systemd derives
the uid from the user name, not from the unit name, so the transient unit gets
the same uid as `ghosttree.service`. Without it the uid differs and starting the
transient unit rewrites the state directory's ownership, which breaks the
server. Reading needs none of this:
`sudo sqlite3 -readonly "file:/var/lib/private/ghosttree/ghosttree.db?immutable=1"`.

### The two halves

The batch endpoint answers up to 24 hours later, in a different process run.
`--submit` sends what is pending and records the batch; `--collect` ingests
whatever has finished. The unit calls both, collect first. `--dry-run` prices a
submission before it happens, and `--project` confines it to one repository.

`ctx cost` reports what has been billed and forecasts the rest. It covers the
batch path only — the synchronous path and `ctx migrate` never see a token
count.

Improving the extraction prompt means bumping `sessiondistill.PromptVersion`.
That alone changes nothing about work already done; `--reprocess-version <old>`
is the deliberate act that puts those sessions back in the queue, and it refuses
to release the current version.

```bash
sudo cp deploy/ghosttree-distill.{service,timer} /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now ghosttree-distill.timer
```

Generated items are quarantined until review, but the bill is not: measure one
project before letting the timer loose on the whole archive.

To add a person later, stop the service, run `person add` against
`/var/lib/private/ghosttree/ghosttree.db`, then start it again.

`ExecStart` binds the private network address explicitly because the host also has a
LAN interface. Check with `ip -4 -o addr show` before deploying elsewhere and
adjust the address.

## Client machines

```bash
install -m755 dist/ctx-linux-amd64 ~/.local/bin/ctx
ctx setup --server http://<private-host>:8474 --token <token>
ctx install claude
ctx install codex
ctx watch --once          # first import, takes a while on a large history
cp deploy/ghosttree-watch.service ~/.config/systemd/user/
systemctl --user enable --now ghosttree-watch
ctx status
```

Claude Code reads MCP servers from `$CLAUDE_CONFIG_DIR/.claude.json`, falling
back to `~/.claude.json` — **not** from `settings.json`, which ignores an
`mcpServers` key (verified on 2.1.234). If some launchers set
`CLAUDE_CONFIG_DIR` and others do not, run the installer twice so both files
carry the registration:

```bash
ctx install claude
CLAUDE_CONFIG_DIR= ctx install claude
```

## Sharing with a second person

1. Share the host in the private network admin console.
2. `ctx person add <name>` for a second token (see the stop/start note above).
3. They run `ctx setup` and `ctx install` on their machine.

## From a local single-machine server to a networked host

`ghosttree-setup` can start a server on the machine you are already working on:
a database under `~/.local/share/ghosttree/`, `ctx serve` bound to loopback,
`ctx person add` for a token. That is a complete ghosttree for one machine, and
it is the right first step when nobody has a host yet.

Moving it to a host later is logically not a migration — it is the same SQLite
file, and the schema creates itself on open. It is also **not a `cp`**.

A running SQLite database in WAL mode is more than one file: recent writes sit
in a sidecar journal that has not been folded back yet. Copying `ghosttree.db`
on its own gives you a snapshot missing them, and it will look fine until it
does not. Two correct ways:

```bash
# stop it, then copy
systemctl --user stop ghosttree
cp ~/.local/share/ghosttree/ghosttree.db /tmp/

# or snapshot it while it runs — the same mechanism ctx uses before a schema rebuild
sqlite3 ~/.local/share/ghosttree/ghosttree.db "VACUUM INTO '/tmp/ghosttree.db'"
```

Then follow **Server** above, putting the copied file in place before the first
start (the same ownership caveat applies: no root-owned writes into the state
directory while the service runs). Afterwards point every machine at the new
URL with `ctx setup` and stop the local service. Nothing else changes.

There is deliberately no skill for this step. It configures a host nobody in
the session has inspected, over ssh, and that is not a risk any amount of
onboarding convenience pays for.
