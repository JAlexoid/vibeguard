package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"regexp"
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
	Services []Service `json:"services"`
	Users    []User    `json:"users"`
}

// Store persists services and users in a JSON file.
type Store struct {
	mu   sync.Mutex
	path string
	d    data
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9_=+.-]{1,15}$`)

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
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
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
		return v, errors.New("invalid name (1-15 chars: letters, digits, _=+.-)")
	}
	if _, ok := s.service(v.Name); ok {
		return v, errors.New("service already exists")
	}
	if p, err := netip.ParsePrefix(v.Address); err != nil || !p.Addr().Is4() {
		return v, errors.New("address must be an IPv4 CIDR such as 10.8.0.1/24")
	}
	if v.ListenPort < 1 || v.ListenPort > 65535 {
		return v, errors.New("invalid listen port")
	}
	for _, o := range s.d.Services {
		if o.ListenPort == v.ListenPort {
			return v, errors.New("listen port already used")
		}
	}
	var err error
	if v.PrivateKey, v.PublicKey, err = genKeyPair(); err != nil {
		return v, err
	}
	s.d.Services = append(s.d.Services, v)
	return v, s.save()
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
	s.d.Services, s.d.Users = svcs, users
	return s.save()
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
	if u.AllowedIPs == "" {
		u.AllowedIPs = "0.0.0.0/0, ::/0"
	}
	prefix, _ := netip.ParsePrefix(svc.Address)
	used := map[netip.Addr]bool{prefix.Addr(): true}
	for _, o := range s.d.Users {
		if o.Service == service {
			if p, err := netip.ParsePrefix(o.Address); err == nil {
				used[p.Addr()] = true
			}
		}
	}
	var ip netip.Addr
	for a := prefix.Masked().Addr().Next(); prefix.Contains(a); a = a.Next() {
		// skip broadcast address
		if n := a.Next(); !prefix.Contains(n) {
			break
		}
		if !used[a] {
			ip = a
			break
		}
	}
	if !ip.IsValid() {
		return u, errors.New("no free addresses in service subnet")
	}
	u.Address = netip.PrefixFrom(ip, 32).String()
	var err error
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
	return u, s.save()
}

func (s *Store) DeleteUser(id string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, u := range s.d.Users {
		if u.ID == id {
			s.d.Users = append(s.d.Users[:i], s.d.Users[i+1:]...)
			return u, s.save()
		}
	}
	return User{}, errors.New("user not found")
}
