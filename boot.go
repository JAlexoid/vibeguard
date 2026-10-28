package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// wgQuickDir is where wg-quick@<iface>.service looks for <iface>.conf.
var wgQuickDir = "/etc/wireguard"

// systemctlPath returns the systemctl binary, or "" when systemd isn't usable
// (no systemctl, or the host wasn't booted with systemd, e.g. Docker).
func systemctlPath() string {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return ""
	}
	p, err := exec.LookPath("systemctl")
	if err != nil {
		return ""
	}
	return p
}

// EnableBoot makes wg-quick@<name> start on boot. It is idempotent and a no-op
// (returning nil) where systemd is unavailable. Needs root, since it writes to
// /etc/systemd and /etc/wireguard.
func (r Runner) EnableBoot(name string) error {
	sc := systemctlPath()
	if sc == "" {
		return nil
	}
	if err := r.linkQuickConf(name); err != nil {
		return fmt.Errorf("link wg-quick config for %s: %v", name, err)
	}
	return run(sc, "enable", "wg-quick@"+name+".service")
}

// DisableBoot undoes EnableBoot and removes the config link it created.
func (r Runner) DisableBoot(name string) error {
	sc := systemctlPath()
	if sc == "" {
		return nil
	}
	if err := run(sc, "disable", "wg-quick@"+name+".service"); err != nil {
		return err
	}
	if link := filepath.Join(wgQuickDir, name+".conf"); filepath.Clean(r.Dir) != filepath.Clean(wgQuickDir) {
		if t, err := os.Readlink(link); err == nil && t == r.confPath(name) {
			return os.Remove(link)
		}
	}
	return nil
}

// linkQuickConf exposes the config to wg-quick when it lives outside wgQuickDir.
func (r Runner) linkQuickConf(name string) error {
	if filepath.Clean(r.Dir) == filepath.Clean(wgQuickDir) {
		return nil
	}
	link, target := filepath.Join(wgQuickDir, name+".conf"), r.confPath(name)
	if t, err := os.Readlink(link); err == nil && t == target {
		return nil
	}
	if err := os.MkdirAll(wgQuickDir, 0o700); err != nil {
		return err
	}
	if _, err := os.Lstat(link); err == nil {
		return fmt.Errorf("%s already exists", link)
	}
	return os.Symlink(target, link)
}
