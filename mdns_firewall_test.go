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

func TestMDNSFirewallRunner(t *testing.T) {
	var got []string
	command := func(name string, args ...string) error {
		got = append([]string{filepath.Base(name)}, args...)
		return nil
	}
	cfg := MDNSSettings{Enabled: true, Upstreams: []string{"eth0"}, Services: []string{"wg0"}}
	r := Runner{command: command}
	if err := r.MDNSFirewall(cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"vibeguard-network", "mdns", "eth0", "wg0"}) {
		t.Fatal(got)
	}
	r.Systemd = true
	if err := r.MDNSFirewall(cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"systemctl", "--no-ask-password", "restart", "vibeguard-mdns-firewall.service"}) {
		t.Fatal(got)
	}
	cfg.Upstreams = []string{"eth0;id"}
	if err := applyMDNSFirewall(cfg, command); err == nil {
		t.Fatal("accepted unsafe interface name")
	}
}

func TestMDNSFirewallCommandReadsSavedState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	calls := filepath.Join(dir, "calls")
	helper := filepath.Join(dir, "network")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$VG_TEST_MDNS_CALLS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIBEGUARD_DATA", path)
	t.Setenv("VIBEGUARD_NETWORK_HELPER", helper)
	t.Setenv("VG_TEST_MDNS_CALLS", calls)
	t.Setenv("VIBEGUARD_MDNS_UPSTREAM", "")
	data := `{"services":[{"name":"wg0"}],"mdns":{"enabled":true,"upstreams":["eth0"],"services":["wg0"]}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runMDNSFirewallCommand(nil); err != nil {
		t.Fatal(err)
	}
	if err := runMDNSFirewallCommand([]string{"--clear"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(calls)
	if string(b) != "mdns eth0 wg0\nmdns\n" {
		t.Fatal(string(b))
	}
}

func TestMDNSSettingsSurfacesFirewallFailure(t *testing.T) {
	s := featureServer(t)
	_, err := s.store.AddService(Service{Name: "wg0", Address: "10.8.0.1/24", ListenPort: 51820})
	if err != nil {
		t.Fatal(err)
	}
	ifcs, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
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
	s.runner.command = func(string, ...string) error { return errors.New("firewall denied") }
	body := `{"enabled":true,"upstreams":["` + lan + `"],"services":["wg0"]}`
	w := request(t, s, "PUT", "/api/settings/mdns", body, s.auth.Token())
	if w.Code != 500 || !strings.Contains(w.Body.String(), "firewall update failed") {
		t.Fatal(w.Code, w.Body)
	}
	if !s.store.MDNS(MDNSSettings{}).Enabled {
		t.Fatal("desired settings lost on firewall failure")
	}
}
