package warden

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/studiowebux/dockward/internal/hub"
)

const shutdownTimeout = 5 * time.Second

// Server wires together the store, SSE hub, heartbeat poller, and HTTP server.
type Server struct {
	cfg       *WardenConfig
	store     *Store
	hub       *hub.Hub
	heartbeat *Heartbeat
	server    *http.Server
}

// NewServer creates a fully wired warden Server from cfg.
// If cfg.API.StatePath is set, the ring buffer is loaded from disk immediately.
func NewServer(cfg *WardenConfig) *Server {
	store := NewStore(cfg.Agents)
	store.LoadState(cfg.API.StatePath)
	h := hub.NewHub()
	hb := NewHeartbeat(store, h, cfg.Agents)

	mux := http.NewServeMux()
	s := &Server{
		cfg:       cfg,
		store:     store,
		hub:       h,
		heartbeat: hb,
		server: &http.Server{
			Addr:    fmt.Sprintf(":%s", cfg.API.Port),
			Handler: mux,
		},
	}

	mux.HandleFunc("/ingest", s.handleIngest)
	mux.HandleFunc("/events", s.handleSSE)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/", s.handleUI)

	return s
}

// Run starts the heartbeat goroutine and HTTP server.
// Blocks until ctx is cancelled, then performs a graceful shutdown.
func (s *Server) Run(ctx context.Context) {
	go s.heartbeat.Run(ctx)

	go func() {
		log.Printf("warden: listening on %s", s.server.Addr)
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("warden: server error: %v", err)
		}
	}()

	<-ctx.Done()

	s.store.SaveState(s.cfg.API.StatePath)

	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := s.server.Shutdown(shutCtx); err != nil {
		log.Printf("warden: shutdown error: %v", err)
	}
	log.Printf("warden: stopped")
}

// handleHealth returns 200 OK — used when a warden itself is monitored upstream.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}
