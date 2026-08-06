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
	"runtime/debug"
	"syscall"
	"time"

	"v1/internal/config"
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
	if err := st.Close(); err != nil {
		log.Printf("store close: %v", err)
	}
}
