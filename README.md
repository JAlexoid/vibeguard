# VibeGuard

Simple WireGuard management UI: manage WireGuard services (interfaces), users (peers) and
generate QR codes for easy client setup. Single admin password, no other accounts.

Stack: one Go binary (standard library + `go-qrcode`) serving a REST API and a small
dependency-free HTML/JS frontend (embedded). Uses `wg` and `ip` on the host (no `wg-quick`), so it
doesn't need root, only the `CAP_NET_ADMIN` capability.

## Configuration (environment variables)

| Variable | Default | Description |
|---|---|---|
| `VIBEGUARD_ADMIN_PASSWORD` | required | Admin password |
| `VIBEGUARD_LISTEN` | `:8080` | Listen address |
| `VIBEGUARD_DATA` | `/var/lib/vibeguard/data.json` | State file (contains keys, keep private) |
| `VIBEGUARD_WG_DIR` | `/etc/wireguard` | Where `<name>.conf` files are written (the systemd unit sets `/var/lib/vibeguard/wireguard`) |
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

    VIBEGUARD_ADMIN_PASSWORD=secret go run .      # needs root (or CAP_NET_ADMIN) + wireguard-tools to start services

## Docker

    docker compose up -d --build    # edit VIBEGUARD_ADMIN_PASSWORD in docker-compose.yml first

Requires `NET_ADMIN` (set in the compose file) and the WireGuard kernel module on the host.
Use `network_mode: host` or publish the UDP port of each service you create.

## Make targets

Run `make help` for the full list.

    make                 # build ./vibeguard
    make test            # go test ./...
    sudo make install    # install as a systemd service (see below)
    sudo make uninstall  # remove binary + unit, keeps data and password
    make lxc-template    # build a Proxmox LXC template into dist/

`make install` copies the binary to `/usr/local/bin` (override with `PREFIX=`/`BINDIR=`) and the unit
to `/etc/systemd/system`. It also writes `/etc/vibeguard.env` with a random admin password if that
file doesn't exist yet, enables `net.ipv4.ip_forward`, and runs `systemctl enable --now vibeguard`.
Install `wireguard-tools` on the host first. If `DESTDIR` is set, only the files are staged.

**Service user.** `make install` asks which user to run as (default `vibeguard`) and creates it as a
system user if it doesn't exist. To skip the prompt, e.g. in scripts, pass `VIBEGUARD_USER=<name>`;
without a terminal it uses the default. The service doesn't need root: the unit grants only
`CAP_NET_ADMIN`, to manage WireGuard interfaces, and `CAP_NET_BIND_SERVICE`, so `VIBEGUARD_LISTEN`
can use a port below 1024. It also sets `NoNewPrivileges`. systemd creates `/var/lib/vibeguard` and
makes the user its owner (`StateDirectory=`); service configs are written to
`/var/lib/vibeguard/wireguard`. `/etc/vibeguard.env` stays owned by root, since systemd reads it
before dropping privileges.

    sudo make install                          # prompts, default: vibeguard
    sudo make install VIBEGUARD_USER=wgadmin   # no prompt

**Start interfaces on boot.** Set `VIBEGUARD_WG_QUICK_BOOT=1` (default: off) and starting a service in
the UI also runs `systemctl enable wg-quick@<name>`, so the interface comes up at boot; stopping or
deleting the service runs `systemctl disable`. Enabling is idempotent and doesn't start anything
itself. If `VIBEGUARD_WG_DIR` isn't `/etc/wireguard`, the config is symlinked there for wg-quick.
It is skipped silently when systemd isn't running (e.g. Docker), and failures are only logged.
`systemctl enable` and writing to `/etc/wireguard` need root, so this only works when VibeGuard runs
as root (`VIBEGUARD_USER=root`, with the unit's capability limits and `NoNewPrivileges` relaxed);
with the default unprivileged user it logs an error and the interface isn't enabled on boot.

## LXC (Proxmox)

### Prebuilt template

`make lxc-template` uses Docker to build a Debian 12 root filesystem with VibeGuard, wireguard-tools
and the systemd units preinstalled, then writes `dist/vibeguard-debian-12_<version>_amd64.tar.zst`
(or `.tar.gz` if `zstd` isn't installed; set `LXC_ARCH=arm64` for other architectures, which needs qemu binfmt).
On first boot the container generates a random admin password into `/etc/vibeguard.env`.

The result is a plain LXC system container: Debian with systemd as init, running VibeGuard as a
native systemd service. Docker is only a build-time tool on the build machine, used to assemble the
root filesystem. The template contains no Docker, containerd or other container runtime, and
neither the Proxmox host nor the container needs Docker.

See [Example deployment on Proxmox](#example-deployment-on-proxmox) for how to use it.

### Example deployment on Proxmox

Example values; change them for your setup:

| | |
|---|---|
| Proxmox host | `pve.lan` |
| Container ID | `120` |
| Storage / bridge | `local-lvm` / `vmbr0` |
| Container IP | `192.168.1.50/24`, gateway `192.168.1.1` |
| Public address | `vpn.example.com`, UDP `51820` forwarded to `192.168.1.50` |

**1. Build the template** (on a machine with Docker) and copy it to the host's template cache:

    make lxc-template
    scp dist/vibeguard-debian-12_*_amd64.tar.zst root@pve.lan:/var/lib/vz/template/cache/

Or download a published template directly on the host (see [Downloading a release](#downloading-a-release-on-proxmox)).

**2. Prepare the host** (once, as root on `pve.lan`):

    echo wireguard > /etc/modules-load.d/wireguard.conf
    modprobe wireguard

**3. Create the container:**

    T=$(basename /var/lib/vz/template/cache/vibeguard-debian-12_*_amd64.tar.zst)
    pct create 120 "local:vztmpl/$T" \
      --hostname vibeguard --ostype debian --unprivileged 1 --features nesting=1 \
      --cores 1 --memory 256 --swap 0 --rootfs local-lvm:2 \
      --net0 name=eth0,bridge=vmbr0,ip=192.168.1.50/24,gw=192.168.1.1 \
      --nameserver 192.168.1.1 --onboot 1

**4. Give it TUN access** (see [requirements](#unprivileged-container-requirements)):

    cat >> /etc/pve/lxc/120.conf <<'CONF'
    lxc.cgroup2.devices.allow: c 10:200 rwm
    lxc.mount.entry: /dev/net/tun dev/net/tun none bind,create=file
    CONF

**5. Optional: enable the mDNS repeater** before the first start, so that VPN clients can resolve
`.local` names on the LAN. Create the env file yourself (the first-boot service won't overwrite it):

    pct mount 120
    ENV=/var/lib/lxc/120/rootfs/etc/vibeguard.env
    printf 'VIBEGUARD_ADMIN_PASSWORD=%s\nVIBEGUARD_MDNS_UPSTREAM=eth0\n' "$(openssl rand -base64 18)" > "$ENV"
    chown 100000:100000 "$ENV" && chmod 600 "$ENV"   # container root = host UID 100000
    pct unmount 120

    # If you skip this step, you can add the line to /etc/vibeguard.env later and run: pct exec 120 -- systemctl restart vibeguard

**6. Start it and get the admin password:**

    pct start 120
    pct exec 120 -- cat /etc/vibeguard.env
    pct exec 120 -- systemctl is-active vibeguard     # active

**7. Configure VibeGuard** at `http://192.168.1.50:8080`: log in with the password and create a
service with listen port `51820`, endpoint `vpn.example.com:51820` and an unused tunnel subnet
(e.g. `10.8.0.1/24`). Then add users and scan their QR codes with the WireGuard mobile app.

**8. Forward the port:** on your router, forward UDP `51820` to `192.168.1.50`. Each additional
service needs its own UDP port forwarded. Keep port `8080` on the LAN only, or put it behind an HTTPS
reverse proxy.

**Updating:** build a new template, then recreate the container while keeping state. Back up
`/var/lib/vibeguard` and `/etc/vibeguard.env` first, for example with
`vzdump 120` or `pct pull`. Alternatively, copy a freshly built `vibeguard` binary in and restart:

    make && scp vibeguard root@pve.lan:/tmp/
    pct push 120 /tmp/vibeguard /usr/local/bin/vibeguard --perms 0755
    pct exec 120 -- systemctl restart vibeguard

### Publishing a release on GitHub

Releases ship the LXC template as a release asset, so Proxmox hosts can download it without Docker
or a build machine. The template's file name comes from `git describe`, so build from a clean tree
on the release tag. Otherwise the name ends in `-dirty` or a commit suffix.

Requires the [GitHub CLI](https://cli.github.com/) (`gh auth login`) and Docker.

    # 1. Tag the release on an up-to-date, clean main
    git checkout main && git pull
    git tag -a v0.1.0 -m "VibeGuard v0.1.0"

    # 2. Build and test; produces dist/vibeguard-debian-12_v0.1.0_amd64.tar.zst
    make clean test lxc-template
    # optional: make lxc-template LXC_ARCH=arm64   (needs qemu binfmt)

    # 3. Checksums
    (cd dist && sha256sum vibeguard-debian-12_* > SHA256SUMS)

    # 4. Push the tag and create the release with the assets attached
    git push origin v0.1.0
    gh release create v0.1.0 dist/vibeguard-debian-12_* dist/SHA256SUMS \
      --title "VibeGuard v0.1.0" --generate-notes

Use `--draft` to review the release before publishing it. To replace an asset on an existing release,
run `gh release upload v0.1.0 <file> --clobber`. Then regenerate and re-upload `SHA256SUMS` too.

#### Downloading a release on Proxmox

On the Proxmox host, download the template and checksums into the template cache and verify them:

    V=v0.1.0
    URL=https://github.com/JAlexoid/vibeguard/releases/download/$V
    cd /var/lib/vz/template/cache
    wget "$URL/vibeguard-debian-12_${V}_amd64.tar.zst" "$URL/SHA256SUMS"
    sha256sum --ignore-missing -c SHA256SUMS && rm SHA256SUMS

The template then shows up as `local:vztmpl/vibeguard-debian-12_v0.1.0_amd64.tar.zst`; continue with
step 2 of the [example deployment](#example-deployment-on-proxmox).

### Script

    ./lxc/vibeguard.sh <CTID> <admin-password> [storage]

Creates a Debian 12 container with TUN access, builds VibeGuard and installs the
`lxc/vibeguard.service` systemd unit. UI is at `http://<container-ip>:8080`.

### Unprivileged container requirements

An unprivileged container shares the host kernel and can't load kernel modules or create device
nodes, so a few things must be set up on the host.

**1. WireGuard kernel module (host).** VibeGuard first tries to create a kernel interface
(`ip link add <name> type wireguard`). That works inside an unprivileged container, because the
container is root in its own network namespace, but only if the host already has the module loaded.
Proxmox VE kernels include WireGuard; make sure the module loads at boot:

    echo wireguard > /etc/modules-load.d/wireguard.conf
    modprobe wireguard

**2. `/dev/net/tun` (container config).** If the kernel interface can't be created, VibeGuard falls
back to `wireguard-go` when it's installed. wireguard-go needs the TUN device (character
device major 10, minor 200). Unprivileged containers have no access to it by default, so allow the
device in the container's cgroup and bind-mount the host node into the container. Add to
`/etc/pve/lxc/<CTID>.conf`:

    lxc.cgroup2.devices.allow: c 10:200 rwm
    lxc.mount.entry: /dev/net/tun dev/net/tun none bind,create=file

- `lxc.cgroup2.devices.allow` lets the container open the device (on hosts that still use cgroup v1,
  the key is `lxc.cgroup.devices.allow`).
- `lxc.mount.entry` bind-mounts the host's `/dev/net/tun`; `create=file` creates the mount point,
  since the container can't run `mknod`. Inside the container the node shows up as owned by
  `nobody:nogroup`. That's expected: the host node is mode `0666`, so it's still usable.
- On Proxmox VE 8.1+ you can use device passthrough instead of the two raw lines:
  `pct set <CTID> --dev0 /dev/net/tun`.
- On plain LXC (not Proxmox), use the same keys with `=` instead of `:` in the container's `config`.

The template doesn't include a userspace WireGuard, so in practice step 1 is what makes it work.
Configuring TUN anyway keeps the container working if you add one later, and
`lxc/vibeguard.sh` does both.

**3. Container features.**
- `--unprivileged 1`: container root is mapped to an unprivileged host UID.
- `--features nesting=1`: needed for systemd in Debian 12 containers to start cleanly (its
  sandboxing features need it). It is not used to run Docker or other containers inside.

**4. IP forwarding.** `net.ipv4.ip_forward` is set per network namespace, so the container sets it
itself (`/etc/sysctl.d/99-vibeguard.conf`); the host's setting doesn't matter.

**5. Network.** Publish or forward each service's UDP `ListenPort` to the container's IP, and use the
container's bridge interface (e.g. `eth0`) for `VIBEGUARD_MDNS_UPSTREAM`.

**Verify** after `pct start <CTID>`:

    pct exec <CTID> -- ls -l /dev/net/tun                                        # crw-rw-rw- ... nobody nogroup 10, 200
    pct exec <CTID> -- sh -c 'ip link add wgtest type wireguard && ip link del wgtest && echo kernel wireguard OK'
    pct exec <CTID> -- cat /proc/sys/net/ipv4/ip_forward                         # 1
    pct exec <CTID> -- systemctl status vibeguard

## Test

    go test ./...
