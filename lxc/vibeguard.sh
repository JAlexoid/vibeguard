#!/usr/bin/env bash
# Create a Proxmox/LXC container running VibeGuard.
# Usage (on a Proxmox host): ./vibeguard.sh <CTID> <admin-password> [storage]
# The container needs /dev/net/tun and nesting; this script configures both.
set -euo pipefail

CTID=${1:?container id}
PASSWORD=${2:?admin password}
STORAGE=${3:-local-lvm}
TEMPLATE=${TEMPLATE:-debian-12-standard_12.7-1_amd64.tar.zst}
REPO=${REPO:-https://github.com/JAlexoid/vibeguard}

pveam download local "$TEMPLATE" || true
pct create "$CTID" "local:vztmpl/$TEMPLATE" \
  --hostname vibeguard --cores 1 --memory 256 --rootfs "$STORAGE:2" \
  --net0 name=eth0,bridge=vmbr0,ip=dhcp --unprivileged 1 --features nesting=1 --onboot 1
cat >> "/etc/pve/lxc/$CTID.conf" <<CONF
lxc.cgroup2.devices.allow: c 10:200 rwm
lxc.mount.entry: /dev/net/tun dev/net/tun none bind,create=file
CONF
pct start "$CTID"
sleep 5
printf 'VIBEGUARD_ADMIN_PASSWORD=%s\n' "$PASSWORD" | pct exec "$CTID" -- bash -c 'umask 077; cat > /etc/vibeguard.env'
pct exec "$CTID" -- bash -c "
set -e
apt-get update && apt-get install -y wireguard-tools iptables golang-go git ca-certificates openresolv iproute2 polkitd
git clone $REPO /opt/vibeguard-src
cd /opt/vibeguard-src && go build -o /usr/local/bin/vibeguard .
id -u vibeguard >/dev/null 2>&1 || useradd --system --user-group --home-dir /var/lib/vibeguard --no-create-home --shell /usr/sbin/nologin vibeguard
printf 'net.ipv4.ip_forward=1\nnet.ipv6.conf.all.forwarding=1\n' > /etc/sysctl.d/99-vibeguard.conf
cp /opt/vibeguard-src/lxc/vibeguard.service /etc/systemd/system/
install -Dm755 systemd/vibeguard-wg-quick /usr/local/libexec/vibeguard-wg-quick
install -Dm644 systemd/wg-quick-vibeguard.conf /etc/systemd/system/wg-quick@.service.d/vibeguard.conf
install -Dm644 systemd/50-vibeguard.rules /etc/polkit-1/rules.d/50-vibeguard.rules
sysctl -p /etc/sysctl.d/99-vibeguard.conf
systemctl daemon-reload
systemctl enable --now vibeguard
"
echo "VibeGuard is available on port 8080 of container $CTID"
