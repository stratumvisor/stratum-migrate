SHELL := /bin/sh

VERSION ?= 1.0.0
GO ?= go
DIST := dist
LDFLAGS := -s -w -buildid= -X main.version=$(VERSION)

.PHONY: all build test vet clean release checksums

all: build

build:
	mkdir -p $(DIST)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o $(DIST)/stratum-migrate ./cmd/stratum-migrate

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

release: test vet clean
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o $(DIST)/stratum-migrate-linux-amd64 ./cmd/stratum-migrate
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o $(DIST)/stratum-migrate-linux-arm64 ./cmd/stratum-migrate
	$(MAKE) checksums

checksums:
	cd $(DIST) && sha256sum stratum-migrate-linux-amd64 stratum-migrate-linux-arm64 > SHA256SUMS

clean:
	rm -rf $(DIST)
