# remoteterminal

Run your PC's terminal from your phone.

`remoteterminal launch` starts a shell, opens a temporary Cloudflare tunnel,
and prints a URL and a one-time password. Open the URL on your phone, enter the
password, and you get the same terminal. Output is live, typing goes straight
to the shell, and a key bar gives you Esc, Tab, Ctrl, Alt, arrows, Enter and
Ctrl+C.

It runs anything that runs in a terminal, including Claude Code and opencode.

## Requirements

- Go 1.25 or newer to build.
- cloudflared. If it is missing, the first run downloads the official binary
  into your user cache directory.
- Windows 10 1809 or newer (ConPTY), macOS, or Linux.

## Build

```sh
go build -o remoteterminal .
```

On Windows:

```powershell
go build -o remoteterminal.exe .
```

Put the binary somewhere on your PATH so `remoteterminal launch` works from any
directory.

## Use

```sh
remoteterminal launch
remoteterminal launch -- opencode
remoteterminal launch --dir C:\code\app -- claude
```

The tool prints something like:

```
  url       https://something.trycloudflare.com
  password  ABCD-EFGH   one time use
```

Scan the QR code or open the URL, then enter the password. The password works
once. Press `p` and Enter on the computer to issue a new one.

Flags:

| Flag | Meaning |
| --- | --- |
| `--port n` | Local port. Random by default. |
| `--lan` | Listen on all interfaces instead of 127.0.0.1. |
| `--no-tunnel` | Skip cloudflared and serve locally. |
| `--cloudflared path` | Use a specific cloudflared binary. |
| `--no-download` | Never download cloudflared. |
| `--dir path` | Working directory for the terminal. |
| `--cols n`, `--rows n` | Initial terminal size. Default 100x30. |
| `--no-qr` | Do not print the QR code. |

## How it works

The program runs a real PTY: ConPTY on Windows, a Unix pty elsewhere. The
browser draws the raw byte stream with xterm.js, so full-screen apps, colors
and mouse reporting behave normally. Input and output travel over one
WebSocket. The last 1 MiB of output is kept in memory, so a phone that locked
its screen can reconnect and rebuild the view.

cloudflared quick tunnels do not need a Cloudflare account. The URL is random
and lives only as long as the process.

## Security

- The HTTP server binds to 127.0.0.1. Only cloudflared can reach it.
- The URL is public, so the password is the lock.
- Passwords are 8 characters from a 32 character alphabet, single use, and
  rate limited with a growing lockout.
- Sessions use an HttpOnly, Secure, SameSite=Strict cookie.
- Anyone with the URL and the password gets a shell on your machine. Keep
  sessions short and press Ctrl+C when you are done.

## Notes

- Ctrl+C stops both the terminal and the tunnel. Killing the process from Task
  Manager can leave cloudflared running.
- `--no-tunnel` without `--lan` only accepts connections from the same machine.

## License

MIT
