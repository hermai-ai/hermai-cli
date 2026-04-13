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

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func buildProbeOpts(proxyURL string, stealth, insecure bool, timeout time.Duration) probe.Options {
	return probe.Options{
		ProxyURL: proxyURL,
		Stealth:  stealth,
		Insecure: insecure,
		Timeout:  timeout,
	}
}
