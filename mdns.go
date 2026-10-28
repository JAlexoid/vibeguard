package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// Reflector reads settings for each packet, so saved selections take effect
// without restarting VibeGuard. IPv4 and IPv6 sockets have independent memberships.
type Reflector struct {
	store    *Store
	defaults MDNSSettings
}

func NewReflector(upstream string, st *Store) *Reflector {
	defaults := MDNSSettings{Enabled: upstream != ""}
	for _, name := range strings.Split(upstream, ",") {
		if name = strings.TrimSpace(name); name != "" {
			defaults.Upstreams = append(defaults.Upstreams, name)
		}
	}
	return &Reflector{store: st, defaults: defaults}
}

func (m *Reflector) Run() error { return m.RunContext(context.Background()) }

func (m *Reflector) RunContext(ctx context.Context) error {
	// Do not bind multicast sockets until the repeater is enabled.
	for !m.store.MDNS(m.defaults).Enabled {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
	results := make(chan error, 2)
	for _, v6 := range []bool{false, true} {
		go func(v6 bool) { results <- m.runSocket(ctx, v6) }(v6)
	}
	var failures []error
	for i := 0; i < 2; i++ {
		err := <-results
		if err != nil {
			log.Printf("mdns: %v", err)
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

type mdnsSocket struct {
	read  func([]byte) (int, int, *net.UDPAddr, error)
	write func([]byte, int, *net.UDPAddr) error
	join  func(*net.Interface, net.Addr) error
	leave func(*net.Interface, net.Addr) error
}

func (m *Reflector) runSocket(ctx context.Context, v6 bool) error {
	network, addr := "udp4", "0.0.0.0:5353"
	group := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
	if v6 {
		network, addr = "udp6", "[::]:5353"
		group = &net.UDPAddr{IP: net.ParseIP("ff02::fb"), Port: 5353}
	}
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var sockErr error
		if err := c.Control(func(fd uintptr) {
			sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			if sockErr == nil {
				sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, 0xf, 1)
			}
		}); err != nil {
			return err
		}
		return sockErr
	}}
	conn, err := lc.ListenPacket(ctx, network, addr)
	if err != nil {
		return fmt.Errorf("%s listen: %w", network, err)
	}
	defer conn.Close()
	var socket mdnsSocket
	if v6 {
		pc := ipv6.NewPacketConn(conn)
		if err = pc.SetControlMessage(ipv6.FlagInterface|ipv6.FlagSrc, true); err != nil {
			return err
		}
		if err = pc.SetMulticastLoopback(false); err != nil {
			return err
		}
		if err = pc.SetMulticastHopLimit(255); err != nil {
			return err
		}
		if err = pc.SetHopLimit(255); err != nil {
			return err
		}
		socket = mdnsSocket{
			join: pc.JoinGroup, leave: pc.LeaveGroup,
			read: func(b []byte) (int, int, *net.UDPAddr, error) {
				n, cm, src, e := pc.ReadFrom(b)
				idx := 0
				if cm != nil {
					idx = cm.IfIndex
				}
				from, _ := src.(*net.UDPAddr)
				return n, idx, from, e
			},
			write: func(b []byte, idx int, dst *net.UDPAddr) error {
				_, e := pc.WriteTo(b, &ipv6.ControlMessage{IfIndex: idx, HopLimit: 255}, dst)
				return e
			},
		}
	} else {
		pc := ipv4.NewPacketConn(conn)
		if err = pc.SetControlMessage(ipv4.FlagInterface|ipv4.FlagSrc, true); err != nil {
			return err
		}
		if err = pc.SetMulticastLoopback(false); err != nil {
			return err
		}
		if err = pc.SetMulticastTTL(255); err != nil {
			return err
		}
		if err = pc.SetTTL(255); err != nil {
			return err
		}
		socket = mdnsSocket{
			join: pc.JoinGroup, leave: pc.LeaveGroup,
			read: func(b []byte) (int, int, *net.UDPAddr, error) {
				n, cm, src, e := pc.ReadFrom(b)
				idx := 0
				if cm != nil {
					idx = cm.IfIndex
				}
				from, _ := src.(*net.UDPAddr)
				return n, idx, from, e
			},
			write: func(b []byte, idx int, dst *net.UDPAddr) error {
				_, e := pc.WriteTo(b, &ipv4.ControlMessage{IfIndex: idx, TTL: 255}, dst)
				return e
			},
		}
	}
	joined := map[int]net.Interface{}
	buf := make([]byte, 9000)
	var refreshAt time.Time
	for ctx.Err() == nil {
		cfg := m.store.MDNS(m.defaults)
		if time.Now().After(refreshAt) {
			m.refresh(socket, group, joined, cfg)
			refreshAt = time.Now().Add(time.Second)
		}
		if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			return err
		}
		n, idx, src, err := socket.read(buf)
		if err != nil {
			if e, ok := err.(net.Error); ok && e.Timeout() {
				continue
			}
			return err
		}
		if src == nil || isLocal(src.IP) {
			continue
		}
		in, err := net.InterfaceByIndex(idx)
		if err != nil {
			continue
		}
		source, ok := netip.AddrFromSlice(src.IP)
		if !ok {
			continue
		}
		source = source.Unmap()
		// A settings update can arrive while ReadFrom is blocked.
		cfg = m.store.MDNS(m.defaults)
		for _, target := range reflectTargets(cfg, m.store.Users(""), in.Name, source, v6) {
			ifc, err := net.InterfaceByName(target.Interface)
			if err != nil || ifc.Flags&net.FlagUp == 0 {
				continue
			}
			dst := &net.UDPAddr{IP: target.IP.AsSlice(), Port: 5353}
			if v6 && target.IP.IsLinkLocalMulticast() {
				dst.Zone = ifc.Name
			}
			if err := socket.write(buf[:n], ifc.Index, dst); err != nil {
				log.Printf("mdns: send on %s: %v", ifc.Name, err)
			}
		}
	}
	return nil
}

func (m *Reflector) refresh(socket mdnsSocket, group *net.UDPAddr, joined map[int]net.Interface, cfg MDNSSettings) {
	seen := map[int]bool{}
	if cfg.Enabled {
		for _, name := range append(append([]string{}, cfg.Upstreams...), cfg.Services...) {
			ifc, err := net.InterfaceByName(name)
			if err != nil || ifc.Flags&net.FlagUp == 0 {
				continue
			}
			seen[ifc.Index] = true
			if _, ok := joined[ifc.Index]; !ok {
				if err := socket.join(ifc, group); err != nil {
					log.Printf("mdns: join %s: %v", name, err)
					continue
				}
				joined[ifc.Index] = *ifc
			}
		}
	}
	for idx, ifc := range joined {
		if !seen[idx] {
			socket.leave(&ifc, group)
			delete(joined, idx)
		}
	}
}

type mdnsTarget struct {
	Interface string
	IP        netip.Addr
}

// reflectTargets is independent of sockets, allowing both address families and
// selection/exclusion rules to be tested without modifying host networking.
func reflectTargets(cfg MDNSSettings, users []User, in string, source netip.Addr, v6 bool) []mdnsTarget {
	if !cfg.Enabled {
		return nil
	}
	selected := false
	for _, name := range append(append([]string{}, cfg.Upstreams...), cfg.Services...) {
		if name == in {
			selected = true
		}
	}
	if !selected {
		return nil
	}
	out := []mdnsTarget{}
	group := netip.MustParseAddr("224.0.0.251")
	if v6 {
		group = netip.MustParseAddr("ff02::fb")
	}
	for _, name := range cfg.Upstreams {
		if name != in {
			out = append(out, mdnsTarget{name, group})
		}
	}
	for _, name := range cfg.Services {
		for _, u := range users {
			if u.Service != name {
				continue
			}
			for _, part := range strings.Split(u.Address, ",") {
				p, err := netip.ParsePrefix(strings.TrimSpace(part))
				if err != nil || p.Addr().Is6() != v6 || p.Addr() == source {
					continue
				}
				out = append(out, mdnsTarget{name, p.Addr()})
			}
		}
	}
	return out
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
