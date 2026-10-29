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

# mDNS is local traffic: prove UDP/5353 traverses INPUT and OUTPUT even with
# terminal DROP rules, and that deselecting lo closes that allowance again.
iptables -A INPUT -m comment --comment unrelated-input -j DROP
iptables -A OUTPUT -m comment --comment unrelated-output -j DROP
ip6tables -A INPUT -m comment --comment unrelated-input -j DROP
ip6tables -A OUTPUT -m comment --comment unrelated-output -j DROP
bash /usr/local/libexec/vibeguard-network mdns eth0 lo
for tool in iptables ip6tables; do
 "$tool" -C VGM_INPUT -i lo -p udp --dport 5353 -j ACCEPT
 "$tool" -C VGM_OUTPUT -o eth0 -p udp --sport 5353 --dport 5353 -j ACCEPT
done
python3 - <<'PYCODE'
import socket
for family,address in [(socket.AF_INET,'127.0.0.1'),(socket.AF_INET6,'::1')]:
 with socket.socket(family,socket.SOCK_DGRAM) as s:
  s.settimeout(2)
  s.bind((address,5353))
  s.sendto(b'mdns-firewall-probe',(address,5353))
  assert s.recvfrom(256)[0]==b'mdns-firewall-probe'
print('PASS: IPv4/IPv6 UDP 5353 INPUT/OUTPUT packets accepted ahead of DROP rules')
PYCODE
bash /usr/local/libexec/vibeguard-network mdns eth0
python3 - <<'PYCODE'
import socket
for family,address in [(socket.AF_INET,'127.0.0.1'),(socket.AF_INET6,'::1')]:
 with socket.socket(family,socket.SOCK_DGRAM) as s:
  s.settimeout(.2)
  s.bind((address,5353))
  try: s.sendto(b'mdns-firewall-probe',(address,5353))
  except PermissionError: continue  # OUTPUT DROP can reject sendto immediately.
  try: s.recvfrom(256)
  except socket.timeout: pass
  else: raise AssertionError('deselected interface still accepts mDNS')
print('PASS: deselected interface no longer accepts mDNS packets')
PYCODE
bash /usr/local/libexec/vibeguard-network mdns
for tool in iptables ip6tables; do
 "$tool" -C INPUT -m comment --comment unrelated-input -j DROP
 "$tool" -C OUTPUT -m comment --comment unrelated-output -j DROP
 if "$tool" -S VGM_INPUT 2>/dev/null; then exit 1; fi
 if "$tool" -S VGM_OUTPUT 2>/dev/null; then exit 1; fi
done
echo 'PASS: mDNS rule cleanup preserves unrelated INPUT/OUTPUT rules'
