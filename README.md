# VibeGuard

Simple WireGuard management UI: manage WireGuard services (interfaces), users (peers) and
generate QR codes for easy client setup. Single admin password, no other accounts.

Stack: one Go binary (standard library + `go-qrcode`) serving a REST API and a small
dependency-free HTML/JS frontend (embedded). Uses `wg` / `wg-quick` on the host.

## Configuration (environment variables)

| Variable | Default | Description |
|---|---|---|
| `VIBEGUARD_ADMIN_PASSWORD` | required | Admin password |
| `VIBEGUARD_LISTEN` | `:8080` | Listen address |
| `VIBEGUARD_DATA` | `/var/lib/vibeguard/data.json` | State file (contains keys, keep private) |
| `VIBEGUARD_WG_DIR` | `/etc/wireguard` | Where `<name>.conf` files are written |

Put the UI behind HTTPS (e.g. a reverse proxy) when exposing it beyond a trusted network.

## Run

    VIBEGUARD_ADMIN_PASSWORD=secret go run .      # needs root + wireguard-tools to start services

## Docker

    docker compose up -d --build    # edit VIBEGUARD_ADMIN_PASSWORD in docker-compose.yml first

Requires `NET_ADMIN` (set in the compose file) and the WireGuard kernel module on the host.
Use `network_mode: host` or publish the UDP port of each service you create.

## LXC (Proxmox)

    ./lxc/vibeguard.sh <CTID> <admin-password> [storage]

Creates a Debian 12 container with TUN access, builds VibeGuard and installs the
`lxc/vibeguard.service` systemd unit. UI is at `http://<container-ip>:8080`.

## Test

    go test ./...
