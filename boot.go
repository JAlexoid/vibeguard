package main

import (
	"fmt"
	"os"
	"os/exec"
)

func unitName(name string) string { return "wg-quick@" + name + ".service" }

func systemdAvailable() bool {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false
	}
	_, err := exec.LookPath("systemctl")
	return err == nil
}

func (r Runner) systemctl(args ...string) error {
	return r.run("systemctl", append([]string{"--no-ask-password"}, args...)...)
}

func (r Runner) Enabled(name string) bool {
	return r.Systemd && r.systemctl("is-enabled", "--quiet", unitName(name)) == nil
}

func newRunner(dir, mode string) (Runner, error) {
	switch mode {
	case "auto", "":
		return Runner{Dir: dir, Systemd: systemdAvailable()}, nil
	case "1":
		if !systemdAvailable() {
			return Runner{}, fmt.Errorf("VIBEGUARD_SYSTEMD=1 requires a running systemd system manager")
		}
		return Runner{Dir: dir, Systemd: true}, nil
	case "0":
		return Runner{Dir: dir}, nil
	default:
		return Runner{}, fmt.Errorf("VIBEGUARD_SYSTEMD must be auto, 1, or 0")
	}
}
