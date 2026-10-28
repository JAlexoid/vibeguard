package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Service is a WireGuard interface (server side).
type Service struct {
	Name       string `json:"name"`
	Address    string `json:"address"` // CIDR of the server in the VPN, e.g. 10.8.0.1/24
	ListenPort int    `json:"listenPort"`
	Endpoint   string `json:"endpoint"` // public host:port handed to clients
	DNS        string `json:"dns"`
	PrivateKey string `json:"privateKey"`
	PublicKey  string `json:"publicKey"`
}

// User is a WireGuard peer (client side).
type User struct {
	ID           string `json:"id"`
	Service      string `json:"service"`
	Name         string `json:"name"`
	Address      string `json:"address"` // e.g. 10.8.0.2/32
	PrivateKey   string `json:"privateKey"`
	PublicKey    string `json:"publicKey"`
	PresharedKey string `json:"presharedKey"`
	AllowedIPs   string `json:"allowedIPs"` // what the client routes through the VPN
}

type data struct {
	Services []Service     `json:"services"`
	Users    []User        `json:"users"`
	MDNS     *MDNSSettings `json:"mdns,omitempty"`
}

// Store persists services and users in a JSON file.
type Store struct {
	mu   sync.Mutex
	path string
	d    data
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,14}$`)

func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, json.Unmarshal(b, &s.d)
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.path, b)
}

func genKeyPair() (priv, pub string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	e := base64.StdEncoding
	return e.EncodeToString(k.Bytes()), e.EncodeToString(k.PublicKey().Bytes()), nil
}

func genPSK() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func (s *Store) Services() []Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Service{}, s.d.Services...)
}

func (s *Store) Service(name string) (Service, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.service(name)
}

func (s *Store) service(name string) (Service, bool) {
	for _, v := range s.d.Services {
		if v.Name == name {
			return v, true
		}
	}
	return Service{}, false
}

func (s *Store) AddService(v Service) (Service, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !nameRe.MatchString(v.Name) {
		return v, errors.New("invalid name (1-15 letters, digits, underscores, dots or hyphens; start with a letter, digit or underscore)")
	}
	if _, ok := s.service(v.Name); ok {
		return v, errors.New("service already exists")
	}
	prefixes, err := parseAddresses(v.Address)
	if err != nil {
		return v, err
	}
	v.Address = joinPrefixes(prefixes)
	if v.Endpoint != "" {
		host, port, err := net.SplitHostPort(v.Endpoint)
		n, e := strconv.Atoi(port)
		if err != nil || e != nil || n < 1 || n > 65535 || host == "" || strings.ContainsAny(host, "\r\n\t #=;\\") {
			return v, errors.New("endpoint must be host:port or [IPv6]:port")
		}
	}
	if strings.ContainsAny(v.DNS, "\r\n") {
		return v, errors.New("DNS must be a single line")
	}

	if v.ListenPort < 1 || v.ListenPort > 65535 {
		return v, errors.New("invalid listen port")
	}
	for _, o := range s.d.Services {
		if o.ListenPort == v.ListenPort {
			return v, errors.New("listen port already used")
		}
	}
	if v.PrivateKey, v.PublicKey, err = genKeyPair(); err != nil {
		return v, err
	}
	s.d.Services = append(s.d.Services, v)
	if err := s.save(); err != nil {
		s.d.Services = s.d.Services[:len(s.d.Services)-1]
		return v, err
	}
	return v, nil
}

func (s *Store) DeleteService(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var svcs []Service
	for _, v := range s.d.Services {
		if v.Name != name {
			svcs = append(svcs, v)
		}
	}
	if len(svcs) == len(s.d.Services) {
		return errors.New("service not found")
	}
	var users []User
	for _, u := range s.d.Users {
		if u.Service != name {
			users = append(users, u)
		}
	}
	old := s.d
	s.d.Services, s.d.Users = svcs, users
	if s.d.MDNS != nil {
		cfg := cloneMDNS(*s.d.MDNS)
		cfg.Services = nil
		for _, n := range s.d.MDNS.Services {
			if n != name {
				cfg.Services = append(cfg.Services, n)
			}
		}
		if len(cfg.Services) == 0 {
			cfg.Enabled = false
		}
		s.d.MDNS = &cfg
	}
	if err := s.save(); err != nil {
		s.d = old
		return err
	}
	return nil
}

func (s *Store) Users(service string) []User {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []User{}
	for _, u := range s.d.Users {
		if service == "" || u.Service == service {
			out = append(out, u)
		}
	}
	return out
}

func (s *Store) User(id string) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.d.Users {
		if u.ID == id {
			return u, true
		}
	}
	return User{}, false
}

// AddUser creates a peer, allocating the next free address in the service subnet.
func (s *Store) AddUser(service, name, allowed string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := User{Service: service, Name: name, AllowedIPs: allowed}
	svc, ok := s.service(service)
	if !ok {
		return u, errors.New("service not found")
	}
	if name == "" || len(name) > 64 {
		return u, errors.New("invalid user name")
	}
	prefixes, err := parseAddresses(svc.Address)
	if err != nil {
		return u, err
	}
	if allowed == "" {
		var routes []string
		for _, p := range prefixes {
			if p.Addr().Is4() {
				routes = append(routes, "0.0.0.0/0")
			} else {
				routes = append(routes, "::/0")
			}
		}
		u.AllowedIPs = strings.Join(routes, ", ")
	} else {
		var routes []string
		for _, part := range strings.Split(allowed, ",") {
			p, err := netip.ParsePrefix(strings.TrimSpace(part))
			if err != nil || p.Addr().Is4In6() {
				return u, errors.New("allowed IPs must be comma-separated IPv4/IPv6 CIDRs")
			}
			routes = append(routes, p.Masked().String())
		}
		u.AllowedIPs = strings.Join(routes, ", ")
	}
	var allocated []netip.Prefix
	for _, prefix := range prefixes {
		used := map[netip.Addr]bool{prefix.Addr(): true}
		for _, o := range s.d.Users {
			if o.Service != service {
				continue
			}
			for _, part := range strings.Split(o.Address, ",") {
				if p, err := netip.ParsePrefix(strings.TrimSpace(part)); err == nil {
					used[p.Addr()] = true
				}
			}
		}
		ip, err := allocateAddress(prefix, used)
		if err != nil {
			return u, err
		}
		allocated = append(allocated, netip.PrefixFrom(ip, ip.BitLen()))
	}
	u.Address = joinPrefixes(allocated)
	if u.PrivateKey, u.PublicKey, err = genKeyPair(); err != nil {
		return u, err
	}
	if u.PresharedKey, err = genPSK(); err != nil {
		return u, err
	}
	idb := make([]byte, 6)
	rand.Read(idb)
	u.ID = fmt.Sprintf("%x", idb)
	s.d.Users = append(s.d.Users, u)
	if err := s.save(); err != nil {
		s.d.Users = s.d.Users[:len(s.d.Users)-1]
		return u, err
	}
	return u, nil
}

func (s *Store) DeleteUser(id string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, u := range s.d.Users {
		if u.ID == id {
			old := s.d.Users
			next := append([]User{}, old[:i]...)
			s.d.Users = append(next, old[i+1:]...)
			if err := s.save(); err != nil {
				s.d.Users = old
				return u, err
			}
			return u, nil
		}
	}
	return User{}, errors.New("user not found")
}
