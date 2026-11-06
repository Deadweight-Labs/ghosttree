# Building and testing

For the contribution process see [CONTRIBUTING.md](../CONTRIBUTING.md).

Ghosttree currently targets Go 1.26.

```bash
make test
make build
make build-all
make verify    # formatting, vet, race tests, and all release builds
```

The direct dependencies are `modernc.org/sqlite`, the official MCP Go SDK,
`fsnotify`, `go-oidc`, and the `golang.org/x` modules `oauth2`, `sys`, and `text`. Release builds set `CGO_ENABLED=0` and target Linux and macOS on
amd64 and arm64.
