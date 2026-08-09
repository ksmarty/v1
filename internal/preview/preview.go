// Package preview manages per-project dev-server and static previews.
package preview

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	portMin = 4100
	portMax = 4199
)

// ringLog keeps the last N lines of output.
type ringLog struct {
	mu    sync.Mutex
	lines []string
	max   int
	cur   string
}

func newRingLog(max int) *ringLog { return &ringLog{max: max} }

func (r *ringLog) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cur += string(p)
	for {
		i := strings.IndexByte(r.cur, '\n')
		if i < 0 {
			break
		}
		r.lines = append(r.lines, r.cur[:i])
		r.cur = r.cur[i+1:]
		if len(r.lines) > r.max {
			r.lines = r.lines[len(r.lines)-r.max:]
		}
	}
	return len(p), nil
}

func (r *ringLog) tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	lines := append([]string(nil), r.lines...)
	if r.cur != "" {
		lines = append(lines, r.cur)
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func (r *ringLog) String(n int) string { return strings.Join(r.tail(n), "\n") }

// Preview is one project's preview state.
type Preview struct {
	ProjectID string
	Mode      string // "static" or "node"
	Port      int
	Vite      bool
	URL       string

	cmd  *exec.Cmd
	pgid int
	logs *ringLog
	done chan struct{}

	mu       sync.Mutex
	running  bool
	lastUsed time.Time
}

func (p *Preview) isRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func (p *Preview) touch() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastUsed = time.Now()
}

func (p *Preview) setRunning(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running = v
}

func (p *Preview) exited() bool {
	if p.done == nil {
		return false
	}
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Manager tracks running previews and enforces the concurrency cap.
type Manager struct {
	max     int
	mu      sync.Mutex
	items   map[string]*Preview
	startMu sync.Mutex
	revs    map[string]int64
}

// NewManager creates a Manager allowing up to max concurrent previews.
func NewManager(max int) *Manager {
	if max <= 0 {
		max = 3
	}
	return &Manager{max: max, items: map[string]*Preview{}, revs: map[string]int64{}}
}

// TouchRevision bumps a project's file-revision counter. Every write to a
// project's files (agent tools and the file API) calls this so the preview
// pane can reload the iframe when the workspace changes.
func (m *Manager) TouchRevision(projectID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revs[projectID]++
}

// Revision returns the current file-revision counter for a project.
func (m *Manager) Revision(projectID string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.revs[projectID]
}

// DirectURL returns the dev-server URL that serves the app at its root, or ""
// when the preview is static (served only through the v1 proxy) or not
// running. Headless tools (screenshot) use this to skip the auth-protected
// proxy.
func (m *Manager) DirectURL(projectID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.items[projectID]
	if !ok || p.Mode != "node" || p.Port == 0 {
		return ""
	}
	base := fmt.Sprintf("http://127.0.0.1:%d/", p.Port)
	if p.Vite {
		// Vite dev servers run with --base so they serve under the proxy path.
		base = fmt.Sprintf("http://127.0.0.1:%d/preview/%s/", p.Port, projectID)
	}
	return base
}

// Start starts (or returns the existing) preview for a project and returns
// its relative URL once it is ready to serve.
func (m *Manager) Start(projectID, dir, previewCommand string) (string, error) {
	m.startMu.Lock()
	defer m.startMu.Unlock()

	m.mu.Lock()
	if p, ok := m.items[projectID]; ok && p.isRunning() {
		p.touch()
		url := p.URL
		m.mu.Unlock()
		return url, nil
	}
	m.mu.Unlock()

	url := "/preview/" + projectID + "/"

	// Static projects need no subprocess at all.
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		p := &Preview{
			ProjectID: projectID,
			Mode:      "static",
			URL:       url,
			logs:      newRingLog(200),
			running:   true,
			lastUsed:  time.Now(),
		}
		m.mu.Lock()
		m.items[projectID] = p
		m.evictLocked(projectID)
		m.mu.Unlock()
		return url, nil
	}

	// Enforce the concurrency cap before doing heavy work.
	m.mu.Lock()
	m.evictLocked(projectID)
	m.mu.Unlock()

	p := &Preview{
		ProjectID: projectID,
		Mode:      "node",
		URL:       url,
		logs:      newRingLog(200),
		done:      make(chan struct{}),
		lastUsed:  time.Now(),
	}
	m.mu.Lock()
	m.items[projectID] = p
	m.mu.Unlock()

	fail := func(err error) (string, error) {
		m.kill(p)
		m.mu.Lock()
		delete(m.items, projectID)
		m.mu.Unlock()
		return "", err
	}

	port, err := m.freePort()
	if err != nil {
		return fail(err)
	}
	p.Port = port

	// Install dependencies if node_modules is missing.
	if _, err := os.Stat(filepath.Join(dir, "node_modules")); err != nil {
		if err := m.installDeps(dir, p.logs); err != nil {
			return fail(fmt.Errorf("dependency install failed: %v\n%s", err, p.logs.String(20)))
		}
	}

	p.Vite = hasViteDep(filepath.Join(dir, "package.json"))

	var cmd *exec.Cmd
	if previewCommand != "" {
		cmd = exec.Command("sh", "-c", previewCommand)
	} else {
		pm := packageManager()
		devArgs := []string{"--port", fmt.Sprint(port), "--host", "127.0.0.1"}
		if p.Vite {
			// vite needs a base path to be proxied under a subpath.
			devArgs = append(devArgs, "--base", "/preview/"+projectID+"/")
		}
		var args []string
		if pm == "pnpm" {
			// pnpm forwards the script args verbatim (no "--" separator).
			// Disable pnpm >=10/11 guards that would otherwise fail the run:
			// the pre-run deps status check (which re-runs install) and
			// strict-dep-builds.
			args = append([]string{
				"--config.verify-deps-before-run=false",
				"--config.strict-dep-builds=false",
				"run", "dev",
			}, devArgs...)
		} else {
			// npm needs "--" before script args.
			args = append([]string{"run", "dev", "--"}, devArgs...)
		}
		cmd = exec.Command(pm, args...)
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = p.logs
	cmd.Stderr = p.logs
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	p.cmd = cmd
	p.pgid = cmd.Process.Pid

	go func() {
		_ = cmd.Wait()
		p.setRunning(false)
		close(p.done)
	}()

	// Wait for the port to accept connections (max 60s).
	deadline := time.Now().Add(60 * time.Second)
	for {
		if p.exited() {
			return fail(fmt.Errorf("dev server exited before accepting connections\n%s", p.logs.String(20)))
		}
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), 300*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			return fail(fmt.Errorf("dev server did not start within 60s\n%s", p.logs.String(20)))
		}
		time.Sleep(300 * time.Millisecond)
	}

	p.setRunning(true)
	return url, nil
}

// Stop stops a project's preview and removes it from the manager.
func (m *Manager) Stop(projectID string) {
	m.mu.Lock()
	p, ok := m.items[projectID]
	delete(m.items, projectID)
	m.mu.Unlock()
	if ok {
		m.kill(p)
	}
}

// StopAll stops every running preview (used on shutdown).
func (m *Manager) StopAll() {
	m.mu.Lock()
	items := m.items
	m.items = map[string]*Preview{}
	m.mu.Unlock()
	for _, p := range items {
		m.kill(p)
	}
}

// Get returns the running preview for a project, bumping its LRU timestamp.
func (m *Manager) Get(projectID string) *Preview {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.items[projectID]
	if !ok || !p.isRunning() {
		return nil
	}
	p.touch()
	return p
}

// Status returns the preview state and the last ~50 log lines.
func (m *Manager) Status(projectID string) (running bool, url *string, logs string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.items[projectID]
	if !ok {
		return false, nil, ""
	}
	running = p.isRunning()
	if running {
		u := p.URL
		url = &u
	}
	return running, url, p.logs.String(50)
}

// kill terminates a preview's process group.
func (m *Manager) kill(p *Preview) {
	p.setRunning(false)
	if p.cmd != nil && p.pgid > 0 {
		_ = syscall.Kill(-p.pgid, syscall.SIGKILL)
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// evictLocked stops the least-recently-used other preview if at cap.
// Callers must hold m.mu.
func (m *Manager) evictLocked(exceptID string) {
	running := 0
	for _, p := range m.items {
		if p.isRunning() {
			running++
		}
	}
	if running < m.max {
		return
	}
	var victim *Preview
	for id, p := range m.items {
		if id == exceptID || !p.isRunning() {
			continue
		}
		if victim == nil || p.lastUsed.Before(victim.lastUsed) {
			victim = p
		}
	}
	if victim != nil {
		delete(m.items, victim.ProjectID)
		// Kill outside the lock is not required; SIGKILL is async anyway.
		go m.kill(victim)
	}
}

// freePort finds an unused port in the preview range.
func (m *Manager) freePort() (int, error) {
	m.mu.Lock()
	used := map[int]bool{}
	for _, p := range m.items {
		if p.Port != 0 {
			used[p.Port] = true
		}
	}
	m.mu.Unlock()
	for port := portMin; port <= portMax; port++ {
		if used[port] {
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
		if err == nil {
			ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free preview ports in range %d-%d", portMin, portMax)
}

// packageManager prefers pnpm when on PATH, else npm.
func packageManager() string {
	if _, err := exec.LookPath("pnpm"); err == nil {
		return "pnpm"
	}
	return "npm"
}

// DefaultPreviewCommand is the command used when a project has no preview
// override — empty for static projects (no package.json, nothing to run).
func DefaultPreviewCommand(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		return ""
	}
	return packageManager() + " run dev"
}

// installDeps runs the package manager install, falling back to npm when the
// preferred manager fails (e.g. pnpm refusing build scripts).
func (m *Manager) installDeps(dir string, logs *ringLog) error {
	pms := []string{packageManager()}
	if pms[0] != "npm" {
		if _, err := exec.LookPath("npm"); err == nil {
			pms = append(pms, "npm")
		}
	}
	var lastErr error
	for _, pm := range pms {
		fmt.Fprintf(logs, "$ %s install\n", pm)
		args := []string{"install"}
		if pm == "pnpm" {
			// pnpm >=10 fails installs with blocked build scripts when the
			// user has strict-dep-builds enabled; modern packages (esbuild,
			// etc.) ship platform binaries via optionalDependencies and work
			// without their postinstall, so don't let that kill the install.
			args = append(args, "--config.strict-dep-builds=false")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		install := exec.CommandContext(ctx, pm, args...)
		install.Dir = dir
		install.Env = os.Environ()
		install.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		install.Stdout = logs
		install.Stderr = logs
		lastErr = install.Run()
		cancel()
		if lastErr == nil {
			return nil
		}
		fmt.Fprintf(logs, "%s install failed (%v)\n", pm, lastErr)
	}
	return lastErr
}

// hasViteDep reports whether package.json lists vite in its dependencies.
func hasViteDep(pkgPath string) bool {
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return false
	}
	var pkg struct {
		Dependencies    map[string]json.RawMessage `json:"dependencies"`
		DevDependencies map[string]json.RawMessage `json:"devDependencies"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return false
	}
	_, inDeps := pkg.Dependencies["vite"]
	_, inDev := pkg.DevDependencies["vite"]
	return inDeps || inDev
}
