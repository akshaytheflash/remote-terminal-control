//go:build !windows

package main

import (
	"os"
	"os/exec"
)

func resolveCommand(spec []string) (string, []string) {
	if len(spec) == 0 {
		return defaultShell(), nil
	}
	return spec[0], spec[1:]
}

func defaultShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	for _, s := range []string{"/bin/bash", "/bin/zsh", "/bin/sh"} {
		if _, err := exec.LookPath(s); err == nil {
			return s
		}
	}
	return "/bin/sh"
}
