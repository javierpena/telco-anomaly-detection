package alertreceiver

import (
	"context"
	"fmt"
	"net/http"
	"time"

	uberzap "go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Server wraps the HTTP server for the alert receiver webhook.
type Server struct {
	BindAddress string
	Handler     *Handler
	httpServer  *http.Server
}

// NewServer creates a Server listening on bindAddress.
func NewServer(bindAddress string, hubClient client.Client, logLevel *uberzap.AtomicLevel) *Server {
	h := NewHandler(hubClient, logLevel)
	return &Server{
		BindAddress: bindAddress,
		Handler:     h,
	}
}

// Start registers routes and begins serving HTTP requests. It blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	logger := log.FromContext(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", s.Handler.HandleWebhook)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	s.httpServer = &http.Server{
		Addr:         s.BindAddress,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	logger.Info("starting alert receiver", "address", s.BindAddress)

	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down alert receiver")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		return fmt.Errorf("alert receiver server error: %w", err)
	}
}
