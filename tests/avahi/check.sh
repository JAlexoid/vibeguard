set -eu
mkdir -p /var/lib/vibeguard
printf '%s\n' '{"services":[{"name":"wg0"}],"mdns":{"enabled":true,"upstreams":["eth0"],"services":["wg0"]}}' > /var/lib/vibeguard/data.json
trap 'vibeguard avahi --stop' EXIT
vibeguard avahi
avahi-daemon --check
grep -q '^allow-interfaces=eth0,wg0$' /run/vibeguard-avahi/avahi-daemon.conf
vibeguard avahi
avahi-daemon --check
vibeguard avahi --stop
if avahi-daemon --check; then echo 'Avahi still running after shutdown' >&2; exit 1; fi
printf '%s\n' '{"mdns":{"enabled":false}}' > /var/lib/vibeguard/data.json
vibeguard avahi
if avahi-daemon --check; then echo 'Disabled Avahi started' >&2; exit 1; fi
echo 'PASS: real Avahi starts, restarts and stops with generated IPv4/IPv6 reflector configuration'
