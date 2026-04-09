VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT)
BINARY  := hermai

.PHONY: build test test-cover vet lint install clean doctor help

## build: compile the binary with version info
build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/hermai/

## test: run all tests
test:
	go test ./... -count=1

## test-cover: run tests with coverage report
test-cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out
	@rm -f coverage.out

## vet: run go vet
vet:
	go vet ./...

## lint: run vet (add staticcheck/golangci-lint when available)
lint: vet

## install: install the binary to $GOPATH/bin
install:
	go install -ldflags "$(LDFLAGS)" ./cmd/hermai/

## clean: remove build artifacts
clean:
	rm -f $(BINARY) $(BINARY).exe coverage.out

## doctor: build and run self-diagnostics
doctor: build
	./$(BINARY) doctor

## help: show this help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | column -t -s ':'
