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

```bash
sudo install -d -m755 /etc/ghosttree
sudo tee /etc/ghosttree/llm.json >/dev/null <<'JSON'
{"format":"openai","base_url":"https://llm.example.invalid/v1","model":"test-model","credential":"llm-key"}
JSON
sudo install -m600 /dev/stdin /etc/ghosttree/llm-key <<<'sk-...'
```

The command has two halves because the batch endpoint answers up to 24 hours
later, in a different process run. `--submit` sends what is pending and records
the batch; `--collect` ingests whatever has finished. The unit calls both, and
`--dry-run` prices a submission before it happens:

```bash
sudo systemctl stop ghosttree     # never write the database while it runs
sudo -u '#63386' /usr/local/bin/ctx distill-sessions \
     --db /var/lib/private/ghosttree/ghosttree.db \
     --project github.com/<owner>/<repo> --limit 50 --submit --dry-run
```

Enable the timer only after one measured run: generated items are quarantined
until review, but the bill is not.

```bash
sudo cp deploy/ghosttree-distill.{service,timer} /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now ghosttree-distill.timer
```

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
