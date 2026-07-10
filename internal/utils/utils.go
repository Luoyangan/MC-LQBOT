// Package utils provides shared utility functions for the framework.
package utils

import (
	"os"
	"os/signal"
	"syscall"
)

// WaitForSignal blocks until SIGINT or SIGTERM is received.
// Returns the signal that caused the exit.
func WaitForSignal() os.Signal {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	return <-sigCh
}
