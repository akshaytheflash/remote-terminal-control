package main

import (
	"fmt"
	"io"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// printQR draws a scannable QR code using half blocks. Colours are explicit
// (black modules on white) so it scans even in a dark terminal theme.
func printQR(w io.Writer, text, prefix string) {
	q, err := qrcode.New(text, qrcode.Medium)
	if err != nil {
		return
	}
	bmp := q.Bitmap()

	const (
		fgLight = "\x1b[38;2;255;255;255m"
		fgDark  = "\x1b[38;2;0;0;0m"
		bgLight = "\x1b[48;2;255;255;255m"
		bgDark  = "\x1b[48;2;0;0;0m"
	)

	var sb strings.Builder
	for y := 0; y < len(bmp); y += 2 {
		sb.WriteString(prefix)
		lastTop, lastBot := -1, -1
		for x := 0; x < len(bmp[y]); x++ {
			top, bot := 0, 0
			if bmp[y][x] {
				top = 1
			}
			if y+1 < len(bmp) && bmp[y+1][x] {
				bot = 1
			}
			if top != lastTop {
				if top == 1 {
					sb.WriteString(fgDark)
				} else {
					sb.WriteString(fgLight)
				}
				lastTop = top
			}
			if bot != lastBot {
				if bot == 1 {
					sb.WriteString(bgDark)
				} else {
					sb.WriteString(bgLight)
				}
				lastBot = bot
			}
			sb.WriteString("▀")
		}
		sb.WriteString("\x1b[0m\n")
	}
	fmt.Fprint(w, sb.String())
}
