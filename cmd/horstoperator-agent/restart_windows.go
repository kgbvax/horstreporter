//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

const (
	// Detach the relauncher/child so it survives this process exiting.
	detachedProcess        = 0x00000008
	createNewProcessGroup  = 0x00000200
	createNewConsole       = 0x00000010
	createBreakawayFromJob = 0x01000000
)

// restartAgent restarts the agent to apply freshly-saved .env settings.
//
// When the agent knows its scheduled-task name (TaskName), it bounces the task
// (/End then /Run) via a detached cmd. That keeps the *task* owning the new
// instance, so there's no duplicate process at the next logon. Otherwise it
// self-respawns a detached copy with the same args. Either way the child gets a
// clean environment (managed keys stripped) so the new .env values take effect.
func restartAgent(cfg serviceConfig) error {
	if cfg.TaskName != "" {
		// timeout gives this process a beat to exit first; then the task is
		// stopped (no-op if already gone) and started fresh.
		script := `timeout /t 1 /nobreak >nul & schtasks /End /TN "` + cfg.TaskName + `" & schtasks /Run /TN "` + cfg.TaskName + `"`
		cmd := exec.Command("cmd", "/c", script)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup | createBreakawayFromJob}
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { time.Sleep(2 * time.Second); os.Exit(0) }()
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, restartArgs()[1:]...)
	cmd.Env = restartEnviron()
	if wd, err := os.Getwd(); err == nil {
		cmd.Dir = wd
	}
	// New console keeps the tray/GUI child independent of the dying parent.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewConsole | createNewProcessGroup | createBreakawayFromJob}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { time.Sleep(500 * time.Millisecond); os.Exit(0) }()
	return nil
}
