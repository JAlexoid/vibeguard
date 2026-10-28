package main

import (
	"errors"
	"net"
	"sort"
)

type MDNSSettings struct {
	Enabled   bool     `json:"enabled"`
	Upstreams []string `json:"upstreams"`
	Services  []string `json:"services"`
}

func cloneMDNS(v MDNSSettings) MDNSSettings {
	v.Upstreams = append([]string{}, v.Upstreams...)
	v.Services = append([]string{}, v.Services...)
	return v
}

func (s *Store) MDNS(defaults MDNSSettings) MDNSSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.MDNS == nil {
		if defaults.Enabled && defaults.Services == nil {
			for _, v := range s.d.Services {
				defaults.Services = append(defaults.Services, v.Name)
			}
		}
		return cloneMDNS(defaults)
	}
	return cloneMDNS(*s.d.MDNS)
}

func (s *Store) SetMDNS(v MDNSSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.Enabled && (len(v.Upstreams) == 0 || len(v.Services) == 0) {
		return errors.New("select at least one LAN interface and one WireGuard interface")
	}
	seen := map[string]bool{}
	for _, name := range v.Upstreams {
		if !nameRe.MatchString(name) || seen[name] {
			return errors.New("invalid or duplicate LAN interface")
		}
		if _, ok := s.service(name); ok {
			return errors.New("WireGuard interfaces must be selected under VPN interfaces")
		}
		ifc, err := net.InterfaceByName(name)
		if err != nil || ifc.Flags&net.FlagLoopback != 0 {
			return errors.New("LAN interface does not exist or is loopback")
		}
		seen[name] = true
	}
	for _, name := range v.Services {
		if seen[name] {
			return errors.New("duplicate interface selection")
		}
		if _, ok := s.service(name); !ok {
			return errors.New("selected WireGuard service does not exist")
		}
		seen[name] = true
	}
	old := s.d.MDNS
	copy := cloneMDNS(v)
	s.d.MDNS = &copy
	if err := s.save(); err != nil {
		s.d.MDNS = old
		return err
	}
	return nil
}

type InterfaceView struct {
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
	Up        bool     `json:"up"`
}

func localInterfaces() ([]InterfaceView, error) {
	ifcs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := []InterfaceView{}
	for _, ifc := range ifcs {
		if ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			return nil, err
		}
		v := InterfaceView{Name: ifc.Name, Up: ifc.Flags&net.FlagUp != 0, Addresses: []string{}}
		for _, a := range addrs {
			v.Addresses = append(v.Addresses, a.String())
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
