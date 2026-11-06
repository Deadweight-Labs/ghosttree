.PHONY: build test vet check run

build:
	go build -o bin/ledgerly ./cmd/ledgerly

test:
	go test ./...

vet:
	go vet ./...

check: vet test
	go run ./cmd/ledgerly migrate-check

run:
	go run ./cmd/ledgerly
