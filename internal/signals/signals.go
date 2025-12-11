package signals

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

// SetupSignalHandler creates a context that is canceled when either SIGTERM or SIGINT is received
func SetupSignalHandler(shutdownTimeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		sig := <-sigChan
		log.Info("Received shutdown signal", "signal", sig)
		cancel()

		// If we receive another signal, force immediate shutdown
		sig = <-sigChan
		log.Warn("Received second shutdown signal, forcing immediate shutdown", "signal", sig)
		os.Exit(1)
	}()

	return ctx, cancel
}

// WaitForShutdown blocks until the context is canceled and handles graceful shutdown
func WaitForShutdown(ctx context.Context, shutdownTimeout time.Duration, cleanup func() error) {
	<-ctx.Done()
	log.Info("Starting graceful shutdown", "timeout", shutdownTimeout)

	// Create a timeout context for cleanup
	timeoutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// Create a channel to signal cleanup completion
	done := make(chan struct{})
	var err error

	go func() {
		err = cleanup()
		close(done)
	}()

	// Wait for either cleanup to complete or timeout
	select {
	case <-done:
		if err != nil {
			log.Error("Error during cleanup", "error", err)
			os.Exit(1)
		}
		log.Info("Graceful shutdown completed")
	case <-timeoutCtx.Done():
		log.Error("Shutdown timed out, forcing exit")
		os.Exit(1)
	}
}
