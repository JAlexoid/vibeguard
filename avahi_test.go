package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAvahiConfiguration(t *testing.T) {
	cfg := MDNSSettings{Enabled: true, Upstreams: []string{"eth0", "eth0"}, Services: []string{"wg0"}}
	data, err := avahiConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"allow-interfaces=eth0,wg0\n", "enable-reflector=yes", "allow-point-to-point=yes", "use-ipv6=yes", "enable-dbus=no"} {
		if !strings.Contains(string(data), want) {
			t.Fatal(string(data))
		}
	}
	cfg.Upstreams = []string{"eth0\n[server]"}
	if _, err := avahiConfig(cfg); err == nil {
		t.Fatal("accepted config injection")
	}
}

func TestAvahiLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "avahi.conf")
	t.Setenv("VIBEGUARD_AVAHI_CONFIG", path)
	var calls [][]string
	command := func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	cfg := MDNSSettings{Enabled: true, Upstreams: []string{"eth0"}, Services: []string{"wg0"}}
	r := Runner{Systemd: true, command: command}
	if err := r.MDNSReflector(cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, [][]string{{"systemctl", "--no-ask-password", "restart", "vibeguard-avahi.service"}}) {
		t.Fatal(calls)
	}
	calls = nil
	r.Systemd = false
	if err := r.MDNSReflector(cfg); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || calls[1][1] != "--kill" || calls[2][1] != "--daemonize" {
		t.Fatal(calls)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "allow-interfaces=eth0,wg0") {
		t.Fatal(string(data), err)
	}
	calls = nil
	if err := r.MDNSReflector(MDNSSettings{}); err != nil || len(calls) != 2 {
		t.Fatal(calls, err)
	}
	r.command = func(_ string, args ...string) error {
		if args[0] == "--kill" {
			return errors.New("stop failed")
		}
		return nil
	}
	if err := r.MDNSReflector(cfg); err == nil {
		t.Fatal("stop failure ignored")
	}
}

func TestMDNSSettingsSurfacesAvahiFailure(t *testing.T) {
	s := featureServer(t)
	if _, err := s.store.AddService(Service{Name: "wg0", Address: "10.8.0.1/24", ListenPort: 51820}); err != nil {
		t.Fatal(err)
	}
	ifcs, _ := net.Interfaces()
	lan := ""
	for _, i := range ifcs {
		if i.Flags&net.FlagLoopback == 0 {
			lan = i.Name
			break
		}
	}
	if lan == "" {
		t.Skip("no LAN interface")
	}
	s.runner.Systemd = true
	s.runner.command = func(string, ...string) error { return errors.New("authorization denied") }
	w := request(t, s, "PUT", "/api/settings/mdns", `{"enabled":true,"upstreams":["`+lan+`"],"services":["wg0"]}`, s.auth.Token())
	if w.Code != 500 || !strings.Contains(w.Body.String(), "Avahi update failed") {
		t.Fatal(w.Code, w.Body)
	}
	if !s.store.MDNS(MDNSSettings{}).Enabled {
		t.Fatal("desired settings lost")
	}
}
