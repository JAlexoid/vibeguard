package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivilegedHelper(t *testing.T) {
	dir := t.TempDir()
	managed := filepath.Join(dir, "managed")
	host := filepath.Join(dir, "host")
	for _, p := range []string{managed, host, filepath.Join(dir, "bin")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	source, err := os.ReadFile("systemd/vibeguard-wg-quick")
	if err != nil {
		t.Fatal(err)
	}
	// Substitute only filesystem locations in the fixture. The command/action
	// implementation is the installed script, running with fake wg/ip executables.
	script := strings.ReplaceAll(string(source), "/var/lib/vibeguard/wireguard", managed)
	script = strings.ReplaceAll(script, "/etc/wireguard", host)
	script = strings.ReplaceAll(script, "/run/vibeguard-wg.", filepath.Join(dir, "reload."))
	path := filepath.Join(dir, "helper")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "calls")
	for _, name := range []string{"wg-quick", "ip", "wg"} {
		body := "#!/bin/sh\nprintf '%s %s\\n' '" + name + "' \"$*\" >> \"$TEST_CALLS\"\n"
		if name == "wg-quick" {
			body += "if [ \"$1\" = strip ]; then echo '[Interface]'; fi\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "bin", name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	runHelper := func(action, name string) error {
		cmd := exec.Command("sh", path, action, name)
		cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "bin")+":"+os.Getenv("PATH"), "TEST_CALLS="+log)
		return cmd.Run()
	}
	if err := os.WriteFile(filepath.Join(managed, "wg0.conf"), []byte("[Interface]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"up", "reload", "down"} {
		if err := runHelper(action, "wg0"); err != nil {
			t.Fatal(action, err)
		}
	}
	b, _ := os.ReadFile(log)
	calls := string(b)
	for _, want := range []string{"wg-quick up " + managed + "/wg0.conf", "ip link set dev wg0 multicast on", "wg syncconf wg0", "wg-quick down " + managed + "/wg0.conf"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %q in %s", want, calls)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "reload.*"))
	if len(files) != 0 {
		t.Fatal("reload left private config behind")
	}
	// Existing unmanaged wg-quick units keep using their stock config location.
	if err := runHelper("up", "wg1"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(log)
	if !strings.Contains(string(b), "wg-quick up "+host+"/wg1.conf") {
		t.Fatal("broke unmanaged wg-quick configuration")
	}
	before, _ := os.ReadFile(log)
	if err := os.WriteFile(filepath.Join(host, "wg0.conf"), []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runHelper("up", "wg0"); err == nil {
		t.Fatal("overrode a conflicting host config")
	}
	for _, name := range []string{"../wg0", "-wg0", "wg0;id", "wg0\ncmd", "."} {
		if err := runHelper("up", name); err == nil {
			t.Fatalf("accepted unsafe instance %q", name)
		}
	}
	if err := runHelper("enable", "wg1"); err == nil {
		t.Fatal("accepted unknown helper action")
	}
	after, _ := os.ReadFile(log)
	if string(before) != string(after) {
		t.Fatal("invalid helper request executed a privileged command")
	}
}
