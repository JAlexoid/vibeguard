package main

import (
	"fmt"
	"os"
	"slices"
)

// MDNSFirewall updates local relay permissions without giving the UI NET_ADMIN.
// The native oneshot reads the atomically saved settings as root. Containers
// already have NET_ADMIN and invoke the same fixed helper directly.
func (r Runner) MDNSFirewall(cfg MDNSSettings) error {
	if r.Systemd {
		return r.systemctl("restart", "vibeguard-mdns-firewall.service")
	}
	return applyMDNSFirewall(cfg, r.run)
}

func applyMDNSFirewall(cfg MDNSSettings, command func(string, ...string) error) error {
	args := []string{"mdns"}
	if cfg.Enabled {
		for _, name := range append(append([]string{}, cfg.Upstreams...), cfg.Services...) {
			if !nameRe.MatchString(name) {
				return fmt.Errorf("invalid mDNS interface name %q", name)
			}
			if !slices.Contains(args[1:], name) {
				args = append(args, name)
			}
		}
	}
	return command(env("VIBEGUARD_NETWORK_HELPER", "/usr/local/libexec/vibeguard-network"), args...)
}

func runMDNSFirewallCommand(args []string) error {
	if len(args) > 1 || (len(args) == 1 && args[0] != "--clear") {
		return fmt.Errorf("usage: vibeguard mdns-firewall [--clear]")
	}
	cfg := MDNSSettings{}
	if len(args) == 0 {
		st, err := NewStore(env("VIBEGUARD_DATA", "/var/lib/vibeguard/data.json"))
		if err != nil {
			return err
		}
		cfg = st.MDNS(NewReflector(os.Getenv("VIBEGUARD_MDNS_UPSTREAM"), st).defaults)
	}
	return applyMDNSFirewall(cfg, run)
}
