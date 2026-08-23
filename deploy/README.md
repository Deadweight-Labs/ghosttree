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
