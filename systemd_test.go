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
	script = strings.ReplaceAll(script, "/usr/local/libexec/vibeguard-network", filepath.Join(dir, "bin", "vibeguard-network"))
	script = strings.ReplaceAll(script, "/etc/wireguard", host)
	script = strings.ReplaceAll(script, "/run/vibeguard-wg.", filepath.Join(dir, "reload."))
	path := filepath.Join(dir, "helper")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "calls")
	for _, name := range []string{"wg-quick", "ip", "wg", "vibeguard-network"} {
		body := "#!/bin/sh\nprintf '%s %s\\n' '" + name + "' \"$*\" >> \"$TEST_CALLS\"\n"
		if name == "wg-quick" {
			body += "if [ \"$1\" = strip ]; then echo '[Interface]'; fi\n"
			body += "if [ \"$1\" = down ] && [ \"${VG_TEST_WG_DOWN_FAIL:-}\" = 1 ]; then exit 1; fi\n"
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
	for _, want := range []string{"wg-quick up " + managed + "/wg0.conf", "ip link set dev wg0 multicast on", "vibeguard-network up wg0", "vibeguard-network down wg0", "wg syncconf wg0", "wg-quick down " + managed + "/wg0.conf"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %q in %s", want, calls)
		}
	}
	t.Setenv("VG_TEST_WG_DOWN_FAIL", "1")
	if err := runHelper("down", "wg0"); err == nil {
		t.Fatal("expected wg-quick down failure")
	}
	failedDown, _ := os.ReadFile(log)
	if !strings.HasSuffix(string(failedDown), "vibeguard-network down wg0\n") {
		t.Fatal("wg-quick failure skipped network cleanup", string(failedDown))
	}
	t.Setenv("VG_TEST_WG_DOWN_FAIL", "")
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

func TestInstallerRejectsMissingPolkitBeforeWritingFiles(t *testing.T) {
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make unavailable")
	}
	dir := t.TempDir()
	// Leave only network tools on PATH: having WireGuard or policy files alone must
	// not pass the polkit prerequisite check and start modifying the host.
	for _, tool := range []string{"wg-quick", "iptables", "ip6tables", "sysctl"} {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(makePath, "--assume-old=/bin/true", "install", "BINARY=/bin/true")
	cmd.Env = append(os.Environ(), "PATH="+dir)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Missing polkitd") {
		t.Fatalf("installer did not diagnose missing polkitd: %v\n%s", err, output)
	}
}

func TestInstallerRequiresRunningPolkit(t *testing.T) {
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make unavailable")
	}
	for _, tc := range []struct{ fail, want string }{{"start", "Cannot start polkit.service"}, {"is-active", "polkit.service must be running"}} {
		t.Run(tc.fail, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"wg-quick", "pkaction", "pkcheck", "iptables", "ip6tables", "sysctl"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			fake := "#!/bin/sh\nif [ \"$1\" = " + tc.fail + " ]; then exit 1; fi\nexit 0\n"
			if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(makePath, "--assume-old=/bin/true", "install", "BINARY=/bin/true")
			cmd.Env = append(os.Environ(), "PATH="+dir)
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), tc.want) {
				t.Fatalf("installer ignored daemon failure: %v\n%s", err, output)
			}
		})
	}
}
