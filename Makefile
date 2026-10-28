BINARY := ringly-relay
PKG := ./cmd/ringly-relay
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet clean dist

## build: build the binary for the current platform
build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(PKG)

## test: unit tests
test:
	go test ./...

## vet: static analysis
vet:
	go vet ./...

## dist: cross-compile for typical VPSes (reproducible: CGO off, fixed flags)
dist:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64 $(PKG)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-arm64 $(PKG)
	cd dist && shasum -a 256 $(BINARY)-* > SHA256SUMS
	@echo "Built into dist/ (+ SHA256SUMS to verify reproducibility)"

## clean: remove build artifacts
clean:
	rm -rf bin dist
