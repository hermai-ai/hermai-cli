package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/probe"
)

func parseTimeout(flag string, fallback time.Duration) (time.Duration, error) {
	if flag == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(flag)
	if err != nil {
		return 0, fmt.Errorf("invalid --timeout: %w", err)
	}
	return d, nil
}

// signalContext returns a context cancelled on SIGINT/SIGTERM with an
// overall deadline. The deadline enforces the advertised --timeout as a
// wall-clock cap on the entire command, not just individual requests.
func signalContext(deadline time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, timeoutCancel := context.WithTimeout(ctx, deadline)
	combined := func() { timeoutCancel(); cancel() }
	return ctx, combined
}

func buildProbeOpts(proxyURL string, stealth, insecure bool, perRequestTimeout time.Duration) probe.Options {
	return probe.Options{
		ProxyURL: proxyURL,
		Stealth:  stealth,
		Insecure: insecure,
		Timeout:  perRequestTimeout,
	}
}
