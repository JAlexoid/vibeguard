package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// ServerConfig renders the wg-quick config of a service including its users.
func ServerConfig(s Service, users []User) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nAddress = %s\nListenPort = %d\nPrivateKey = %s\n", s.Address, s.ListenPort, s.PrivateKey)
	for _, u := range users {
		fmt.Fprintf(&b, "\n# %s\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = %s\n",
			strings.NewReplacer("\n", " ", "\r", " ").Replace(u.Name), u.PublicKey, u.PresharedKey, u.Address)
	}
	return b.String()
}

// ClientConfig renders the config a user imports into a WireGuard client.
func ClientConfig(s Service, u User) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nAddress = %s\n", u.PrivateKey, u.Address)
	if s.DNS != "" {
		fmt.Fprintf(&b, "DNS = %s\n", s.DNS)
	}
	fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = %s\nEndpoint = %s\nPersistentKeepalive = 25\n",
		s.PublicKey, u.PresharedKey, u.AllowedIPs, s.Endpoint)
	return b.String()
}

// ClientConfigFilename identifies the VPN interface and public endpoint host.
// Keep names portable across desktop platforms, including IPv6 endpoints.
func ClientConfigFilename(s Service) string {
	host, _, err := net.SplitHostPort(s.Endpoint)
	if err != nil {
		host = "server"
	}
	host = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, host)
	host = strings.Trim(host, ".-")
	if host == "" {
		host = "server"
	}
	return s.Name + "@" + host + ".conf"
}

func QRPNG(content string) ([]byte, error) {
	return qrcode.Encode(content, qrcode.Medium, 512)
}

// Runner uses systemd on native installations and wg/ip in containers.
type Runner struct {
	Dir     string
	Systemd bool
	// command is overridden by tests; production executes native tools directly.
	command func(string, ...string) error
}

func (r Runner) run(name string, args ...string) error {
	if r.command != nil {
		return r.command(name, args...)
	}
	return run(name, args...)
}

func (r Runner) confPath(name string) string { return filepath.Join(r.Dir, name+".conf") }

func (r Runner) Write(s Service, users []User) error {
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return err
	}
	if !nameRe.MatchString(s.Name) {
		return fmt.Errorf("invalid interface name")
	}
	return atomicWrite(r.confPath(s.Name), []byte(ServerConfig(s, users)))
}

func run(name string, args ...string) error { return runIn("", name, args...) }

// runIn runs a command with stdin as its standard input.
func runIn(stdin, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// wgQuickOnly are wg-quick keys that `wg setconf` does not understand.
var wgQuickOnly = map[string]bool{"address": true, "dns": true, "mtu": true, "table": true,
	"preup": true, "postup": true, "predown": true, "postdown": true, "saveconfig": true}

// stripConf splits a wg-quick config into the part `wg setconf` accepts (like
// `wg-quick strip`) and the interface addresses.
func stripConf(conf string) (wg string, addrs []string) {
	var b strings.Builder
	for _, line := range strings.Split(conf, "\n") {
		k, v, ok := strings.Cut(strings.SplitN(line, "#", 2)[0], "=")
		k = strings.ToLower(strings.TrimSpace(k))
		if ok && k == "address" {
			for _, a := range strings.Split(v, ",") {
				if a = strings.TrimSpace(a); a != "" {
					addrs = append(addrs, a)
				}
			}
		}
		if ok && wgQuickOnly[k] {
			continue
		}
		b.WriteString(line + "\n")
	}
	return b.String(), addrs
}

func (r Runner) readConf(name string) (string, []string, error) {
	data, err := os.ReadFile(r.confPath(name))
	if err != nil {
		return "", nil, err
	}
	wg, addrs := stripConf(string(data))
	return wg, addrs, nil
}

func (r Runner) Active(name string) bool {
	if r.Systemd {
		return r.systemctl("is-active", "--quiet", unitName(name)) == nil
	}
	return exec.Command("wg", "show", name).Run() == nil
}

func (r Runner) Start(name string) error {
	if r.Systemd {
		return r.systemctl("enable", "--no-reload", "--now", unitName(name))
	}
	if r.Active(name) {
		return nil
	}
	wg, addrs, err := r.readConf(name)
	if err != nil {
		return err
	}
	if err := run("ip", "link", "add", "dev", name, "type", "wireguard"); err != nil {
		// No kernel module: fall back to the userspace implementation (needs /dev/net/tun), as wg-quick does.
		if _, lerr := exec.LookPath("wireguard-go"); lerr != nil {
			return err
		}
		if err := run("wireguard-go", name); err != nil {
			return err
		}
	}
	if err := r.configure(name, wg, addrs); err != nil {
		run("ip", "link", "del", "dev", name)
		return err
	}
	if err := r.network("up", name); err != nil {
		cleanupErr := r.network("down", name)
		linkErr := r.run("ip", "link", "del", "dev", name)
		return errors.Join(err, cleanupErr, linkErr)
	}
	return nil
}

func (r Runner) network(action, name string) error {
	return r.run(env("VIBEGUARD_NETWORK_HELPER", "/usr/local/libexec/vibeguard-network"), action, name)
}

func (r Runner) configure(name, wg string, addrs []string) error {
	if err := runIn(wg, "wg", "setconf", name, "/dev/stdin"); err != nil {
		return err
	}
	for _, a := range addrs {
		if err := run("ip", "address", "add", a, "dev", name); err != nil {
			return err
		}
	}
	// WireGuard interfaces lack the MULTICAST flag by default; needed for the mDNS reflector.
	return run("ip", "link", "set", "dev", name, "multicast", "on", "up")
}

func (r Runner) Stop(name string) error {
	if r.Systemd {
		return r.systemctl("disable", "--no-reload", "--now", unitName(name))
	}
	if r.Active(name) {
		if err := r.run("ip", "link", "del", "dev", name); err != nil {
			return err
		}
	}
	return r.network("down", name)
}

func (r Runner) Restart(name string) error {
	if r.Systemd {
		if err := r.systemctl("enable", "--no-reload", unitName(name)); err != nil {
			return err
		}
		return r.systemctl("restart", unitName(name))
	}
	if err := r.Stop(name); err != nil {
		return err
	}
	return r.Start(name)
}

// Sync pushes peer changes into a running interface without dropping it.
func (r Runner) Sync(name string) error {
	if r.Systemd {
		if !r.Active(name) {
			return nil
		}
		return r.systemctl("reload", unitName(name))
	}
	if !r.Active(name) {
		return nil
	}
	wg, _, err := r.readConf(name)
	if err != nil {
		return err
	}
	return runIn(wg, "wg", "syncconf", name, "/dev/stdin")
}
