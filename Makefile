VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"
BINARY  := cronmon
DIST    := dist

.PHONY: build test lint run clean \
	build-linux-amd64 build-linux-arm64 \
	build-darwin-amd64 build-darwin-arm64 \
	build-windows-amd64

## ── Local build ─────────────────────────────────────────────────────────────

build:
	go build $(LDFLAGS) -o $(BINARY) .

run: build
	./$(BINARY)

## ── Tests ───────────────────────────────────────────────────────────────────

test:
	go test -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

## ── Lint ────────────────────────────────────────────────────────────────────

lint:
	golangci-lint run ./...

## ── Clean ───────────────────────────────────────────────────────────────────

clean:
	rm -rf $(BINARY) $(DIST) coverage.out

## ── Cross-compile targets ───────────────────────────────────────────────────

build-linux-amd64:
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(DIST)/$(BINARY)-linux-amd64 .

build-linux-arm64:
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(DIST)/$(BINARY)-linux-arm64 .

build-darwin-amd64:
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(DIST)/$(BINARY)-darwin-amd64 .

build-darwin-arm64:
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(DIST)/$(BINARY)-darwin-arm64 .

build-windows-amd64:
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(DIST)/$(BINARY)-windows-amd64.exe .

build-all: build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 build-windows-amd64
