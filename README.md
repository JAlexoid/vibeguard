# VibeGuard

Simple WireGuard management UI: manage WireGuard services (interfaces), users (peers) and
generate QR codes for easy client setup. Single admin password, no other accounts.
Client configuration downloads use `<interface>@<endpoint-host>.conf`, for example
`wg0@vpn.example.com.conf` (the endpoint port is omitted).

Stack: one Go binary (standard library + `go-qrcode`) serving a REST API and a small
dependency-free HTML/JS frontend (embedded). Native installs manage `wg-quick@<interface>.service` through systemd and polkit. The UI runs
as the unprivileged `vibeguard` user. Containers without systemd use `wg` and `ip` directly
with `CAP_NET_ADMIN`. IPv4-only, IPv6-only, and dual-stack interfaces are supported.

## Dependencies

- Go 1.24 or newer to build the binary.
- Native Linux deployment: systemd, **polkitd (installed and running)**, wireguard-tools, iproute2, iptables (including ip6tables), and procps (sysctl).
  On Debian 12/13 or Ubuntu with modern polkit:

  ```sh
  sudo apt-get update
  sudo apt-get install polkitd wireguard-tools iproute2 iptables procps
  sudo systemctl start polkit.service
  sudo systemctl is-active polkit.service
  ```

`make install` checks for the actual polkit tools, starts `polkit.service`, verifies it is active,
and checks the installed rule authorizes the service account. It fails with an actionable error
if a prerequisite is missing; the presence of `/usr/share/polkit-1` alone is insufficient.
Both LXC installation paths install `polkitd`; the systemd unit starts it before VibeGuard.
Docker deployments without systemd use direct interface management and do not need polkit.

## Configuration (environment variables)

| Variable | Default | Description |
|---|---|---|
| `VIBEGUARD_ADMIN_PASSWORD` | required on first start | Bootstrap admin password; a password changed in the UI takes precedence |
| `VIBEGUARD_PASSWORD_FILE` | `admin-password.json` beside the data file | Persisted salted PBKDF2-SHA256 password hash (private, mode 0600) |
| `VIBEGUARD_SYSTEMD` | `auto` | `auto` detects systemd; `1` requires it, `0` uses direct interface management |
| `VIBEGUARD_LISTEN` | `:8080` | Listen address |
| `VIBEGUARD_DATA` | `/var/lib/vibeguard/data.json` | State file (contains keys, keep private) |
| `VIBEGUARD_WG_DIR` | `/var/lib/vibeguard/wireguard` with systemd; `/etc/wireguard` otherwise | Where `<name>.conf` files are written. Systemd mode requires the managed path expected by the installed helper |
| `VIBEGUARD_MDNS_UPSTREAM` | unset (disabled) | Bootstrap comma-separated LAN interfaces, e.g. `eth0,eth1`. Saved UI settings override this value |

Put the UI behind HTTPS (e.g. a reverse proxy) when exposing it beyond a trusted network.


## mDNS repeater

Use **Settings → mDNS repeater** to enable discovery and choose one or more LAN interfaces and
WireGuard interfaces. Selections are saved in the state file and apply without restarting.
Only selected, running interfaces forward traffic. Removing the last selected VPN interface
disables the repeater. `VIBEGUARD_MDNS_UPSTREAM` bootstraps the old behavior (all VPN interfaces)
until settings are first saved.

The repeater supports IPv4 (`224.0.0.251:5353`) and IPv6 (`[ff02::fb]:5353`): LAN multicast is
forwarded as unicast to VPN peers, peer traffic is reflected onto selected LANs, and discovery
is shared between selected VPN peers. Deselected interfaces leave the multicast groups.
Clients need routes for `224.0.0.251/32` and `ff02::fb/128`; full-tunnel routes for each family include
them. Docker needs host networking so LAN interfaces are visible. A disabled/unavailable address
family is logged; the other family continues independently.

With managed firewall rules enabled, saving discovery settings also updates **IPv4 and IPv6
INPUT/OUTPUT rules** for the selected interfaces. INPUT allows UDP destination port 5353;
OUTPUT allows UDP source/destination port 5353, covering both multicast queries and unicast
relays. No other UDP ports or unselected interfaces are opened. Deselection or disabling the
repeater removes its old allowances. These rules are separate from VPN FORWARD/NAT rules.

Native installs use the root-owned `vibeguard-mdns-firewall.service` oneshot, authorized through
polkit; the UI keeps running as `vibeguard` without NET_ADMIN. The oneshot reads the saved settings,
restores rules before the UI starts, and clears them when the UI service stops. Docker/direct
mode uses its existing NET_ADMIN capability and cleans up on graceful shutdown. Install the
updated unit and polkit rule once with `make install`; subsequent UI changes need no restart.
A failed firewall update is reported by the settings API instead of silently claiming success.
`VIBEGUARD_MANAGE_FIREWALL=0` opts out of both gateway and mDNS rule management. After changing
that flag in the root-owned environment file, save discovery settings again to apply it to mDNS.

Check the mDNS rules on the host/container:

```sh
sudo iptables -S VGM_INPUT
sudo iptables -S VGM_OUTPUT
sudo ip6tables -S VGM_INPUT
sudo ip6tables -S VGM_OUTPUT
```

## Password changes

Use **Settings → Change password**, enter the current password and a new password of at least
12 characters, and sign in again. The saved salted hash overrides the bootstrap environment password
on future starts. Every existing session is invalidated. Keep the password file in backups along
with `data.json`; it is never returned by the API. To reset a forgotten password, stop VibeGuard,
remove only the saved password file, set a new bootstrap password in `/etc/vibeguard.env`, and
restart. Do not remove the WireGuard state file.

## IPv6 and dual stack

Enter one CIDR per family, for example `10.8.0.1/24, fd00:8::1/64`, or just `fd00:8::1/64` for IPv6-only.
Peers receive a unique `/32` and/or `/128` from the configured subnets. IPv6 has no broadcast address.
Public IPv6 endpoints must include brackets: `[2001:db8::1]:51820`. Client DNS and allowed routes can
also contain IPv6 addresses/CIDRs. Default allowed routes cover only the service's configured families.
Existing IPv4 state files remain compatible.

## Forwarding and NAT

The native installer and both LXC paths persist IPv4/IPv6 forwarding in
`/etc/sysctl.d/99-vibeguard.conf`. Docker Compose sets the equivalent container sysctls.
Starting a managed VPN interface also checks forwarding and installs gateway rules:

- IPv4: masquerade the interface's VPN subnet on the uplink, allowing peers to reach the
  Internet/LAN without a return route on the upstream router.
- IPv6: allow forwarding with established/related return traffic. By default, route the VPN's
  IPv6 prefix on the upstream router; NAT66 is available when a routed prefix is unavailable.
- Firewall rules accept traffic from the VPN subnet to the selected uplink and established/related
  replies back to that subnet. Other firewall rules and default policies are preserved.

Rules live in private `VGF_<interface>`/`VGN_<interface>` filter/NAT chains and are removed on Stop/Delete.
They are restored by enabled `wg-quick` units on boot and rebuilt without duplicate rules on restart.
Failures roll back the new rules and fail interface startup. The native UI remains `User=vibeguard`;
its root-owned systemd helpers perform network administration. Existing unmanaged wg-quick instances
keep their previous behavior.

Native/LXC configuration is in the root-owned `/etc/vibeguard-network.env` (preserved by `make install`):

| Setting | Default | Behavior |
|---|---|---|
| `VIBEGUARD_EGRESS_INTERFACE` | `auto` | Use the default-route uplink for each family, falling back to the other family's uplink. Set e.g. `eth0` explicitly for multi-uplink hosts. |
| `VIBEGUARD_MANAGE_FIREWALL` | `1` | Set `0` to manage gateway and mDNS firewall rules yourself. IP forwarding remains enabled. |
| `VIBEGUARD_NAT` | `1` | IPv4 masquerading; set `0` when the upstream router routes the VPN subnet. |
| `VIBEGUARD_NAT66` | `0` | Set `1` for IPv6 masquerading. |

For example, set `VIBEGUARD_EGRESS_INTERFACE=eth0` in that file, then **restart the VPN interface**
in the UI to apply changes. Also restart existing VPN interfaces once after upgrading to this
version; restarting the UI alone does not reconfigure already-running tunnels. The same settings
are process environment variables in Docker/direct mode. Docker images include the helper at
`/usr/local/libexec/vibeguard-network`; local direct-mode development can select it using
`VIBEGUARD_NETWORK_HELPER=./systemd/vibeguard-network` (make the file executable first).
Direct mode requires `CAP_NET_ADMIN` and the firewall tools.

Verify a running `wg0` on its host/container:

```sh
sysctl net.ipv4.ip_forward net.ipv6.conf.all.forwarding  # both 1
sudo iptables -S VGF_wg0
sudo iptables -t nat -S VGN_wg0                       # IPv4 MASQUERADE
sudo ip6tables -S VGF_wg0                            # when IPv6 is configured
```

IPv6 forwarding can disable router-advertisement learning on an uplink. If that interface receives
its IPv6 default route through RAs, persist `net.ipv6.conf.<uplink>.accept_ra=2` on that host.
VibeGuard does not provision an IPv6 prefix or change uplink RA settings. Publish/forward each
WireGuard UDP port through any external firewall/router, and keep the management UI on a trusted
network or behind HTTPS. Hosts with UFW/firewalld or another firewall manager should either integrate
these narrowly scoped chains with that manager or set `VIBEGUARD_MANAGE_FIREWALL=0` and provide
matching rules there; a firewall reload may remove dynamically installed rules.


## Run

    VIBEGUARD_ADMIN_PASSWORD='<bootstrap-password>' go run .

Native systemd hosts need the installation below before interface control works. For direct-mode
development, set `VIBEGUARD_SYSTEMD=0`; starting interfaces requires `CAP_NET_ADMIN` and wireguard-tools.

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
file doesn't exist yet, enables IPv4/IPv6 forwarding, and enables and restarts the VibeGuard service.
Install `wireguard-tools`, `iproute2`, and `polkitd` on the host first (Debian/Ubuntu: `sudo apt-get install wireguard-tools iproute2 iptables procps polkitd`). If `DESTDIR` is set, only the files are staged.

**Service user.** `make install` asks which user to run as (default `vibeguard`) and creates it as a
system user if it doesn't exist. To skip the prompt, e.g. in scripts, pass `VIBEGUARD_USER=<name>`;
without a terminal it uses the default. The service stays unprivileged (`User=vibeguard`, `NoNewPrivileges=yes`), with only
`CAP_NET_BIND_SERVICE` for optional low HTTP ports. systemd creates `/var/lib/vibeguard` owned by
that user (`StateDirectory=`); generated configs are private files under
`/var/lib/vibeguard/wireguard`. `/etc/vibeguard.env` remains root-owned.

`make install` also installs:

- `/etc/polkit-1/rules.d/50-vibeguard.rules`: authorizes the chosen service account to manage
  `wg-quick@*.service` units and enable/disable unit files. systemd does not supply per-unit details
  for the unit-file action, so that action intentionally permits managing other unit files too.
  It does not grant general command execution or daemon-reload. No sudo password or root UI is needed.
- `/usr/local/libexec/vibeguard-wg-quick` and a `wg-quick@.service` drop-in: root-owned integration
  that reads the managed configs, enables multicast on managed VPN interfaces, and reloads peers
  using `wg syncconf` without dropping connections. Unmanaged units continue using `/etc/wireguard`.
  Conflicting configs in both locations cause an explicit error rather than replacing host files.

Starting an interface runs `systemctl --no-ask-password enable --no-reload --now wg-quick@<name>.service`.
Stopping or deleting it runs `disable --no-reload --now`; restart also enables it at boot.
The UI shows running and boot status independently, and reports systemd errors instead of hiding them.
`--no-reload` avoids requiring daemon-reload authorization; installation performs the one-time reload
as root. Enabled interfaces start independently of the UI on subsequent boots.

If interface startup reports `Failed to enable unit: Access denied`, check the installed
service account and polkit rule, rather than changing VibeGuard to root:

```sh
systemctl show vibeguard -p User -p NoNewPrivileges
sudo stat -c '%U:%G %a %n' /etc/polkit-1/rules.d/50-vibeguard.rules
pkaction --version
sudo make check-polkit                 # VIBEGUARD_USER=... for a custom account
sudo journalctl -u polkit -b --no-pager -n 40
```

The rule must name the account in the installed unit and be root-owned **0644**, not 0600:
polkit reads JavaScript rules as its own unprivileged user. Do not change the permissions or
ownership of the `rules.d` directory; distributions can deliberately keep it private to polkitd.
To reinstall the default rule and reload it without changing the UI account:

```sh
sudo install -o root -g root -m 0644 systemd/50-vibeguard.rules /etc/polkit-1/rules.d/50-vibeguard.rules
sudo systemctl restart polkit.service
sudo make check-polkit
```

Use the rule rendered by `make install VIBEGUARD_USER=...` when using a custom account.
The installer now verifies that the actual polkit tools/authority are present and that the chosen
account is authorized before starting VibeGuard. Some legacy polkit 0.105 backends use `.pkla`
instead of JavaScript `.rules`; use a supported modern polkit backend for this integration.

A real-daemon regression test can run in a disposable Docker container (no privileged mode):

```sh
docker build -f tests/polkit/Dockerfile -t vibeguard-polkit-test .
docker run --rm vibeguard-polkit-test
```

When upgrading from direct interface management (or PR #3), stop the old VPN interfaces before
installing to avoid an already-existing interface conflict. Preserve `/var/lib/vibeguard` and
`/etc/vibeguard.env`, run `make install`, and start desired interfaces in the UI once to enable
boot startup. If upgrading PR #3's `/etc/wireguard` configuration layout, move each managed config
into `/var/lib/vibeguard/wireguard` (owned by the service user, directory 0700/files 0600), or remove
only redundant generated copies after backing them up. Do not overwrite unrelated host configs.
`make uninstall` keeps the helper and wg-quick drop-in so enabled tunnels continue working; disable
those units before manually removing their integration files.

    sudo make install                          # prompts, default: vibeguard
    sudo make install VIBEGUARD_USER=wgadmin   # no prompt

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

**6. Start it, check polkit, and get the admin password:**

    pct start 120
    pct exec 120 -- dpkg-query -W -f='${Status}\n' polkitd  # install ok installed
    pct exec 120 -- systemctl is-active polkit.service        # active
    pct exec 120 -- runuser -u vibeguard -- sh -c 'exec pkcheck --action-id org.freedesktop.systemd1.manage-unit-files --process "$$"'  # exit 0
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

Creates a **Debian 13** container with TUN access, builds VibeGuard and installs the
`lxc/vibeguard.service` systemd unit. UI is at `http://<container-ip>:8080`.
The script refreshes the Proxmox template catalog and selects the latest available
`debian-13-standard_*_amd64.tar.zst` image. To choose a specific image, list the available
Debian 13 templates with `pveam available --section system`, then pass its exact name:

    TEMPLATE='<debian-13-template-name>' ./lxc/vibeguard.sh <CTID> <admin-password> [storage]

This manual creation path installs `polkitd`, starts `polkit.service`, checks it is active,
and verifies the `vibeguard` account is authorized before starting the UI. The Docker-built
preinstalled template described above remains based on Debian 12.

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
- `--features nesting=1`: needed for systemd in Debian 12/13 containers to start cleanly (its
  sandboxing features need it). It is not used to run Docker or other containers inside.

**4. Forwarding and NAT.** IPv4 and IPv6 forwarding are set in the container's network namespace
via `/etc/sysctl.d/99-vibeguard.conf`. VPN startup installs scoped forwarding and IPv4 masquerade
rules on its uplink; see [Forwarding and NAT](#forwarding-and-nat) for NAT66 and routed configurations.
The Proxmox host and upstream router must also permit the container's outbound traffic.

**5. Network.** Publish or forward each service's UDP `ListenPort` to the container's IP, and use the
container's bridge interface (e.g. `eth0`) for `VIBEGUARD_MDNS_UPSTREAM`.

**Verify** after `pct start <CTID>`:

    pct exec <CTID> -- ls -l /dev/net/tun                                        # crw-rw-rw- ... nobody nogroup 10, 200
    pct exec <CTID> -- sh -c 'ip link add wgtest type wireguard && ip link del wgtest && echo kernel wireguard OK'
    pct exec <CTID> -- cat /proc/sys/net/ipv4/ip_forward                         # 1
    pct exec <CTID> -- dpkg-query -W -f='${Status}\n' polkitd                # install ok installed
    pct exec <CTID> -- systemctl is-active polkit.service                     # active
    pct exec <CTID> -- runuser -u vibeguard -- sh -c 'exec pkcheck --action-id org.freedesktop.systemd1.manage-unit-files --process "$$"'  # exit 0
    pct exec <CTID> -- systemctl status vibeguard

## Test

    go test ./...
    go test -race ./...
    go vet ./...

Tests cover password persistence/session invalidation, systemd lifecycle and failure responses
(with a fake systemctl), IPv6/dual-stack allocation, mDNS family/selection routing, multicast
membership changes, and the privileged helper using isolated paths and fake wg/ip executables.
On a native systemd test host, additionally verify that Start marks `wg-quick@wg0` active and enabled,
Stop makes it inactive and disabled, peer edits reload without losing the interface, and an enabled
interface returns after reboot with the UI still running as `vibeguard`. Actual tunnel traffic and
LAN discovery require a WireGuard-capable host and clients.

The networking helper also has an isolated Debian 13 integration check using real firewall tools:

```sh
docker build -f tests/network/Dockerfile -t vibeguard-network-test .
docker run --rm --cap-add NET_ADMIN \
  --sysctl net.ipv4.ip_forward=1 --sysctl net.ipv6.conf.all.forwarding=1 \
  vibeguard-network-test
```

The test adds addresses and firewall rules only inside the disposable container's network namespace.
It checks forwarding rules, IPv4 NAT, optional NAT66, idempotency, and removal while preserving
unrelated rules. It also sends real IPv4/IPv6 UDP 5353 packets through INPUT/OUTPUT with terminal
DROP rules and verifies deselection blocks those packets again. It does not test a complete
WireGuard client tunnel.
