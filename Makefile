BINARY := ringly-relay
PKG := ./cmd/ringly-relay
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet clean dist

## build: собрать бинарник под текущую платформу
build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(PKG)

## test: юнит-тесты
test:
	go test ./...

## vet: статический анализ
vet:
	go vet ./...

## dist: кросс-компиляция под типичные VPS (воспроизводимо: CGO выключен, флаги фиксированы)
dist:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64 $(PKG)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-arm64 $(PKG)
	cd dist && shasum -a 256 $(BINARY)-* > SHA256SUMS
	@echo "Собрано в dist/ (+ SHA256SUMS для проверки воспроизводимости)"

## clean: удалить артефакты сборки
clean:
	rm -rf bin dist
