build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/ctx ./cmd/ctx

build-all:
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -trimpath -o dist/ctx-linux-amd64  ./cmd/ctx
	CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go build -trimpath -o dist/ctx-linux-arm64  ./cmd/ctx
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -o dist/ctx-darwin-amd64 ./cmd/ctx
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -o dist/ctx-darwin-arm64 ./cmd/ctx

# Release archives + checksums.txt for GHOSTTREE_DIST_DIR (see scripts/build-dist.sh).
dist:
	scripts/build-dist.sh dist/release

test:
	go test ./...

fmt-check:
	test -z "$$(find cmd internal skills -name '*.go' -type f -print0 | xargs -0 gofmt -l)"

tidy-check:
	go mod tidy
	git diff --exit-code -- go.mod go.sum

verify: fmt-check tidy-check
	go vet ./...
	go test -race -count=1 ./...
	$(MAKE) build-all

storebench-small:
	@set -eu; task_dir=$$(mktemp -d); \
	go run ./cmd/storebench --backend=current --preset=small --output=$$task_dir/current.json; \
	go run ./cmd/storebench --backend=queued --preset=small --output=$$task_dir/queued.json; \
	echo "reports: $$task_dir"

# Tests des Benchmark-Harness. Tests mit Docker (Image: bench/agent-compare/image/build.sh) oder bubblewrap
# werden übersprungen, wenn beides fehlt.
bench-test:
	python3 -m unittest discover -s bench/agent-compare/tests -p 'test_*.py'

.PHONY: build build-all dist test fmt-check tidy-check verify storebench-small bench-test
