package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/aymanbagabas/go-pty"
)

// historySize is how much raw terminal output is kept so a phone that
// reconnects (or locks its screen) can rebuild the screen.
const historySize = 1 << 20 // 1 MiB

// Session owns one pseudo-terminal and fans its output out to every attached
// WebSocket client. Input from any client is written straight to the pty, so
// the phone behaves exactly like a local terminal.
type Session struct {
	pty pty.Pty

	mu     sync.Mutex
	hist   *history
	subs   map[chan []byte]struct{}
	exited bool
	code   int

	done chan struct{}
}

func NewSession(name string, args []string, dir string, cols, rows int) (*Session, error) {
	p, err := pty.New()
	if err != nil {
		return nil, err
	}

	// Set the size before the process starts so the shell/agent sees the real
	// dimensions on its very first draw.
	if up, ok := p.(pty.UnixPty); ok && cols > 0 && rows > 0 {
		_ = up.SetWinsize(&pty.Winsize{Row: uint16(rows), Col: uint16(cols)})
	}

	cmd := p.Command(name, args...)
	cmd.Env = terminalEnv()
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		p.Close()
		return nil, err
	}

	s := &Session{
		pty:  p,
		hist: newHistory(historySize),
		subs: make(map[chan []byte]struct{}),
		done: make(chan struct{}),
	}
	if cols > 0 && rows > 0 {
		_ = p.Resize(cols, rows)
	}

	go s.readLoop()
	go s.waitLoop(cmd)
	return s, nil
}

func (s *Session) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			s.broadcast(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) waitLoop(cmd *pty.Cmd) {
	err := cmd.Wait()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			code = 1
		}
	}

	s.mu.Lock()
	if s.exited {
		s.mu.Unlock()
		return
	}
	s.exited = true
	s.code = code
	for ch := range s.subs {
		delete(s.subs, ch)
		close(ch)
	}
	s.mu.Unlock()

	// Closing the pty unblocks any read still in flight.
	_ = s.pty.Close()
	close(s.done)
}

func (s *Session) broadcast(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exited {
		return
	}
	s.hist.Write(p)
	if len(s.subs) == 0 {
		return
	}
	data := append([]byte(nil), p...)
	for ch := range s.subs {
		select {
		case ch <- data:
		default:
			// The client cannot keep up (usually a phone that lost signal).
			// Drop it; it will reconnect and replay history.
			delete(s.subs, ch)
			close(ch)
		}
	}
}

// attach registers a listener and returns everything needed to rebuild the
// screen. The snapshot and the subscription happen under one lock so no
// output falls between them.
func (s *Session) attach() (snapshot []byte, ch chan []byte, exited bool, code int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot = s.hist.Snapshot()
	if s.hist.Full() {
		snapshot = trimToBoundary(snapshot)
	}
	if s.exited {
		return snapshot, nil, true, s.code
	}
	ch = make(chan []byte, 1024)
	s.subs[ch] = struct{}{}
	return snapshot, ch, false, 0
}

func (s *Session) detach(ch chan []byte) {
	s.mu.Lock()
	if _, ok := s.subs[ch]; ok {
		delete(s.subs, ch)
		close(ch)
	}
	s.mu.Unlock()
}

func (s *Session) Write(p []byte) {
	s.mu.Lock()
	exited := s.exited
	s.mu.Unlock()
	if exited || len(p) == 0 {
		return
	}
	_, _ = s.pty.Write(p)
}

func (s *Session) Resize(cols, rows int) {
	if cols < 2 || rows < 2 || cols > 2000 || rows > 1000 {
		return
	}
	_ = s.pty.Resize(cols, rows)
}

func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) ExitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.code
}

func (s *Session) Close() {
	s.mu.Lock()
	exited := s.exited
	s.mu.Unlock()
	if exited {
		return
	}
	_ = s.pty.Close()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
	}
}

// trimToBoundary avoids replaying output that begins in the middle of an ANSI
// escape sequence, which would make xterm print escape soup.
func trimToBoundary(b []byte) []byte {
	window := len(b)
	if window > 4096 {
		window = 4096
	}
	if i := bytes.IndexByte(b[:window], 0x1b); i > 0 {
		return b[i:]
	}
	return b
}

// terminalEnv gives the remote shell a sane terminal description. Without
// this, TUI agents like Claude Code or opencode can fall back to a degraded
// (or completely broken) renderer.
func terminalEnv() []string {
	overrides := map[string]string{
		"TERM":         "xterm-256color",
		"COLORTERM":    "truecolor",
		"TERM_PROGRAM": "remoteterminal",
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := overrides[k]; ok {
			continue
		}
		// Windows Terminal / local terminal specific variables would confuse
		// programs about what the remote terminal can do.
		switch k {
		case "WT_SESSION", "WT_PROFILE_ID", "TERM_PROGRAM_VERSION":
			continue
		}
		env = append(env, kv)
	}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	if runtime.GOOS != "windows" {
		has := false
		for _, kv := range env {
			if strings.HasPrefix(kv, "LANG=") {
				has = true
				break
			}
		}
		if !has {
			env = append(env, "LANG=en_US.UTF-8")
		}
	}
	return env
}

// history is a byte ring buffer.
type history struct {
	buf   []byte
	start int
	n     int
}

func newHistory(max int) *history {
	return &history{buf: make([]byte, max)}
}

func (h *history) Write(p []byte) {
	max := len(h.buf)
	if len(p) == 0 {
		return
	}
	if len(p) >= max {
		copy(h.buf, p[len(p)-max:])
		h.start = 0
		h.n = max
		return
	}
	if h.n+len(p) > max {
		drop := h.n + len(p) - max
		h.start = (h.start + drop) % max
		h.n -= drop
	}
	end := (h.start + h.n) % max
	c := copy(h.buf[end:], p)
	copy(h.buf[:], p[c:])
	h.n += len(p)
}

func (h *history) Snapshot() []byte {
	out := make([]byte, h.n)
	c := copy(out, h.buf[h.start:min(h.start+h.n, len(h.buf))])
	copy(out[c:], h.buf[:h.n-c])
	return out
}

func (h *history) Full() bool { return h.n == len(h.buf) }
