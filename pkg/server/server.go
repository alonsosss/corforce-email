package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alonsosss/corforce-email/pkg/observability"
	"go.uber.org/zap"
)

type Server struct {
	httpServer *http.Server
	logger     *zap.Logger
}

func New(port int, handler http.Handler, logger *zap.Logger) *Server {
	return &Server{
		httpServer: &http.Server{
			Addr:              fmt.Sprintf(":%d", port),
			Handler:           observability.WithOps(handler),
			ReadTimeout:       15 * time.Second,
			ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    1 << 20,
		},
		logger: logger,
	}
}

func (s *Server) Run() error {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("server starting", zap.String("addr", s.httpServer.Addr))
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("server failed: %w", err)
	case sig := <-quit:
		s.logger.Info("shutdown signal received", zap.String("signal", sig.String()))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("server shutdown failed: %w", err)
	}

	s.logger.Info("server stopped gracefully")
	return nil
}
