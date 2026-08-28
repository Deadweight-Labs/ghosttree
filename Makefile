build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/ctx ./cmd/ctx

build-all:
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -trimpath -o dist/ctx-linux-amd64  ./cmd/ctx
	CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go build -trimpath -o dist/ctx-linux-arm64  ./cmd/ctx
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -o dist/ctx-darwin-amd64 ./cmd/ctx
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -o dist/ctx-darwin-arm64 ./cmd/ctx

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

.PHONY: build build-all test fmt-check tidy-check verify
