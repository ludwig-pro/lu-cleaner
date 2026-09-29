BINARY  := lu-cleaner
PKG     := ./cmd/lu-cleaner
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOBIN   := $(shell go env GOPATH)/bin

# cgo is on by default on macOS (FSEvents for the persistent size cache, see
# internal/fsevents); binaries run on macOS 12+ like Go itself. Without a C
# toolchain the go command disables cgo: the build still works, without the
# cache.
export MACOSX_DEPLOYMENT_TARGET ?= 12.0

.PHONY: all build install test vet fmt lint nocgo universal clean run docs site-dev site-build

all: build

## build: compile bin/lu-cleaner
build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(PKG)

## install: install lu-cleaner into $(go env GOPATH)/bin
install:
	GOBIN=$(GOBIN) go install -ldflags "$(LDFLAGS)" $(PKG)
	@echo "Installed $(GOBIN)/$(BINARY) ($(VERSION))"
	@echo "Tip: add a short alias to your ~/.zshrc:  alias luc=lu-cleaner"

## test: run the unit tests
test:
	go test ./...

## vet: go vet
vet:
	go vet ./...

## fmt: gofmt every file
fmt:
	gofmt -w .

## lint: go vet + gofmt check
lint: vet
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

## nocgo: check that everything builds and vets without cgo (no FSEvents: no size cache)
nocgo:
	CGO_ENABLED=0 go build ./...
	CGO_ENABLED=0 go vet ./...

## universal: bin/lu-cleaner-universal (arm64 + x86_64, cgo), like the release
universal:
	CGO_ENABLED=1 CC=clang GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-arm64 $(PKG)
	CGO_ENABLED=1 CC=clang GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-amd64 $(PKG)
	lipo -create -output bin/$(BINARY)-universal bin/$(BINARY)-arm64 bin/$(BINARY)-amd64
	rm bin/$(BINARY)-arm64 bin/$(BINARY)-amd64

## clean: remove build outputs
clean:
	rm -rf bin

## run: build then run (make run ARGS="scan --summary")
run: build
	./bin/$(BINARY) $(ARGS)

## docs: regenerate the reference pages of the documentation site from the CLI
docs:
	go run $(PKG) gen-docs --out site/src/content/docs/reference

## site-dev: run the documentation site locally (http://localhost:4321/lu-cleaner/)
site-dev: docs
	cd site && npm install && npm run dev

## site-build: build the documentation site into site/dist
site-build: docs
	cd site && npm ci && npm run build
