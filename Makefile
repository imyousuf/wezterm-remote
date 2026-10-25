.DEFAULT_GOAL := build

GO ?= go
BINARY := wezrelay
BUILD_DIR := bin
DIST_DIR := dist
PLATFORMS := linux-amd64 linux-arm64 darwin-amd64 darwin-arm64
GOFILES := $(shell find cmd internal web -name '*.go' -type f)

.PHONY: build install test test-race vet check fmt clean build-all checksums help $(addprefix build-,$(PLATFORMS))

## build: Build the local binary with its embedded offline UI
build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -o $(BUILD_DIR)/$(BINARY) ./cmd/wezrelay

## install: Install into GOBIN (or GOPATH/bin)
install:
	$(GO) install ./cmd/wezrelay

## test: Run the Go suite, including the PTY subprocess test
test:
	$(GO) test ./...

## test-race: Check concurrent relay behavior with the race detector
test-race:
	$(GO) test -race ./...

## vet: Run Go static analysis
vet:
	$(GO) vet ./...

## check: Validate formatting, JavaScript, Go analysis, tests and build
check: vet test-race build
	@test -z "$$(gofmt -l $(GOFILES))" || { gofmt -l $(GOFILES); exit 1; }
	node --check web/app.js

## fmt: Format application Go sources
fmt:
	gofmt -w $(GOFILES)

# Cross-compilation needs no C compiler or frontend build. Each archive includes
# the binary and distribution notices, and retains executable permissions.
$(addprefix build-,$(PLATFORMS)): build-%:
	@mkdir -p $(DIST_DIR)/$*
	CGO_ENABLED=0 GOOS=$(word 1,$(subst -, ,$*)) GOARCH=$(word 2,$(subst -, ,$*)) $(GO) build -trimpath -ldflags='-s -w' -o $(DIST_DIR)/$*/$(BINARY) ./cmd/wezrelay
	cp LICENSE README.md $(DIST_DIR)/$*/
	tar -czf $(DIST_DIR)/$(BINARY)-$*.tar.gz -C $(DIST_DIR)/$* $(BINARY) LICENSE README.md
	@rm -rf $(DIST_DIR)/$*

## build-all: Package Linux/macOS amd64/arm64 binaries with SHA-256 checksums
build-all: $(addprefix build-,$(PLATFORMS))
	$(MAKE) checksums

checksums:
	cd $(DIST_DIR) && { if command -v sha256sum >/dev/null 2>&1; then sha256sum $(addprefix $(BINARY)-,$(addsuffix .tar.gz,$(PLATFORMS))); else shasum -a 256 $(addprefix $(BINARY)-,$(addsuffix .tar.gz,$(PLATFORMS))); fi; } > checksums.txt

## clean: Remove generated binaries and archives
clean:
	rm -rf $(BUILD_DIR) $(DIST_DIR)

## help: Show available targets
help:
	@sed -n 's/^## /  /p' $(MAKEFILE_LIST)
