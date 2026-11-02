//go:build windows

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// resolveCommand turns what the user typed into something CreateProcess can
// actually launch. npm-installed CLIs (claude, opencode, etc.) are .cmd shims on
// Windows and cannot be started directly.
func resolveCommand(spec []string) (string, []string) {
	if len(spec) == 0 {
		return defaultShell(), nil
	}

	prog, args := spec[0], spec[1:]
	resolved, err := exec.LookPath(prog)
	if err != nil {
		return prog, args
	}

	switch strings.ToLower(filepath.Ext(resolved)) {
	case ".cmd", ".bat":
		return "cmd.exe", append([]string{"/d", "/c", resolved}, args...)
	case ".ps1":
		ps := "powershell.exe"
		if p, err := exec.LookPath("pwsh.exe"); err == nil {
			ps = p
		}
		return ps, append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", resolved}, args...)
	default:
		return resolved, args
	}
}

func defaultShell() string {
	for _, name := range []string{"pwsh.exe", "powershell.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return "cmd.exe"
}
