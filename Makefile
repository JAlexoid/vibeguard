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
POLKITDIR ?= /etc/polkit-1/rules.d
LIBEXECDIR ?= $(PREFIX)/libexec
ENV_FILE := /etc/vibeguard.env
DATA_DIR := /var/lib/vibeguard
SYSCTL   := /etc/sysctl.d/99-vibeguard.conf
# Service user; prompted for by `make install` when unset (default: vibeguard).
VIBEGUARD_USER ?=

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
        install uninstall check-polkit \
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

run: ## Run locally (systemd/polkit, or CAP_NET_ADMIN for direct mode)
	$(GO) run .

clean: ## Remove build outputs
	rm -rf $(BINARY) $(DIST)

## --- systemd install -------------------------------------------------------

install: $(BINARY) ## Install binary + systemd unit and start it (run as root)
ifeq ($(DESTDIR),)
	@command -v sysctl >/dev/null || { echo "Install procps (sysctl) first" >&2; exit 1; }
	@command -v iptables >/dev/null && command -v ip6tables >/dev/null || { echo "Install iptables (IPv4 and IPv6 tools) first" >&2; exit 1; }
	@command -v avahi-daemon >/dev/null || { echo "Install avahi-daemon first" >&2; exit 1; }
	@command -v wg-quick >/dev/null || { echo "Install wireguard-tools first" >&2; exit 1; }
	@command -v pkaction >/dev/null && command -v pkcheck >/dev/null || { echo "Missing polkitd: install polkitd (including pkaction and pkcheck) first" >&2; exit 1; }
	@systemctl start polkit.service || { echo "Cannot start polkit.service; check that polkitd is installed and inspect its journal" >&2; exit 1; }
	@systemctl is-active --quiet polkit.service || { echo "polkit.service must be running before installing VibeGuard" >&2; exit 1; }
	-systemctl stop vibeguard-mdns-firewall.service
endif
	install -Dm755 $(BINARY) $(DESTDIR)$(BINDIR)/vibeguard
	@mkdir -p $(DIST)
	@user='$(VIBEGUARD_USER)'; \
	if [ -z "$$user" ] && [ -t 0 ]; then printf 'Run VibeGuard as user [vibeguard]: '; read -r user; fi; \
	user=$${user:-vibeguard}; \
	case "$$user" in ''|-*|*[!a-z0-9_-]*) echo "invalid user name: $$user" >&2; exit 1;; esac; \
	if [ -z "$(DESTDIR)" ] && ! id -u "$$user" >/dev/null 2>&1; then \
	  echo "Creating system user $$user"; \
	  useradd --system --user-group --home-dir $(DATA_DIR) --no-create-home \
	    --shell "$$(command -v nologin || echo /bin/false)" "$$user" || exit 1; \
	fi; \
	echo "Service user: $$user"; \
	sed -e 's|/usr/local/bin/vibeguard|$(BINDIR)/vibeguard|' -e "s|^User=.*|User=$$user|" \
	  lxc/vibeguard.service > $(DIST)/vibeguard.service; \
	  sed "s|subject.user !== \"vibeguard\"|subject.user !== \"$$user\"|" systemd/50-vibeguard.rules > $(DIST)/50-vibeguard.rules
	install -Dm644 $(DIST)/vibeguard.service $(DESTDIR)$(UNITDIR)/vibeguard.service
	sed 's|/usr/local/bin/vibeguard|$(BINDIR)/vibeguard|g' systemd/vibeguard-avahi.service > $(DIST)/vibeguard-avahi.service
	install -Dm644 $(DIST)/vibeguard-avahi.service $(DESTDIR)$(UNITDIR)/vibeguard-avahi.service
	sed 's|/usr/local/libexec|$(LIBEXECDIR)|g' systemd/vibeguard-wg-quick > $(DIST)/vibeguard-wg-quick
	install -Dm755 $(DIST)/vibeguard-wg-quick $(DESTDIR)$(LIBEXECDIR)/vibeguard-wg-quick
	install -Dm755 systemd/vibeguard-network $(DESTDIR)$(LIBEXECDIR)/vibeguard-network
	@test -e $(DESTDIR)/etc/vibeguard-network.env || install -Dm644 systemd/vibeguard-network.env $(DESTDIR)/etc/vibeguard-network.env
	sed 's|/usr/local/libexec|$(LIBEXECDIR)|g' systemd/wg-quick-vibeguard.conf > $(DIST)/wg-quick-vibeguard.conf
	install -Dm644 $(DIST)/wg-quick-vibeguard.conf $(DESTDIR)$(UNITDIR)/wg-quick@.service.d/vibeguard.conf
	install -Dm644 $(DIST)/50-vibeguard.rules $(DESTDIR)$(POLKITDIR)/50-vibeguard.rules
	install -Dm644 /dev/null $(DESTDIR)$(SYSCTL)
	printf 'net.ipv4.ip_forward=1\nnet.ipv6.conf.all.forwarding=1\n' > $(DESTDIR)$(SYSCTL)
ifeq ($(DESTDIR),)
	sh systemd/check-polkit "$$(sed -n 's/^User=//p' $(DIST)/vibeguard.service)"
	sh lxc/vibeguard-firstboot $(ENV_FILE)
	sysctl -q -p $(SYSCTL)
	-systemctl disable --now vibeguard-mdns-firewall.service
	rm -f $(UNITDIR)/vibeguard-mdns-firewall.service
	-systemctl disable --now avahi-daemon.socket avahi-daemon.service
	systemctl daemon-reload
	systemctl enable vibeguard.service
	systemctl restart vibeguard.service
	@command -v wg >/dev/null || echo "WARNING: wireguard-tools (wg) not found; install it."
	@echo "VibeGuard installed. Admin password is in $(ENV_FILE); UI on :8080 by default."
endif

check-polkit: ## Check installed polkit authorization without changing any units (run as root)
	sh systemd/check-polkit $(if $(VIBEGUARD_USER),$(VIBEGUARD_USER),vibeguard)

uninstall: ## Stop and remove the binary + unit (keeps data and password)
ifeq ($(DESTDIR),)
	-systemctl disable --now vibeguard.service
endif
	# Keep the wg-quick helper/drop-in: enabled tunnels must still start and stop
	# after uninstalling the UI. Disable those units before removing these files.
	rm -f $(DESTDIR)$(BINDIR)/vibeguard $(DESTDIR)$(UNITDIR)/vibeguard.service $(DESTDIR)$(UNITDIR)/vibeguard-avahi.service $(DESTDIR)$(SYSCTL) $(DESTDIR)$(POLKITDIR)/50-vibeguard.rules
ifeq ($(DESTDIR),)
	systemctl daemon-reload
	@echo "Kept $(DATA_DIR), $(ENV_FILE) and the service user; remove them manually to wipe all state."
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
