package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const version = "0.1.0"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	switch args[0] {
	case "launch", "run", "start":
		if err := launch(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "\n  error: "+err.Error())
			os.Exit(1)
		}
	case "version", "--version", "-v":
		fmt.Println("remoteterminal " + version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `remoteterminal - use your pc's terminal from your phone

usage:
  remoteterminal launch [flags] [--] [command ...]

examples:
  remoteterminal launch                     # your default shell
  remoteterminal launch -- opencode         # start a coding agent
  remoteterminal launch --dir C:\code -- claude
  remoteterminal launch --no-tunnel         # local network only

flags:
  --port n          local port (default: random)
  --lan             listen on all interfaces instead of 127.0.0.1
  --no-tunnel       do not start a cloudflared tunnel
  --cloudflared p   path to the cloudflared binary
  --no-download     never download cloudflared automatically
  --dir p           working directory for the terminal
  --cols n          initial columns (default 100)
  --rows n          initial rows (default 30)
  --no-qr           do not print a QR code
  -h, --help        show this help
`)
}

func launch(args []string) error {
	fs := flag.NewFlagSet("launch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		port       = fs.Int("port", 0, "local port (0 = random)")
		lan        = fs.Bool("lan", false, "listen on all interfaces")
		noTunnel   = fs.Bool("no-tunnel", false, "do not start a tunnel")
		cfPath     = fs.String("cloudflared", "", "path to cloudflared")
		noDownload = fs.Bool("no-download", false, "never download cloudflared")
		dir        = fs.String("dir", "", "working directory")
		cols       = fs.Int("cols", 100, "initial columns")
		rows       = fs.Int("rows", 30, "initial rows")
		noQR       = fs.Bool("no-qr", false, "do not print a QR code")
	)
	fs.Usage = usage

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	cwd := *dir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if cwd != "" {
		if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
			return fmt.Errorf("working directory %q does not exist", cwd)
		}
	}

	spec := fs.Args()
	shellName, shellArgs := resolveCommand(spec)

	sess, err := NewSession(shellName, shellArgs, cwd, *cols, *rows)
	if err != nil {
		return fmt.Errorf("could not start %s: %w", shellName, err)
	}
	defer sess.Close()

	auth := NewAuth()
	app := NewApp(sess, auth)

	host := "127.0.0.1"
	if *lan {
		host = "0.0.0.0"
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, *port))
	if err != nil {
		return fmt.Errorf("could not listen: %w", err)
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port

	localURL := fmt.Sprintf("http://127.0.0.1:%d", actualPort)
	displayURL := localURL
	if *lan {
		if ip := outboundIP(); ip != "" {
			displayURL = fmt.Sprintf("http://%s:%d", ip, actualPort)
		}
	}

	srv := &http.Server{
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var tunnel *Tunnel
	publicURL := ""
	var tunnelErr error

	if !*noTunnel {
		fmt.Fprint(os.Stderr, dim("  starting cloudflared tunnel...")+"\r")
		bin, err := ensureCloudflared(*cfPath, !*noDownload)
		if err != nil {
			tunnelErr = err
		} else if tunnel, err = StartTunnel(ctx, bin, actualPort); err != nil {
			tunnelErr = err
		} else {
			publicURL = tunnel.URL
			displayURL = publicURL
		}
		// Clear the progress line.
		fmt.Fprint(os.Stderr, "\r"+strings.Repeat(" ", 40)+"\r")
	}

	shellLabel := filepath.Base(shellName)
	if len(spec) > 0 {
		shellLabel = strings.Join(spec, " ")
	}
	if len(shellLabel) > 64 {
		shellLabel = shellLabel[:61] + "..."
	}

	printBanner(publicURL, localURL, displayURL, shellLabel, auth.Password(), !*noQR, tunnelErr)

	quit := make(chan struct{})
	go controlKeys(auth, quit)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	exitNote := ""
	sessionEnded := false
	select {
	case <-sess.Done():
		sessionEnded = true
		exitNote = fmt.Sprintf("terminal exited (code %d)", sess.ExitCode())
	case <-quit:
		exitNote = "closed from the computer"
	case <-sig:
		exitNote = "interrupted"
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			exitNote = "server error: " + err.Error()
		}
	}

	fmt.Println()
	fmt.Println("  " + dim(exitNote))
	if sessionEnded {
		app.waitClients(1500 * time.Millisecond)
	}
	if tunnel != nil {
		tunnel.Stop()
	}
	cancel()
	_ = srv.Close()
	sess.Close()
	return nil
}

// controlKeys lets the person at the PC issue a fresh password or end the
// session without touching the phone.
func controlKeys(auth *Auth, quit chan struct{}) {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		switch strings.ToLower(strings.TrimSpace(sc.Text())) {
		case "p", "password":
			otp := auth.Rotate()
			fmt.Println()
			fmt.Println("  " + dim("new password ") + bold(green(otp)) + dim("  (one time use)"))
			fmt.Println()
		case "q", "quit", "exit":
			close(quit)
			return
		case "?", "h", "help":
			fmt.Println("  " + dim("p = new password | q = quit | ctrl+c = quit"))
		}
	}
}

func printBanner(publicURL, localURL, lanURL, shell, otp string, qr bool, tunnelErr error) {
	fmt.Println()
	fmt.Println("  " + bold("remoteterminal") + "  " + dim("your pc's terminal, on your phone"))
	fmt.Println()
	if publicURL != "" {
		fmt.Println("  " + dim("url       ") + cyan(publicURL))
	} else {
		fmt.Println("  " + dim("url       ") + cyan(lanURL))
	}
	fmt.Println("  " + dim("password  ") + bold(green(otp)) + dim("   one time use"))
	fmt.Println("  " + dim("shell     ") + shell)
	if publicURL != "" {
		fmt.Println("  " + dim("local     ") + localURL)
	}

	if tunnelErr != nil {
		fmt.Println()
		fmt.Println("  " + yellow("!") + " " + dim("tunnel unavailable: "+tunnelErr.Error()))
		if lanURL != localURL {
			fmt.Println("    " + dim("your phone can still reach "+lanURL+" on the same wi-fi"))
		} else {
			fmt.Println("    " + dim("run with --lan to allow phones on the same wi-fi"))
		}
	}

	if qr && publicURL != "" && isTerminal(os.Stdout) {
		fmt.Println()
		fmt.Println("  " + dim("scan to open:"))
		fmt.Println()
		printQR(os.Stdout, publicURL, "  ")
	}

	fmt.Println()
	fmt.Println("  " + dim("p + enter = new password | ctrl+c = quit"))
	fmt.Println()
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func outboundIP() string {
	c, err := net.Dial("udp4", "1.1.1.1:53")
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}
