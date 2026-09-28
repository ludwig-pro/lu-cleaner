BINARY  := lu-cleaner
PKG     := ./cmd/lu-cleaner
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOBIN   := $(shell go env GOPATH)/bin

.PHONY: all build install test vet fmt lint clean run

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

## clean: remove build outputs
clean:
	rm -rf bin

## run: build then run (make run ARGS="scan --summary")
run: build
	./bin/$(BINARY) $(ARGS)
