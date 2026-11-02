package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

var trycloudflareRE = regexp.MustCompile(`https://[a-zA-Z0-9-]+\.trycloudflare\.com`)

// Tunnel is a running cloudflared quick tunnel.
type Tunnel struct {
	cmd  *exec.Cmd
	URL  string
	wait chan error

	stopOnce sync.Once
	mu       sync.Mutex
	logs     []string
}

func (t *Tunnel) log(line string) {
	t.mu.Lock()
	t.logs = append(t.logs, stripANSI(line))
	if len(t.logs) > 40 {
		t.logs = t.logs[len(t.logs)-40:]
	}
	t.mu.Unlock()
}

func (t *Tunnel) tail(n int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := t.logs
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func StartTunnel(ctx context.Context, bin string, port int) (*Tunnel, error) {
	cmd := exec.CommandContext(ctx, bin, "tunnel",
		"--url", fmt.Sprintf("http://127.0.0.1:%d", port),
		"--no-autoupdate",
	)

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		pr.Close()
		return nil, err
	}

	t := &Tunnel{cmd: cmd, wait: make(chan error, 1)}
	urlCh := make(chan string, 1)

	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			t.log(line)
			if m := trycloudflareRE.FindString(line); m != "" {
				select {
				case urlCh <- m:
				default:
				}
			}
		}
		pr.Close()
	}()

	go func() {
		err := cmd.Wait()
		pw.Close()
		t.wait <- err
	}()

	select {
	case u := <-urlCh:
		t.URL = u
		return t, nil
	case err := <-t.wait:
		return nil, fmt.Errorf("cloudflared exited (%v)\n%s", err, indent(t.tail(8)))
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("timed out waiting for the tunnel url\n%s", indent(t.tail(8)))
	}
}

func (t *Tunnel) Stop() {
	if t == nil {
		return
	}
	t.stopOnce.Do(func() {
		if t.cmd != nil && t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		select {
		case <-t.wait:
		case <-time.After(3 * time.Second):
		}
	})
}

func indent(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// ensureCloudflared finds a usable cloudflared binary, downloading the
// official release into the user cache directory if necessary.
func ensureCloudflared(explicit string, allowDownload bool) (string, error) {
	if explicit != "" {
		if st, err := os.Stat(explicit); err != nil || st.IsDir() {
			return "", fmt.Errorf("cloudflared not found at %s", explicit)
		}
		return explicit, nil
	}
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p, nil
	}

	cache, err := os.UserCacheDir()
	if err != nil {
		return "", errors.New("cloudflared not found in PATH")
	}
	dest := filepath.Join(cache, "remoteterminal", "cloudflared"+exeSuffix())
	if st, err := os.Stat(dest); err == nil && !st.IsDir() {
		return dest, nil
	}
	if !allowDownload {
		return "", errors.New("cloudflared not found in PATH (install it, or drop --no-download)")
	}

	fmt.Fprintln(os.Stderr, dim("  cloudflared not found, downloading it once..."))
	if err := downloadCloudflared(dest); err != nil {
		return "", fmt.Errorf("cloudflared not installed and download failed: %w", err)
	}
	return dest, nil
}

func downloadCloudflared(dest string) error {
	asset, isArchive, err := cloudflaredAsset()
	if err != nil {
		return err
	}

	url := "https://github.com/cloudflare/cloudflared/releases/latest/download/" + asset
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "remoteterminal/"+version)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", asset, resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".download"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)

	prog := &progressReader{r: resp.Body}

	if isArchive {
		gz, err := gzip.NewReader(prog)
		if err != nil {
			f.Close()
			return err
		}
		defer gz.Close()

		tr := tar.NewReader(gz)
		found := false
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				f.Close()
				return err
			}
			if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "cloudflared" {
				continue
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			found = true
			break
		}
		if !found {
			f.Close()
			return errors.New("cloudflared binary not found in archive")
		}
	} else {
		if _, err := io.Copy(f, prog); err != nil {
			f.Close()
			return err
		}
	}

	fmt.Fprint(os.Stderr, "\r"+strings.Repeat(" ", 44)+"\r")
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

type progressReader struct {
	r     io.Reader
	total int64
	last  int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.total += int64(n)
	if p.total-p.last >= 4<<20 {
		p.last = p.total
		fmt.Fprintf(os.Stderr, "\r  downloading cloudflared... %d MB", p.total>>20)
	}
	return n, err
}

func cloudflaredAsset() (string, bool, error) {
	switch runtime.GOOS {
	case "windows":
		if runtime.GOARCH == "386" {
			return "cloudflared-windows-386.exe", false, nil
		}
		return "cloudflared-windows-amd64.exe", false, nil
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return "cloudflared-darwin-arm64.tgz", true, nil
		}
		return "cloudflared-darwin-amd64.tgz", true, nil
	case "linux":
		switch runtime.GOARCH {
		case "arm64":
			return "cloudflared-linux-arm64", false, nil
		case "arm":
			return "cloudflared-linux-arm", false, nil
		case "386":
			return "cloudflared-linux-386", false, nil
		}
		return "cloudflared-linux-amd64", false, nil
	case "freebsd":
		return "cloudflared-freebsd-amd64", false, nil
	}
	return "", false, fmt.Errorf("no cloudflared build for %s/%s - install it manually", runtime.GOOS, runtime.GOARCH)
}
