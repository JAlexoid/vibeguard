package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type firewallFixture struct {
	Tables     map[string]map[string][][]string `json:"tables"`
	Calls      [][]string                       `json:"calls"`
	Forwarding bool                             `json:"forwarding"`
}

// Real helper processes run against this stateful iptables/ip/sysctl fixture.
// No firewall tools or privileges are required by the Go test suite.
func TestNetworkToolProcess(t *testing.T) {
	if os.Getenv("VG_NETWORK_TEST_TOOL") != "1" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	tool, args := filepath.Base(args[0]), args[1:]
	path := os.Getenv("VG_NETWORK_TEST_STATE")
	b, _ := os.ReadFile(path)
	var state firewallFixture
	if err := json.Unmarshal(b, &state); err != nil {
		panic(err)
	}
	state.Calls = append(state.Calls, append([]string{tool}, args...))
	status := 0
	switch tool {
	case "sysctl":
		if args[0] == "-n" {
			if state.Forwarding {
				fmt.Println("1")
			} else {
				fmt.Println("0")
			}
		} else {
			state.Forwarding = true
		}
	case "ip":
		switch {
		case args[0] == "-o":
			if args[1] == "-4" {
				fmt.Println("1: wg0 inet 10.8.0.1/24 scope global wg0")
			} else {
				fmt.Println("1: wg0 inet6 fd00:8::1/64 scope global")
			}
		case len(args) > 2 && args[1] == "route":
			if os.Getenv("VG_NETWORK_TEST_NO_UPLINK") == "" {
				fmt.Println("default via 192.0.2.1 dev eth0 proto dhcp")
			}
		case args[0] == "link": // egress existence probe
		default:
			panic(args)
		}
	case "iptables", "ip6tables":
		table, op, chain := args[2], args[3], args[4]
		if op == "-S" && os.Getenv("VG_NETWORK_TEST_DENY") == "1" {
			status = 4
			break
		}
		rules := state.Tables[tool+"/"+table]
		entry, exists := rules[chain]
		rest := args[5:]
		if os.Getenv("VG_NETWORK_TEST_FAIL_MDNS") == "1" && tool == "ip6tables" && op == "-A" && chain == "VGM_OUTPUT" {
			status = 4
			break
		}
		if os.Getenv("VG_NETWORK_TEST_FAIL_NAT") == "1" && op == "-A" && table == "nat" {
			status = 4
			break
		}
		switch op {
		case "-S":
			if !exists {
				status = 1
			}
		case "-N":
			if exists {
				status = 1
			} else {
				rules[chain] = [][]string{}
			}
		case "-F":
			if !exists {
				status = 1
			} else {
				rules[chain] = [][]string{}
			}
		case "-X":
			if !exists || len(entry) != 0 {
				status = 1
				break
			}
			for _, rs := range rules {
				for _, r := range rs {
					if slices.Contains(r, chain) {
						status = 1
					}
				}
			}
			if status == 0 {
				delete(rules, chain)
			}
		case "-A":
			rules[chain] = append(entry, rest)
		case "-I":
			rules[chain] = append([][]string{rest[1:]}, entry...)
		case "-C", "-D":
			found := -1
			for i, r := range entry {
				if reflect.DeepEqual(r, rest) {
					found = i
					break
				}
			}
			if found < 0 {
				status = 1
			} else if op == "-D" {
				rules[chain] = append(entry[:found], entry[found+1:]...)
			}
		default:
			panic(op)
		}
	default:
		panic(tool)
	}
	b, _ = json.Marshal(state)
	if err := os.WriteFile(path, b, 0600); err != nil {
		panic(err)
	}
	os.Exit(status)
}

func networkFixture(t *testing.T) (func(string, ...string) error, func() firewallFixture) {
	t.Helper()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	state := firewallFixture{Tables: map[string]map[string][][]string{}}
	for _, tool := range []string{"iptables", "ip6tables"} {
		state.Tables[tool+"/filter"] = map[string][][]string{"FORWARD": {{"-j", "DROP"}}, "INPUT": {{"-j", "DROP"}}, "OUTPUT": {{"-j", "DROP"}}}
		state.Tables[tool+"/nat"] = map[string][][]string{"POSTROUTING": {{"-s", "192.0.2.0/24", "-j", "MASQUERADE"}}}
	}
	b, _ := json.Marshal(state)
	if err := os.WriteFile(statePath, b, 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"ip", "iptables", "ip6tables", "sysctl"} {
		script := "#!/bin/sh\nexec \"$VG_NETWORK_TEST_BINARY\" -test.run=^TestNetworkToolProcess$ -- \"$0\" \"$@\"\n"
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	run := func(action string, extra ...string) error {
		args := []string{"systemd/vibeguard-network", action, "wg0"}
		if strings.HasPrefix(action, "mdns") {
			args = append([]string{"systemd/vibeguard-network"}, strings.Fields(action)...)
		}
		cmd := exec.Command("bash", args...)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "VG_NETWORK_TEST_TOOL=1", "VG_NETWORK_TEST_BINARY="+binary, "VG_NETWORK_TEST_STATE="+statePath)
		cmd.Env = append(cmd.Env, extra...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, out)
		}
		return nil
	}
	read := func() firewallFixture {
		b, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		var s firewallFixture
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	return run, read
}

func TestGatewayRulesLifecycle(t *testing.T) {
	run, read := networkFixture(t)
	baseline := read().Tables
	if err := run("up"); err != nil {
		t.Fatal(err)
	}
	first := read()
	if !first.Forwarding {
		t.Fatal("forwarding not enabled")
	}
	rules := fmt.Sprint(first.Tables["iptables/nat"]["VGN_wg0"])
	if !strings.Contains(rules, "-s 10.8.0.1/24 -o eth0 -j MASQUERADE") {
		t.Fatal("missing IPv4 masquerading", rules)
	}
	if _, ok := first.Tables["ip6tables/nat"]["VGN_wg0"]; ok {
		t.Fatal("NAT66 unexpectedly enabled")
	}
	for _, tool := range []string{"iptables", "ip6tables"} {
		rs := first.Tables[tool+"/filter"]["VGF_wg0"]
		if len(rs) != 2 || !strings.Contains(fmt.Sprint(rs), "RELATED,ESTABLISHED") {
			t.Fatal("missing bidirectional forwarding", rs)
		}
	}
	if err := run("up"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Tables, read().Tables) {
		t.Fatal("repeated up duplicated or changed rules")
	}
	if err := run("down"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(baseline, read().Tables) {
		t.Fatal("cleanup changed unrelated firewall rules", read().Tables)
	}
	if err := run("down"); err != nil {
		t.Fatal("repeated down:", err)
	}
}

func TestGatewayOptionsAndRollback(t *testing.T) {
	t.Run("nat66-and-uplink", func(t *testing.T) {
		run, read := networkFixture(t)
		if err := run("up", "VIBEGUARD_NAT66=1", "VIBEGUARD_EGRESS_INTERFACE=wan0"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(fmt.Sprint(read().Tables["ip6tables/nat"]["VGN_wg0"]), "-o wan0 -j MASQUERADE") {
			t.Fatal("NAT66 or uplink override ignored")
		}
	})
	t.Run("routed", func(t *testing.T) {
		run, read := networkFixture(t)
		if err := run("up", "VIBEGUARD_NAT=0"); err != nil {
			t.Fatal(err)
		}
		if _, ok := read().Tables["iptables/nat"]["VGN_wg0"]; ok {
			t.Fatal("IPv4 NAT opt-out ignored")
		}
	})
	for _, tc := range []struct {
		name string
		env  []string
		fail bool
	}{
		{"firewall-opt-out", []string{"VIBEGUARD_MANAGE_FIREWALL=0"}, false},
		{"partial-failure", []string{"VG_NETWORK_TEST_FAIL_NAT=1"}, true},
		{"no-uplink", []string{"VG_NETWORK_TEST_NO_UPLINK=1"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run, read := networkFixture(t)
			baseline := read().Tables
			err := run("up", tc.env...)
			if (err != nil) != tc.fail {
				t.Fatalf("unexpected result: %v", err)
			}
			if !reflect.DeepEqual(baseline, read().Tables) {
				t.Fatal("rules leaked after opt-out or failure", read().Tables)
			}
		})
	}
}

func TestGatewayCleanupReportsPermissionErrors(t *testing.T) {
	run, read := networkFixture(t)
	baseline := read().Tables
	if err := run("down", "VG_NETWORK_TEST_DENY=1"); err == nil {
		t.Fatal("cleanup hid firewall permission error")
	}
	if !reflect.DeepEqual(baseline, read().Tables) {
		t.Fatal("failed cleanup touched unrelated rules")
	}
}

func TestMDNSFirewallSelectionAndCleanup(t *testing.T) {
	run, read := networkFixture(t)
	baseline := read().Tables
	if err := run("mdns eth0 wg0"); err != nil {
		t.Fatal(err)
	}
	first := read().Tables
	for _, tool := range []string{"iptables", "ip6tables"} {
		filter := first[tool+"/filter"]
		wantIn := [][]string{{"-i", "eth0", "-p", "udp", "--dport", "5353", "-j", "ACCEPT"}, {"-i", "wg0", "-p", "udp", "--dport", "5353", "-j", "ACCEPT"}}
		wantOut := [][]string{{"-o", "eth0", "-p", "udp", "--sport", "5353", "--dport", "5353", "-j", "ACCEPT"}, {"-o", "wg0", "-p", "udp", "--sport", "5353", "--dport", "5353", "-j", "ACCEPT"}}
		if !reflect.DeepEqual(filter["VGM_INPUT"], wantIn) || !reflect.DeepEqual(filter["VGM_OUTPUT"], wantOut) {
			t.Fatalf("unexpected %s mDNS allowances: %v", tool, filter)
		}
	}
	if err := run("mdns eth0 wg0"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, read().Tables) {
		t.Fatal("repeat mDNS application duplicated rules")
	}
	if err := run("mdns eth1 wg1"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprint(read().Tables), "-i wg0") || strings.Contains(fmt.Sprint(read().Tables), "-o eth0") {
		t.Fatal("deselected interfaces still allowed")
	}
	if err := run("mdns"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(baseline, read().Tables) {
		t.Fatal("mDNS cleanup changed unrelated firewall rules")
	}
	if err := run("mdns eth0 wg0", "VIBEGUARD_MANAGE_FIREWALL=0"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(baseline, read().Tables) {
		t.Fatal("mDNS firewall opt-out ignored")
	}
}

func TestMDNSFirewallRollsBackBothFamilies(t *testing.T) {
	run, read := networkFixture(t)
	baseline := read().Tables
	if err := run("mdns eth0 wg0", "VG_NETWORK_TEST_FAIL_MDNS=1"); err == nil {
		t.Fatal("expected partial IPv6 update failure")
	}
	if !reflect.DeepEqual(baseline, read().Tables) {
		t.Fatal("mDNS failure left partial allowances or changed unrelated rules", read().Tables)
	}
}
