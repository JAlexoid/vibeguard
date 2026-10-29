set -euo pipefail
ip address add 10.81.0.1/24 dev lo
ip -6 address add fd81::1/64 dev lo
iptables -A FORWARD -m comment --comment unrelated-rule -j DROP
bash /usr/local/libexec/vibeguard-network up lo
iptables -C FORWARD -i lo -j VGF_lo
iptables -t nat -C VGN_lo -s 10.81.0.0/24 -o eth0 -j MASQUERADE
ip6tables -C VGF_lo -i eth0 -o lo -d fd81::/64 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
bash /usr/local/libexec/vibeguard-network up lo
[ "$(iptables -S FORWARD | grep -c -- '-i lo -j VGF_lo')" -eq 1 ]
VIBEGUARD_NAT66=1 bash /usr/local/libexec/vibeguard-network up lo
ip6tables -t nat -C VGN_lo -s fd81::/64 -o eth0 -j MASQUERADE
bash /usr/local/libexec/vibeguard-network down lo
bash /usr/local/libexec/vibeguard-network down lo
iptables -C FORWARD -m comment --comment unrelated-rule -j DROP
if iptables -t nat -S VGN_lo 2>/dev/null; then exit 1; fi
if ip6tables -S VGF_lo 2>/dev/null; then exit 1; fi
echo 'PASS: real IPv4/IPv6 firewall setup, IPv4 NAT, optional NAT66, repeat starts, cleanup, and preservation of unrelated rules'
