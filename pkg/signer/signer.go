// Package signer runs per-request signing code supplied by a schema. Some
// sites (X, TikTok, Xiaohongshu) require a dynamically-computed header or
// query parameter on every authenticated request — the value is produced by
// JavaScript that lives inside the page. Rather than compile a native
// implementation per site into the CLI, Hermai ships the signing function
// inside the schema itself and executes it in a sandboxed JS runtime on the
// client. Adding or fixing a site becomes a registry push, not a CLI
// release.
//
// The sandbox is intentionally minimal: no file system, no network, no
// require/import, no timers beyond a fixed wall-clock deadline. Signers get
// the request details as input and return headers/URL augmentations as
// output. Everything the signer needs from crypto/base64/hex land is
// exposed via the injected `hermai` global — see jssigner.go.
package signer

import (
	"context"
	"errors"
	"time"
)

// Input is the request information handed to a signer. It is the complete
// set of inputs a signer is allowed to see — no implicit globals, no
// environment access. Anything a signer needs that isn't here is a bug in
// the signer or a gap in this contract.
type Input struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	// Cookies are the name->value pairs scoped to the target host. The
	// signer uses this instead of a document.cookie string so we never
	// hand the signer cookies for other domains.
	Cookies map[string]string `json:"cookies"`
	// State carries schema-specific key/value data produced by a prior
	// bootstrap step — for example, X's pre-computed `animation_key`,
	// or TikTok's `msToken` if the site's signer needs it in-hand. State
	// is opaque to the CLI core; each schema defines what it puts here.
	// Persisted alongside cookies in the session directory.
	State map[string]string `json:"state"`
	// NowMS is the current wall-clock time in milliseconds. Injected
	// rather than read from Date.now() so tests can replay captured
	// vectors deterministically.
	NowMS int64 `json:"now_ms"`
}

// Output is what a signer returns. URL may be the input URL with extra
// query parameters appended (TikTok's X-Bogus, _signature). Headers are
// merged onto the outgoing request, overriding any existing values with
// the same name.
type Output struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// Signer is the single operation every signing implementation exposes.
// Implementations must be safe for concurrent use — the CLI will hold one
// Signer per site and call it from many goroutines when batch operations
// arrive.
type Signer interface {
	Sign(ctx context.Context, in Input) (*Output, error)
}

// ErrSignerTimeout is returned when a signer exceeds its deadline. It is
// treated as a fatal schema bug — either the signer is pathological or the
// site changed the algorithm. The CLI surfaces this clearly so contributors
// know to recapture.
var ErrSignerTimeout = errors.New("signer: execution exceeded deadline")

// ErrSignerRuntime is returned when the JS runtime throws or the signer
// produces an output we can't parse. Wraps the underlying cause.
var ErrSignerRuntime = errors.New("signer: runtime error")

// DefaultTimeout is the deadline applied when the caller does not set one
// via context. Real signers complete in <5ms on a laptop; 100ms is
// generous enough to absorb cold-start compile time while still failing
// fast on infinite loops.
const DefaultTimeout = 100 * time.Millisecond
