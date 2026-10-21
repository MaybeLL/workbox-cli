package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/MaybeLL/workbox-cli/internal/workbox"
)

// Set by the release build; go build remains usable from a source checkout.
var version = "0.1.2"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(workbox.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, version))
}
