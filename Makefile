VERSION ?= $(shell date +%Y.%m.%d)-dev
LDFLAGS := -s -w -X github.com/KalonEdge/kalonide/internal/version.Version=$(VERSION)
PREFIX  ?= /usr/local

.PHONY: build test race vet install clean bench bench-validate

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o kalonide ./cmd/kalonide

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

install: build
	ln -sf "$(CURDIR)/kalonide" "$(PREFIX)/bin/kalonide"

BENCH_MODEL ?= gemma4:4b

bench: build
	go run ./bench -model "$(BENCH_MODEL)" -report

bench-validate:
	go run ./bench -validate

clean:
	go clean
	rm -f kalonide
