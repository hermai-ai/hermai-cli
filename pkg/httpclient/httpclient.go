// Package httpclient exposes the Chrome-TLS-fingerprinted HTTP client
// used throughout hermai-cli for anti-bot-friendly fetches.
//
// This package is a thin re-export of internal/httpclient so external
// modules (hermai-api's hosted-fetch service, future SDK consumers)
// can import it — internal/ is still the canonical home, to keep the
// in-repo import graph unchanged. If the stealth client ever needs
// breaking changes, the in-repo callers move first and this shim
// follows.
package httpclient

import (
	internal "github.com/hermai-ai/hermai-cli/internal/httpclient"
)

// Doer is the minimal interface hermai code uses for HTTP.
type Doer = internal.Doer

// Options configures a client. See internal/httpclient for field docs.
type Options = internal.Options

// New returns a plain net/http client configured with the given
// options. Use this when you don't need TLS-fingerprinting.
func New(opts Options) Doer {
	return internal.New(opts)
}

// NewStealth returns a Chrome-TLS-fingerprinted client via
// bogdanfinn/tls-client. Use this against Cloudflare, Akamai, DataDome,
// and PerimeterX-protected hosts that rustle the JA3/JA4 of plain Go.
func NewStealth(opts Options) (Doer, error) {
	return internal.NewStealth(opts)
}

// MustNewStealth panics on construction failure. Only for tests and
// startup code where a fail is fatal.
func MustNewStealth(opts Options) Doer {
	return internal.MustNewStealth(opts)
}

// NewStealthOrFallback returns a stealth client, falling back to the
// plain client on construction failure. Useful when the stealth
// fingerprint library might not be available on the target platform.
func NewStealthOrFallback(opts Options) Doer {
	return internal.NewStealthOrFallback(opts)
}

// NewStealthWithRedirects wraps a stealth client with a redirect
// follower that preserves the TLS fingerprint across the redirect
// chain. Stock net/http redirects would otherwise re-establish the
// connection with the default Go fingerprint on the hop.
func NewStealthWithRedirects(opts Options, maxRedirects int) (Doer, error) {
	return internal.NewStealthWithRedirects(opts, maxRedirects)
}
