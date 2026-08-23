build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/ctx ./cmd/ctx

build-all:
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -trimpath -o dist/ctx-linux-amd64  ./cmd/ctx
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -o dist/ctx-darwin-arm64 ./cmd/ctx

test:
	go test ./...

.PHONY: build build-all test
