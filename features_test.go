package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func request(t *testing.T, s *Server, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func featureServer(t *testing.T) *Server {
	t.Helper()
	st, err := NewStore(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewPersistentAuth("initial password", filepath.Join(t.TempDir(), "password.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &Server{store: st, auth: a, runner: Runner{Dir: t.TempDir(), command: func(name string, args ...string) error {
		if filepath.Base(name) == "vibeguard-network" {
			return nil
		}
		return errors.New("unexpected command: " + name)
	}}}
}
func TestPasswordChangePersistenceAndSessions(t *testing.T) {
	s := featureServer(t)
	token := s.auth.Token()
	for _, path := range []string{"/api/password", "/api/settings/mdns", "/api/interfaces", "/api/settings"} {
		if w := request(t, s, "POST", path, `{}`, ""); w.Code != 401 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	for _, test := range []struct {
		body string
		code int
	}{
		{`{"currentPassword":"wrong","newPassword":"replacement password"}`, 403},
		{`{"currentPassword":"initial password","newPassword":"short"}`, 400},
		{`{"currentPassword":"initial password","newPassword":"replacement password"}`, 200},
	} {
		if w := request(t, s, "POST", "/api/password", test.body, token); w.Code != test.code {
			t.Fatalf("change password: %d %s", w.Code, w.Body)
		}
	}
	if w := request(t, s, "GET", "/api/services", "", token); w.Code != 401 {
		t.Fatal("old session remained valid")
	}
	b, err := os.ReadFile(s.auth.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("replacement password")) || bytes.Contains(b, []byte("initial password")) {
		t.Fatal("saved plaintext password")
	}
	info, _ := os.Stat(s.auth.path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("password mode: %v", info.Mode())
	}
	for _, bootstrap := range []string{"initial password", ""} {
		restarted, err := NewPersistentAuth(bootstrap, s.auth.path)
		if err != nil {
			t.Fatal(err)
		}
		if restarted.CheckPassword("initial password") || !restarted.CheckPassword("replacement password") {
			t.Fatal("saved password did not override bootstrap")
		}
	}
	if w := request(t, s, "POST", "/api/login", `{"password":"replacement password"}`, ""); w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal("cannot log in with new password")
	}
}
func TestPasswordSaveFailurePreservesPasswordAndSessions(t *testing.T) {
	a := NewAuth("initial password")
	a.path = t.TempDir()
	token := a.Token()
	if err := a.ChangePassword("initial password", "replacement password"); err == nil {
		t.Fatal("expected save error")
	}
	if !a.CheckPassword("initial password") {
		t.Fatal("password changed despite save failure")
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	if !a.Valid(r) {
		t.Fatal("session invalidated despite save failure")
	}
}
func TestIPv6AndDualStackAllocation(t *testing.T) {
	for _, test := range []struct {
		address string
		want    []string
	}{
		{"fd00:8::1/126", []string{"fd00:8::2/128", "fd00:8::3/128"}},
		{"10.8.0.1/29, fd00:8::1/126", []string{"10.8.0.2/32, fd00:8::2/128", "10.8.0.3/32, fd00:8::3/128"}},
		{"10.8.0.0/31", []string{"10.8.0.1/32"}},
	} {
		t.Run(test.address, func(t *testing.T) {
			st, _ := NewStore(filepath.Join(t.TempDir(), "d.json"))
			svc, err := st.AddService(Service{Name: "wg0", Address: test.address, ListenPort: 51820, Endpoint: "[2001:db8::1]:51820"})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range test.want {
				u, err := st.AddUser("wg0", "peer", "")
				if err != nil || u.Address != want {
					t.Fatalf("%q: %v %v", want, u, err)
				}
				conf := ClientConfig(svc, u)
				if !strings.Contains(conf, "Address = "+want) || !strings.Contains(conf, "Endpoint = [2001:db8::1]:51820") {
					t.Fatal("bad client config")
				}
				if !strings.Contains(ServerConfig(svc, []User{u}), "AllowedIPs = "+want) {
					t.Fatal("missing server routes")
				}
			}
			if _, err := st.AddUser("wg0", "exhausted", ""); err == nil {
				t.Fatal("expected subnet exhaustion")
			}
			reloaded, err := NewStore(st.path)
			if err != nil || len(reloaded.Users("wg0")) != len(test.want) {
				t.Fatal("allocation did not persist cleanly")
			}
		})
	}
}
func TestInvalidNetworkInputs(t *testing.T) {
	for _, address := range []string{"invalid", "10.8.0.1/24, 10.9.0.1/24", "fd00::1/64, fd01::1/64", "::ffff:192.0.2.1/120", "ff02::1/64", "0.0.0.0/24"} {
		st, _ := NewStore(filepath.Join(t.TempDir(), "d.json"))
		if _, err := st.AddService(Service{Name: "wg0", Address: address, ListenPort: 1}); err == nil {
			t.Fatalf("accepted address %q", address)
		}
	}
	for _, name := range []string{"../wg0", "-wg0", "wg0;reboot", "wg0\nPostUp=bad", "."} {
		st, _ := NewStore(filepath.Join(t.TempDir(), "d.json"))
		if _, err := st.AddService(Service{Name: name, Address: "fd00::1/64", ListenPort: 1}); err == nil {
			t.Fatalf("accepted name %q", name)
		}
	}
	s := featureServer(t)
	if _, err := s.store.AddService(Service{Name: "wg0", Address: "fd00::1/64", ListenPort: 1, Endpoint: "2001:db8::1:51820"}); err == nil {
		t.Fatal("accepted unbracketed IPv6 endpoint")
	}
	_, err := s.store.AddService(Service{Name: "wg0", Address: "fd00::1/64", ListenPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.AddUser("wg0", "peer", "::/0\nPostUp=bad"); err == nil {
		t.Fatal("accepted configuration injection")
	}
}

type fakeSystemd struct {
	active, enabled bool
	fail            string
	commands        []string
}

func (f *fakeSystemd) command(name string, args ...string) error {
	if name != "systemctl" {
		return errors.New("unexpected native command " + name)
	}
	f.commands = append(f.commands, strings.Join(args, " "))
	if len(args) < 3 || args[0] != "--no-ask-password" || args[len(args)-1] != "wg-quick@wg0.service" {
		return errors.New("invalid systemctl invocation")
	}
	if args[1] == f.fail {
		return errors.New("systemd authorization denied")
	}
	switch args[1] {
	case "is-active":
		if !f.active {
			return errors.New("inactive")
		}
	case "is-enabled":
		if !f.enabled {
			return errors.New("disabled")
		}
	case "enable":
		if args[2] != "--no-reload" {
			return errors.New("enable must not request daemon-reload")
		}
		f.enabled = true
		for _, arg := range args {
			if arg == "--now" {
				f.active = true
			}
		}
	case "disable":
		if args[2] != "--no-reload" {
			return errors.New("disable must not request daemon-reload")
		}
		f.enabled = false
		f.active = false
	case "restart":
		f.active = true
	case "reload":
		if !f.active {
			return errors.New("inactive")
		}
	default:
		return errors.New("unexpected systemctl action")
	}
	return nil
}
func TestSystemdLifecycleAPI(t *testing.T) {
	s := featureServer(t)
	f := &fakeSystemd{}
	s.runner.Systemd = true
	s.runner.command = f.command
	token := s.auth.Token()
	w := request(t, s, "POST", "/api/services", `{"name":"wg0","address":"10.8.0.1/24, fd00::1/64","listenPort":51820,"endpoint":"[2001:db8::1]:51820"}`, token)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	for _, action := range []string{"start", "start", "restart"} {
		w = request(t, s, "POST", "/api/services/wg0/"+action, "", token)
		if w.Code != 200 || !f.active || !f.enabled {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body)
		}
	}
	w = request(t, s, "POST", "/api/users", `{"service":"wg0","name":"peer"}`, token)
	if w.Code != 201 || !strings.Contains(strings.Join(f.commands, "\n"), "reload wg-quick@wg0.service") {
		t.Fatal("peer change did not reload active unit", w.Code, w.Body)
	}
	w = request(t, s, "GET", "/api/services", "", token)
	if !strings.Contains(w.Body.String(), `"enabled":true`) || !strings.Contains(w.Body.String(), `"active":true`) {
		t.Fatal("incorrect status", w.Body)
	}
	f.fail = "disable"
	w = request(t, s, "DELETE", "/api/services/wg0", "", token)
	if w.Code != 500 || !strings.Contains(w.Body.String(), "authorization denied") {
		t.Fatal("systemd error swallowed", w.Code, w.Body)
	}
	if _, ok := s.store.Service("wg0"); !ok {
		t.Fatal("failed delete removed store entry")
	}
	if _, err := os.Stat(s.runner.confPath("wg0")); err != nil {
		t.Fatal("failed delete removed config")
	}
	f.fail = ""
	w = request(t, s, "POST", "/api/services/wg0/stop", "", token)
	if w.Code != 200 || f.active || f.enabled {
		t.Fatal("stop did not disable boot", w.Code, w.Body)
	}
	f.enabled = true
	w = request(t, s, "DELETE", "/api/services/wg0", "", token)
	if w.Code != 200 || f.enabled {
		t.Fatal("delete left unit enabled", w.Code, w.Body)
	}
	if _, err := os.Stat(s.runner.confPath("wg0")); !os.IsNotExist(err) {
		t.Fatal("config remained after delete")
	}
}
func TestSystemdEnableAndReloadFailures(t *testing.T) {
	f := &fakeSystemd{fail: "enable"}
	r := Runner{Systemd: true, command: f.command}
	if err := r.Start("wg0"); err == nil {
		t.Fatal("enable error ignored")
	}
	if err := r.Restart("wg0"); err == nil || f.active {
		t.Fatal("restarted after failed enable")
	}
	f.fail = "reload"
	f.active = true
	if err := r.Sync("wg0"); err == nil {
		t.Fatal("reload error ignored")
	}
	f.active = false
	f.commands = nil
	if err := r.Sync("wg0"); err != nil || len(f.commands) != 1 {
		t.Fatal("inactive unit was reloaded", f.commands)
	}
}
func TestMDNSRoutingFamiliesAndSelections(t *testing.T) {
	cfg := MDNSSettings{Enabled: true, Upstreams: []string{"lan0", "lan1"}, Services: []string{"wg0"}}
	users := []User{{Service: "wg0", Address: "10.8.0.2/32, fd00::2/128"}, {Service: "wg0", Address: "fd00::3/128"}, {Service: "wg1", Address: "10.9.0.2/32, fd01::2/128"}}
	for _, test := range []struct {
		in, source string
		v6         bool
		want       []mdnsTarget
	}{
		{"lan0", "192.0.2.5", false, []mdnsTarget{{"lan1", netip.MustParseAddr("224.0.0.251")}, {"wg0", netip.MustParseAddr("10.8.0.2")}}},
		{"lan0", "fe80::5", true, []mdnsTarget{{"lan1", netip.MustParseAddr("ff02::fb")}, {"wg0", netip.MustParseAddr("fd00::2")}, {"wg0", netip.MustParseAddr("fd00::3")}}},
		{"wg0", "fd00::2", true, []mdnsTarget{{"lan0", netip.MustParseAddr("ff02::fb")}, {"lan1", netip.MustParseAddr("ff02::fb")}, {"wg0", netip.MustParseAddr("fd00::3")}}},
		{"wg1", "fd01::2", true, nil},
	} {
		got := reflectTargets(cfg, users, test.in, netip.MustParseAddr(test.source), test.v6)
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("%s IPv6=%v: got %v want %v", test.in, test.v6, got, test.want)
		}
	}
	cfg.Enabled = false
	if got := reflectTargets(cfg, users, "lan0", netip.MustParseAddr("192.0.2.5"), false); len(got) != 0 {
		t.Fatal("disabled repeater forwards traffic")
	}
}
func TestMDNSMembershipLeavesDeselectedInterfaces(t *testing.T) {
	ifcs, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var chosen net.Interface
	for _, ifc := range ifcs {
		if ifc.Flags&net.FlagUp != 0 {
			chosen = ifc
			break
		}
	}
	if chosen.Name == "" {
		t.Skip("no up interface")
	}
	joins, leaves := 0, 0
	sock := mdnsSocket{join: func(*net.Interface, net.Addr) error { joins++; return nil }, leave: func(*net.Interface, net.Addr) error { leaves++; return nil }}
	m := &Reflector{}
	joined := map[int]net.Interface{}
	cfg := MDNSSettings{Enabled: true, Upstreams: []string{chosen.Name}}
	m.refresh(sock, &net.UDPAddr{}, joined, cfg)
	m.refresh(sock, &net.UDPAddr{}, joined, cfg)
	m.refresh(sock, &net.UDPAddr{}, joined, MDNSSettings{})
	if joins != 1 || leaves != 1 || len(joined) != 0 {
		t.Fatalf("memberships: joins %d leaves %d", joins, leaves)
	}
}
func TestMDNSSettingsPersistence(t *testing.T) {
	s := featureServer(t)
	token := s.auth.Token()
	_, err := s.store.AddService(Service{Name: "wg0", Address: "fd00::1/64", ListenPort: 51820})
	if err != nil {
		t.Fatal(err)
	}
	ifcs, err := localInterfaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(ifcs) == 0 {
		t.Skip("no LAN interface")
	}
	cfg := MDNSSettings{Enabled: true, Upstreams: []string{ifcs[0].Name}, Services: []string{"wg0"}}
	body, _ := json.Marshal(cfg)
	w := request(t, s, "PUT", "/api/settings/mdns", string(body), token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	st, err := NewStore(s.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.MDNS(MDNSSettings{}), cfg) {
		t.Fatal("settings did not persist")
	}
	cfg.Services = []string{"missing"}
	if err := st.SetMDNS(cfg); err == nil {
		t.Fatal("accepted unknown service")
	}
	if err := st.DeleteService("wg0"); err != nil {
		t.Fatal(err)
	}
	if st.MDNS(MDNSSettings{}).Enabled {
		t.Fatal("repeater still enabled without selected VPN service")
	}
}
func TestMDNSDisabledContextCancellation(t *testing.T) {
	st, _ := NewStore(filepath.Join(t.TempDir(), "d.json"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- NewReflector("", st).RunContext(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("disabled repeater did not stop")
	}
}

func TestDefaultRoutesMatchAddressFamilies(t *testing.T) {
	for _, tc := range []struct{ addr, want string }{{"10.8.0.1/24", "0.0.0.0/0"}, {"fd00::1/64", "::/0"}, {"10.8.0.1/24, fd00::1/64", "0.0.0.0/0, ::/0"}} {
		st, _ := NewStore(filepath.Join(t.TempDir(), "data.json"))
		if _, err := st.AddService(Service{Name: "wg0", Address: tc.addr, ListenPort: 51820}); err != nil {
			t.Fatal(err)
		}
		u, err := st.AddUser("wg0", "peer", "")
		if err != nil || u.AllowedIPs != tc.want {
			t.Fatalf("%s: got %q %v", tc.addr, u.AllowedIPs, err)
		}
		u, err = st.AddUser("wg0", "custom", " 10.0.0.2/8,\n fd00::1/64 ")
		if err != nil || u.AllowedIPs != "10.0.0.0/8, fd00::/64" {
			t.Fatalf("routes were not normalized: %q %v", u.AllowedIPs, err)
		}
	}
}

func TestLegacyIPv4Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	legacy := `{"services":[{"name":"wg0","address":"10.8.0.1/24","listenPort":51820}],"users":[{"id":"old","service":"wg0","name":"old","address":"10.8.0.2/32","allowedIPs":"0.0.0.0/0, ::/0"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.AddUser("wg0", "new", "")
	if err != nil || u.Address != "10.8.0.3/32" {
		t.Fatal("legacy allocation incompatible", u, err)
	}
	old, _ := st.User("old")
	if old.AllowedIPs != "0.0.0.0/0, ::/0" {
		t.Fatal("changed existing client routes")
	}
}
