# VibeGuard build, install and packaging.
# Run `make help` for the list of targets.

BINARY  := vibeguard
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DIST    := dist

GO      ?= go
GOFLAGS ?= -trimpath
LDFLAGS ?= -s -w
GO_SRC  := $(wildcard *.go) go.mod go.sum $(wildcard web/*)

# Install locations (DESTDIR is honoured for staged/package installs).
PREFIX   ?= /usr/local
BINDIR   ?= $(PREFIX)/bin
UNITDIR  ?= /etc/systemd/system
ENV_FILE := /etc/vibeguard.env
DATA_DIR := /var/lib/vibeguard
SYSCTL   := /etc/sysctl.d/99-vibeguard.conf

# LXC template (Proxmox vztmpl-compatible rootfs tarball).
LXC_ARCH  ?= amd64
LXC_IMAGE := vibeguard-lxc:$(VERSION)
ifneq ($(shell command -v zstd 2>/dev/null),)
LXC_EXT      := zst
LXC_COMPRESS := zstd -q -19 -T0
else
LXC_EXT      := gz
LXC_COMPRESS := gzip -9
endif
LXC_TEMPLATE ?= $(DIST)/vibeguard-debian-12_$(VERSION)_$(LXC_ARCH).tar.$(LXC_EXT)

.PHONY: all build test vet fmt run clean help \
        install uninstall \
        docker-build docker-up docker-down \
        lxc-template

all: build ## Build the binary (default)

build: $(BINARY) ## Build the vibeguard binary

$(BINARY): $(GO_SRC)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $@ .

test: ## Run unit tests
	$(GO) test ./...

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Format Go sources
	gofmt -w $(wildcard *.go)

run: ## Run locally (needs root and VIBEGUARD_ADMIN_PASSWORD)
	$(GO) run .

clean: ## Remove build outputs
	rm -rf $(BINARY) $(DIST)

## --- systemd install -------------------------------------------------------

install: $(BINARY) ## Install binary + systemd unit and start it (run as root)
	install -Dm755 $(BINARY) $(DESTDIR)$(BINDIR)/vibeguard
	@mkdir -p $(DIST)
	sed 's|/usr/local/bin/vibeguard|$(BINDIR)/vibeguard|' lxc/vibeguard.service > $(DIST)/vibeguard.service
	install -Dm644 $(DIST)/vibeguard.service $(DESTDIR)$(UNITDIR)/vibeguard.service
	install -d -m700 $(DESTDIR)$(DATA_DIR)
	install -Dm644 /dev/null $(DESTDIR)$(SYSCTL)
	echo 'net.ipv4.ip_forward=1' > $(DESTDIR)$(SYSCTL)
ifeq ($(DESTDIR),)
	sh lxc/vibeguard-firstboot $(ENV_FILE)
	sysctl -q -p $(SYSCTL)
	systemctl daemon-reload
	systemctl enable --now vibeguard.service
	@command -v wg >/dev/null || echo "WARNING: wireguard-tools (wg, wg-quick) not found; install them."
	@echo "VibeGuard installed. Admin password is in $(ENV_FILE); UI on :8080 by default."
endif

uninstall: ## Stop and remove the binary + unit (keeps data and password)
ifeq ($(DESTDIR),)
	-systemctl disable --now vibeguard.service
endif
	rm -f $(DESTDIR)$(BINDIR)/vibeguard $(DESTDIR)$(UNITDIR)/vibeguard.service $(DESTDIR)$(SYSCTL)
ifeq ($(DESTDIR),)
	systemctl daemon-reload
	@echo "Kept $(DATA_DIR) and $(ENV_FILE); remove them manually to wipe all state."
endif

## --- Docker ----------------------------------------------------------------

docker-build: ## Build the Docker image
	docker build -t vibeguard:$(VERSION) -t vibeguard:latest .

docker-up: ## Start with docker compose
	docker compose up -d --build

docker-down: ## Stop docker compose
	docker compose down

## --- LXC -------------------------------------------------------------------

lxc-template: ## Build a Proxmox LXC template in dist/ (needs Docker)
	@mkdir -p $(DIST)
	docker build --platform linux/$(LXC_ARCH) -f lxc/Dockerfile -t $(LXC_IMAGE) .
	cid=$$(docker create --platform linux/$(LXC_ARCH) $(LXC_IMAGE) /sbin/init) && \
	  docker export -o $(DIST)/rootfs.tar $$cid; rc=$$?; docker rm $$cid >/dev/null; exit $$rc
	tar --delete -f $(DIST)/rootfs.tar .dockerenv
	$(LXC_COMPRESS) < $(DIST)/rootfs.tar > $(LXC_TEMPLATE)
	rm -f $(DIST)/rootfs.tar
	@echo "Template: $(LXC_TEMPLATE)"

help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
