.PHONY: test race vet build hostcheck hostcheck-known-bug sdkrepro check-plugin-dir
PLUGIN := build/opencode-v0.1.0.so

test:
	go test ./... -count=1
	bash scripts/check-plugin-directory-test.sh
race:
	go test -race ./... -count=1
vet:
	go vet ./...
build:
	mkdir -p build
	CGO_ENABLED=1 go build -buildmode=c-shared -o $(PLUGIN) .
hostcheck: build
	go -C cmd/hostcheck run . --quiesce $(abspath build)
	go -C cmd/hostcheck run . --cancel-first $(abspath build)
hostcheck-known-bug: build
	go -C cmd/hostcheck run . --disable $(abspath build)
sdkrepro:
	go run ./cmd/sdkrepro

# Read-only deployment preflight; override PLUGIN_DIR for the actual host directory.
PLUGIN_DIR ?= build
check-plugin-dir:
	bash scripts/check-plugin-directory.sh "$(PLUGIN_DIR)"
