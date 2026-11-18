package main

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const serverShutdownTimeout = 5 * time.Minute

func serveUntilCanceled(ctx context.Context, srv *http.Server) error {
	return serveWithUntilCanceled(ctx, srv, srv.ListenAndServe)
}

// serveWithUntilCanceled is serveUntilCanceled for a server that is started by
// run (for example on a TLS listener) instead of by ListenAndServe.
func serveWithUntilCanceled(ctx context.Context, srv *http.Server, run func() error) error {
	done := make(chan error, 1)
	go func() { done <- run() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		err := srv.Shutdown(shutdown)
		if err != nil {
			_ = srv.Close()
		}
		serveErr := <-done
		if err != nil {
			return err
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	}
}
