package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// App ties the HTTP surface to the terminal session and the auth state.
type App struct {
	sess *Session
	auth *Auth
	up   websocket.Upgrader
	wg   sync.WaitGroup
}

type controlMsg struct {
	Type string `json:"type"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Code int    `json:"code,omitempty"`
}

func NewApp(sess *Session, auth *Auth) *App {
	return &App{
		sess: sess,
		auth: auth,
		up: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 32 * 1024,
			CheckOrigin:     checkOrigin,
		},
	}
}

func (a *App) Handler() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	static := http.FileServer(http.FS(sub))

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		if r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, "index.html") {
			w.Header().Set("Cache-Control", "no-store")
		}
		static.ServeHTTP(w, r)
	})
	mux.HandleFunc("/api/auth", a.handleAuth)
	mux.HandleFunc("/api/logout", a.handleLogout)
	mux.HandleFunc("/api/me", a.handleMe)
	mux.HandleFunc("/ws", a.handleWS)

	return secureHeaders(mux)
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
				"script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": a.auth.authed(r)})
}

func (a *App) handleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return
	}

	token, err := a.auth.Login(req.Password)
	if err != nil {
		// A small constant delay makes guessing even more pointless.
		time.Sleep(250 * time.Millisecond)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
		return
	}

	setAuthCookie(w, r, token)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c, err := r.Cookie(cookieName); err == nil {
		a.auth.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// waitClients gives connected browsers a moment to receive the final frames
// before the process goes away.
func (a *App) waitClients(d time.Duration) {
	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}

func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser client (curl, scripts)
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func (a *App) handleWS(w http.ResponseWriter, r *http.Request) {
	if !a.auth.authed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := a.up.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	a.wg.Add(1)
	defer a.wg.Done()

	snapshot, ch, exited, code := a.sess.attach()

	// Send the replay before any live frame so the client rebuilds the screen
	// in the right order.
	_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if err := conn.WriteJSON(controlMsg{Type: "hello"}); err != nil {
		conn.Close()
		if ch != nil {
			a.sess.detach(ch)
		}
		return
	}
	if len(snapshot) > 0 {
		if err := conn.WriteMessage(websocket.BinaryMessage, snapshot); err != nil {
			conn.Close()
			if ch != nil {
				a.sess.detach(ch)
			}
			return
		}
	}
	if exited {
		_ = conn.WriteJSON(controlMsg{Type: "exit", Code: code})
		time.Sleep(100 * time.Millisecond)
		conn.Close()
		return
	}

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case msg, ok := <-ch:
				if !ok {
					// The terminal process ended: tell the client why and
					// hang up instead of making it reconnect to find out.
					_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
					_ = conn.WriteJSON(controlMsg{Type: "exit", Code: a.sess.ExitCode()})
					_ = conn.WriteControl(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session ended"),
						time.Now().Add(2*time.Second))
					_ = conn.Close()
					return
				}
				_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
				if err := conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
					return
				}
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					return
				}
			}
		}
	}()

	defer func() {
		a.sess.detach(ch)
		_ = conn.Close()
		<-writerDone
	}()

	conn.SetReadLimit(4 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	})

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch mt {
		case websocket.BinaryMessage:
			a.sess.Write(data)
		case websocket.TextMessage:
			var msg controlMsg
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			if msg.Type == "resize" {
				a.sess.Resize(msg.Cols, msg.Rows)
			}
		}
	}
}
