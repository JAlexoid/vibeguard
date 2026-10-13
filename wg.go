package main

import (
	"fmt"
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

func QRPNG(content string) ([]byte, error) {
	return qrcode.Encode(content, qrcode.Medium, 512)
}

// Runner applies service configuration to the host using wg / wg-quick.
type Runner struct{ Dir string }

func (r Runner) confPath(name string) string { return filepath.Join(r.Dir, name+".conf") }

func (r Runner) Write(s Service, users []User) error {
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(r.confPath(s.Name), []byte(ServerConfig(s, users)), 0o600)
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (r Runner) Active(name string) bool {
	return exec.Command("wg", "show", name).Run() == nil
}

func (r Runner) Start(name string) error { return run("wg-quick", "up", r.confPath(name)) }
func (r Runner) Stop(name string) error  { return run("wg-quick", "down", r.confPath(name)) }

// Sync pushes peer changes into a running interface without dropping it.
func (r Runner) Sync(name string) error {
	if !r.Active(name) {
		return nil
	}
	return run("bash", "-c", fmt.Sprintf("wg syncconf %q <(wg-quick strip %q)", name, r.confPath(name)))
}
