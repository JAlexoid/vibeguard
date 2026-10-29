package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

func mdnsDefaults(upstream string) MDNSSettings {
	cfg := MDNSSettings{Enabled: strings.TrimSpace(upstream) != ""}
	for _, name := range strings.Split(upstream, ",") {
		if name = strings.TrimSpace(name); name != "" {
			cfg.Upstreams = append(cfg.Upstreams, name)
		}
	}
	return cfg
}

// Avahi owns the multicast sockets. The native UI only restarts this fixed unit;
// its privileged entry point reads settings and constructs the daemon config.
func (r Runner) MDNSReflector(cfg MDNSSettings) error {
	if r.Systemd {
		return r.systemctl("restart", "vibeguard-avahi.service")
	}
	return applyAvahi(cfg, r.run)
}

func avahiConfig(cfg MDNSSettings) ([]byte, error) {
	names := []string{}
	for _, name := range append(append([]string{}, cfg.Upstreams...), cfg.Services...) {
		if !nameRe.MatchString(name) {
			return nil, fmt.Errorf("invalid mDNS interface name %q", name)
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	if cfg.Enabled && (len(cfg.Upstreams) == 0 || len(cfg.Services) == 0) {
		return nil, fmt.Errorf("select LAN and VPN interfaces for Avahi")
	}
	return []byte("[server]\nuse-ipv4=yes\nuse-ipv6=yes\nenable-dbus=no\nallow-point-to-point=yes\nallow-interfaces=" + strings.Join(names, ",") + "\n[publish]\ndisable-publishing=yes\n[reflector]\nenable-reflector=yes\nreflect-ipv=no\n"), nil
}

func writeAvahiConfig(cfg MDNSSettings) (string, error) {
	data, err := avahiConfig(cfg)
	if err != nil {
		return "", err
	}
	path := env("VIBEGUARD_AVAHI_CONFIG", "/run/vibeguard-avahi/avahi-daemon.conf")
	return path, atomicWrite(path, data)
}

func applyAvahi(cfg MDNSSettings, command func(string, ...string) error) error {
	// --check distinguishes an absent daemon from a failed stop. Direct mode runs
	// in a dedicated container with no system Avahi instance.
	if err := command("avahi-daemon", "--check"); err == nil {
		if err := command("avahi-daemon", "--kill"); err != nil {
			return err
		}
	}
	if !cfg.Enabled {
		return nil
	}
	path, err := writeAvahiConfig(cfg)
	if err != nil {
		return err
	}
	return command("avahi-daemon", "--daemonize", "--no-chroot", "--no-rlimits", "--file="+path)
}

func runAvahiCommand(args []string) error {
	if len(args) == 1 && args[0] == "--stop" {
		return applyAvahi(MDNSSettings{}, run)
	}
	if len(args) != 0 {
		return fmt.Errorf("usage: vibeguard avahi [--stop]")
	}
	st, err := NewStore(env("VIBEGUARD_DATA", "/var/lib/vibeguard/data.json"))
	if err != nil {
		return err
	}
	return applyAvahi(st.MDNS(mdnsDefaults(os.Getenv("VIBEGUARD_MDNS_UPSTREAM"))), run)
}
