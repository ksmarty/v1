// Package terminal provides per-project WebSocket PTY terminals.
package terminal

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// Manager tracks active terminal sessions per project.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]map[*session]struct{}
}

type session struct {
	cmd  *exec.Cmd
	ptmx *os.File
	once sync.Once
}

// NewManager creates an empty Manager.
func NewManager() *Manager {
	return &Manager{sessions: map[string]map[*session]struct{}{}}
}

var upgrader = websocket.Upgrader{
	// Same-origin is enforced by cookie auth; behind reverse proxies the
	// Origin header may not match the Host, so do not reject on Origin.
	CheckOrigin: func(r *http.Request) bool { return true },
}

type clientMsg struct {
	Type string `json:"type"`
	Data string `json:"data"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// ServeWS upgrades the request to a WebSocket and bridges it to a shell PTY
// rooted at dir. The shell is killed when the socket closes.
func (m *Manager) ServeWS(w http.ResponseWriter, r *http.Request, projectID, dir string) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Keepalive: reverse proxies (Cloudflare, Traefik) drop WebSockets that
	// sit idle — an open terminal with no output looks exactly like that.
	// Ping every 25s and treat any pong as proof of life.
	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-t.C:
				// WriteControl is safe alongside the output goroutine's writes.
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
					return
				}
			}
		}
	}()

	shell := "bash"
	if _, err := exec.LookPath("bash"); err != nil {
		shell = "sh"
	}
	cmd := exec.Command(shell)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		return
	}

	s := &session{cmd: cmd, ptmx: ptmx}
	m.mu.Lock()
	if m.sessions[projectID] == nil {
		m.sessions[projectID] = map[*session]struct{}{}
	}
	m.sessions[projectID][s] = struct{}{}
	m.mu.Unlock()
	defer func() {
		s.kill()
		m.mu.Lock()
		delete(m.sessions[projectID], s)
		if len(m.sessions[projectID]) == 0 {
			delete(m.sessions, projectID)
		}
		m.mu.Unlock()
	}()

	// pty -> ws
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				payload, _ := json.Marshal(map[string]any{"type": "output", "data": string(buf[:n])})
				if werr := conn.WriteMessage(websocket.TextMessage, payload); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// ws -> pty
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg clientMsg
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "input":
			_, _ = ptmx.Write([]byte(msg.Data))
		case "resize":
			if msg.Cols > 0 && msg.Rows > 0 {
				_ = pty.Setsize(ptmx, &pty.Winsize{Cols: msg.Cols, Rows: msg.Rows})
			}
		}
	}
}

func (s *session) kill() {
	s.once.Do(func() {
		if s.ptmx != nil {
			_ = s.ptmx.Close()
		}
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
	})
}

// KillProject terminates all terminal sessions for a project.
func (m *Manager) KillProject(projectID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for s := range m.sessions[projectID] {
		s.kill()
	}
	delete(m.sessions, projectID)
}

// KillAll terminates every terminal session (used on shutdown).
func (m *Manager) KillAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.sessions {
		for s := range m.sessions[id] {
			s.kill()
		}
		delete(m.sessions, id)
	}
}
