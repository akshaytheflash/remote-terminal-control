package main

import (
	"os"
	"strings"
)

var colorOK = isTerminal(os.Stdout) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"

func paint(code, s string) string {
	if !colorOK {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func bold(s string) string   { return paint("1", s) }
func dim(s string) string    { return paint("2", s) }
func green(s string) string  { return paint("38;5;114", s) }
func cyan(s string) string   { return paint("38;5;81", s) }
func yellow(s string) string { return paint("38;5;221", s) }

func stripANSI(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && !(s[i] >= 0x40 && s[i] <= 0x7e) {
				i++
			}
			i++ // final byte
			continue
		}
		sb.WriteByte(s[i])
		i++
	}
	return sb.String()
}
