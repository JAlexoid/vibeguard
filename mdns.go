package main

import (
	"context"
	"log"
	"net"
	"net/netip"
	"syscall"
	"time"

	"golang.org/x/net/ipv4"
)

var mdnsGroup = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// Reflector forwards mDNS packets between the upstream (LAN) interface and the
// WireGuard service interfaces so VPN clients can resolve .local names.
//
//   - upstream -> WireGuard: sent as unicast (port 5353) to every user of each active service
//   - WireGuard -> upstream: re-sent to the mDNS multicast group on the upstream interface
//   - WireGuard -> other WireGuard users of the same service: unicast copies
type Reflector struct {
	upstream string
	store    *Store
	pc       *ipv4.PacketConn
	joined   map[int]bool // interface index -> joined
}

func NewReflector(upstream string, st *Store) *Reflector {
	return &Reflector{upstream: upstream, store: st, joined: map[int]bool{}}
}

func (m *Reflector) Run() error {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var err error
		c.Control(func(fd uintptr) {
			if err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err == nil {
				err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, 0xf, 1) // SO_REUSEPORT
			}
		})
		return err
	}}
	conn, err := lc.ListenPacket(context.Background(), "udp4", ":5353") //nolint
	if err != nil {
		return err
	}
	m.pc = ipv4.NewPacketConn(conn)
	if err := m.pc.SetControlMessage(ipv4.FlagInterface|ipv4.FlagSrc, true); err != nil {
		return err
	}
	m.pc.SetMulticastLoopback(false)
	m.pc.SetMulticastTTL(255)
	go func() {
		for {
			m.refresh()
			time.Sleep(5 * time.Second)
		}
	}()
	buf := make([]byte, 9000)
	for {
		n, cm, src, err := m.pc.ReadFrom(buf)
		if err != nil {
			return err
		}
		if cm == nil {
			continue
		}
		m.forward(buf[:n], cm.IfIndex, src.(*net.UDPAddr))
	}
}

// refresh joins the multicast group on the upstream and all running service interfaces.
func (m *Reflector) refresh() {
	names := []string{m.upstream}
	for _, s := range m.store.Services() {
		names = append(names, s.Name)
	}
	seen := map[int]bool{}
	for _, n := range names {
		ifc, err := net.InterfaceByName(n)
		if err != nil || ifc.Flags&net.FlagUp == 0 {
			continue
		}
		seen[ifc.Index] = true
		if !m.joined[ifc.Index] {
			if err := m.pc.JoinGroup(ifc, mdnsGroup); err != nil {
				log.Printf("mdns: join %s: %v", n, err)
				continue
			}
			m.joined[ifc.Index] = true
			log.Printf("mdns: listening on %s", n)
		}
	}
	for idx := range m.joined {
		if !seen[idx] {
			delete(m.joined, idx)
		}
	}
}

func isLocal(ip net.IP) bool {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

func (m *Reflector) forward(pkt []byte, idx int, src *net.UDPAddr) {
	in, err := net.InterfaceByIndex(idx)
	if err != nil || isLocal(src.IP) {
		return
	}
	up, err := net.InterfaceByName(m.upstream)
	if err != nil {
		return
	}
	if in.Index == up.Index {
		for _, s := range m.store.Services() {
			if ifc, err := net.InterfaceByName(s.Name); err == nil && ifc.Flags&net.FlagUp != 0 {
				m.unicastUsers(pkt, s.Name, netip.Addr{})
			}
		}
		return
	}
	if _, ok := m.store.Service(in.Name); !ok {
		return
	}
	cm := &ipv4.ControlMessage{IfIndex: up.Index}
	m.pc.WriteTo(pkt, cm, mdnsGroup)
	from, _ := netip.AddrFromSlice(src.IP.To4())
	m.unicastUsers(pkt, in.Name, from)
}

func (m *Reflector) unicastUsers(pkt []byte, service string, except netip.Addr) {
	for _, u := range m.store.Users(service) {
		p, err := netip.ParsePrefix(u.Address)
		if err != nil || p.Addr() == except {
			continue
		}
		m.pc.WriteTo(pkt, nil, &net.UDPAddr{IP: p.Addr().AsSlice(), Port: 5353})
	}
}
