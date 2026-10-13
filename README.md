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
| `VIBEGUARD_MDNS_UPSTREAM` | unset (disabled) | Upstream/LAN interface (e.g. `eth0`). Enables the mDNS repeater |

Put the UI behind HTTPS (e.g. a reverse proxy) when exposing it beyond a trusted network.


## mDNS repeater

With `VIBEGUARD_MDNS_UPSTREAM` set, mDNS (224.0.0.251:5353) is reflected between that interface and all
running WireGuard services, so VPN clients can resolve `.local` names:
queries from VPN users are re-sent on the upstream network, and mDNS traffic from the upstream is
forwarded as unicast to each VPN user (and between VPN users). Clients must route traffic to
`224.0.0.251` through the tunnel (the default `0.0.0.0/0` allowed IPs do; otherwise add `224.0.0.251/32`).
Requires `NET_ADMIN` and host networking (`network_mode: host` in Docker) so the LAN interface is visible.

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
