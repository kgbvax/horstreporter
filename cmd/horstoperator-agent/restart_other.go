//go:build !windows

package main

import (
	"os"
	"syscall"
)

// restartAgent on non-Windows re-execs the binary in place (syscall.Exec
// replaces the image, so there is no duplicate process and the PID is stable for
// any supervisor). The environment has the Settings-managed keys stripped so the
// new process re-reads them from the just-saved .env. Used mainly for dev/test;
// the tray itself is Windows-only.
func restartAgent(_ serviceConfig) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, restartArgs(), restartEnviron())
}
