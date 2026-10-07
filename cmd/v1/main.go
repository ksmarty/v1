// v1 is a self-hosted AI web-app builder. It runs as a single binary that
// serves the web UI, the HTTP API, project previews and terminals.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"v1/internal/config"
	"v1/internal/harness"
	"v1/internal/server"
	"v1/internal/store"
)

// version and commit are overridden at build time via -ldflags
// "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "dev"
)

// init falls back to the VCS revision stamped into the binary by the Go
// toolchain (present when building from a git checkout), so plain `go build`
// and `go run` still report a useful commit.
func init() {
	if commit != "dev" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				commit = s.Value[:7]
				break
			}
		}
	}
}

func main() {
	cfg := config.Load(version, commit)

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	srv := server.New(cfg, st)
	httpSrv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: srv.Handler(),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The pi-durable sidecar is the default chat harness. It starts before the
	// server accepts traffic so a sidecar that cannot start, handshake or agree
	// on the protocol fails startup loudly instead of failing the user's first
	// chat turn.
	sup, err := startHarness(ctx, cfg)
	if err != nil {
		st.Close()
		// A silent fall back to the Go loop would hide the fact that the
		// requested harness is not running, so this is fatal with a fix.
		log.Fatalf("%v\n\nThe pi-durable sidecar is the default chat harness. Install its\ndependencies with `make sidecar-deps`, or set V1_HARNESS=go to run the\nbuilt-in agent loop instead.", err)
	}
	if sup != nil {
		// The bridge routes the sidecar's host-tool calls back into v1's tools
		// and turns its agent events into the chat client's SSE events.
		srv.SetHarness(harness.NewBridge(sup, log.Printf))
	}

	go func() {
		log.Printf("v1 %s (%s) listening on :%d (data dir: %s)", version, commit, cfg.Port, cfg.DataDir)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")

	// Stop previews and terminals first, then drain HTTP, then close the DB.
	srv.Shutdown()
	shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	// The sidecar holds the durable transcript open, so it stops before the
	// store closes.
	if sup != nil {
		if err := sup.Close(); err != nil {
			log.Printf("harness shutdown: %v", err)
		}
	}
	if err := st.Close(); err != nil {
		log.Printf("store close: %v", err)
	}
}

// startHarness launches the pi-durable sidecar when the pi harness is selected
// (the default) and returns
// nil otherwise, so the default configuration keeps running the Go agent loop.
func startHarness(ctx context.Context, cfg config.Config) (*harness.Supervisor, error) {
	if !cfg.HarnessEnabled() {
		log.Printf("harness: built-in Go agent loop (V1_HARNESS=go); the pi-durable sidecar is the default")
		return nil, nil
	}
	sup := harness.New(harness.Options{
		Command: cfg.SidecarCmd,
		Script:  sidecarScript(cfg),
		Socket:  cfg.SidecarSocket,
		DBPath:  cfg.HarnessDB,
		// The agent extensions v1 ships and the ones a user writes both live
		// under the data volume, so the sidecar is told where to load them.
		ExtensionsDir: filepath.Join(cfg.DataDir, "extensions"),
		ClientVersion: fmt.Sprintf("v1/%s", version),
		MaxRestarts:   cfg.MaxSidecarRestarts,
		Logf:          log.Printf,
	})
	if err := sup.Start(ctx); err != nil {
		return nil, err
	}
	return sup, nil
}

// sidecarScript resolves the sidecar entrypoint: an explicit
// V1_SIDECAR_SCRIPT wins, then the copy shipped next to the v1 binary, then
// the repo checkout (for `make dev-backend`). The sidecar is plain ESM, so
// `src/host.js` is the real entrypoint; `dist/host.js` is preferred when a
// bundled copy exists.
func sidecarScript(cfg config.Config) string {
	if cfg.SidecarScript != "" {
		return cfg.SidecarScript
	}
	roots := []string{"sidecar"}
	if exe, err := os.Executable(); err == nil {
		roots = append([]string{filepath.Join(filepath.Dir(exe), "sidecar")}, roots...)
	}
	for _, root := range roots {
		for _, name := range []string{"dist/host.js", "src/host.js"} {
			candidate := filepath.Join(root, filepath.FromSlash(name))
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	return filepath.Join("sidecar", "src", "host.js")
}
