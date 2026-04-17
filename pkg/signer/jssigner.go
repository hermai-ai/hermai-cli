package signer

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// JSSigner runs a schema-supplied JavaScript function inside a sandboxed
// goja runtime. The JS source must define a global function
// `sign(input)` that takes the Input shape (as a plain object) and
// returns an object matching Output.
//
// The source is compiled exactly once at construction time. Each Sign
// call creates a fresh goja.Runtime so signer executions never leak
// state between requests — this matters when signers use let-bindings
// at module scope or store computed prefixes. Goja runtime creation is
// ~100µs; the amortized cost over a network request is negligible.
type JSSigner struct {
	program *goja.Program
	source  string // kept for error messages only
	timeout time.Duration
}

// NewJSSigner compiles the signer source. Returns an error if the source
// fails to parse. The function does NOT execute the top-level of the
// source — that happens on each Sign call so module-scoped state never
// persists across requests.
func NewJSSigner(source string) (*JSSigner, error) {
	if source == "" {
		return nil, errors.New("signer: source is empty")
	}
	prog, err := goja.Compile("signer.js", source, true)
	if err != nil {
		return nil, fmt.Errorf("signer: compile: %w", err)
	}
	return &JSSigner{
		program: prog,
		source:  source,
		timeout: DefaultTimeout,
	}, nil
}

// WithTimeout overrides the default per-call deadline. Useful for
// signers that do unusually expensive crypto (rare — most are <5ms).
func (s *JSSigner) WithTimeout(d time.Duration) *JSSigner {
	s.timeout = d
	return s
}

// Sign runs the signer against the given input. The runtime is
// interrupted either when the provided context is canceled or when the
// signer's own timeout fires, whichever comes first.
func (s *JSSigner) Sign(ctx context.Context, in Input) (*Output, error) {
	rt := goja.New()

	// Enforce a hard deadline. Goja's Interrupt stops the running script
	// at the next bytecode boundary and causes the Runtime.RunProgram
	// call to return an InterruptedError. This is the only reliable way
	// to stop an infinite loop in user-supplied JS.
	deadline := s.timeout
	if d, ok := ctx.Deadline(); ok {
		if rem := time.Until(d); rem > 0 && rem < deadline {
			deadline = rem
		}
	}
	timer := time.AfterFunc(deadline, func() {
		rt.Interrupt(ErrSignerTimeout)
	})
	defer timer.Stop()

	// Cancel via context too — caller may abort before the timeout fires
	// (e.g. user hit Ctrl-C on the CLI).
	stopCtxWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			rt.Interrupt(ctx.Err())
		case <-stopCtxWatch:
		}
	}()
	defer close(stopCtxWatch)

	injectHermaiGlobal(rt)

	// Execute the source. This defines `sign` (and any helpers) on the
	// runtime. We discard the program's return value.
	if _, err := rt.RunProgram(s.program); err != nil {
		return nil, wrapRuntimeError(err)
	}

	signFn, ok := goja.AssertFunction(rt.Get("sign"))
	if !ok {
		return nil, fmt.Errorf("%w: signer source did not define a global `sign` function", ErrSignerRuntime)
	}

	inputVal := rt.ToValue(map[string]any{
		"method":  in.Method,
		"url":     in.URL,
		"headers": in.Headers,
		"body":    in.Body,
		"cookies": in.Cookies,
		"state":   in.State,
		"now_ms":  in.NowMS,
	})

	result, err := signFn(goja.Undefined(), inputVal)
	if err != nil {
		return nil, wrapRuntimeError(err)
	}

	return parseOutput(result)
}

// wrapRuntimeError converts goja's various error shapes into our
// sentinel errors. Interrupts produced by the deadline timer carry the
// original ErrSignerTimeout as their value; we surface that directly so
// callers can distinguish "signer too slow" from "signer threw."
func wrapRuntimeError(err error) error {
	if err == nil {
		return nil
	}
	var interrupt *goja.InterruptedError
	if errors.As(err, &interrupt) {
		if interrupt.Value() == ErrSignerTimeout {
			return ErrSignerTimeout
		}
		if ctxErr, ok := interrupt.Value().(error); ok {
			return ctxErr
		}
	}
	return fmt.Errorf("%w: %v", ErrSignerRuntime, err)
}

// parseOutput turns the JS return value into a typed Output. Signers may
// omit either field — an empty URL means "don't change the request URL"
// and a nil Headers means "no headers to merge."
func parseOutput(v goja.Value) (*Output, error) {
	if v == nil || goja.IsNull(v) || goja.IsUndefined(v) {
		return &Output{}, nil
	}
	obj := v.ToObject(nil)
	if obj == nil {
		return nil, fmt.Errorf("%w: sign() returned a non-object", ErrSignerRuntime)
	}
	out := &Output{Headers: map[string]string{}}
	if urlVal := obj.Get("url"); urlVal != nil && !goja.IsUndefined(urlVal) && !goja.IsNull(urlVal) {
		out.URL = urlVal.String()
	}
	if hdrs := obj.Get("headers"); hdrs != nil && !goja.IsUndefined(hdrs) && !goja.IsNull(hdrs) {
		hobj := hdrs.ToObject(nil)
		if hobj == nil {
			return nil, fmt.Errorf("%w: sign().headers is not an object", ErrSignerRuntime)
		}
		for _, k := range hobj.Keys() {
			out.Headers[k] = hobj.Get(k).String()
		}
	}
	return out, nil
}

// injectHermaiGlobal installs the `hermai` object the signer uses for
// crypto, encoding, and entropy. The JS sandbox intentionally omits
// browser globals like `crypto.subtle`, `TextEncoder`, `btoa`, etc. —
// signer authors write against hermai.* and we keep the surface area
// small enough to audit at a glance.
func injectHermaiGlobal(rt *goja.Runtime) {
	h := rt.NewObject()

	_ = h.Set("sha256", func(input string) string {
		sum := sha256.Sum256([]byte(input))
		return hex.EncodeToString(sum[:])
	})

	_ = h.Set("sha1", func(input string) string {
		sum := sha1.Sum([]byte(input))
		return hex.EncodeToString(sum[:])
	})

	_ = h.Set("hmacSha256", func(key, message string) string {
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(message))
		return hex.EncodeToString(mac.Sum(nil))
	})

	_ = h.Set("base64Encode", func(input string) string {
		return base64.StdEncoding.EncodeToString([]byte(input))
	})

	_ = h.Set("base64EncodeURL", func(input string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(input))
	})

	_ = h.Set("base64Decode", func(input string) (string, error) {
		b, err := base64.StdEncoding.DecodeString(input)
		if err != nil {
			// Try URL-safe as a fallback — signers often capture from
			// browser code that normalized to URL-safe encoding.
			b, err = base64.RawURLEncoding.DecodeString(input)
			if err != nil {
				return "", err
			}
		}
		return string(b), nil
	})

	_ = h.Set("hex", func(input string) string {
		return hex.EncodeToString([]byte(input))
	})

	_ = h.Set("hexDecode", func(input string) (string, error) {
		b, err := hex.DecodeString(input)
		if err != nil {
			return "", err
		}
		return string(b), nil
	})

	_ = h.Set("randomHex", func(n int) (string, error) {
		if n <= 0 || n > 1024 {
			return "", fmt.Errorf("randomHex: invalid length %d", n)
		}
		b := make([]byte, n)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		return hex.EncodeToString(b), nil
	})

	_ = rt.Set("hermai", h)
}

// compileCache is a process-wide cache of compiled signer programs,
// keyed by source. Different callers loading the same signer source
// (the common case — every request to x.com uses the same x.com
// signer) share a single goja.Program rather than recompiling.
var compileCache sync.Map // map[string]*goja.Program

// CachedJSSigner is like NewJSSigner but reuses a process-wide cached
// compiled program if the same source was seen before. Intended for the
// per-request dispatch path in the `hermai action` command, where the
// same schema's signer is invoked many times in succession.
func CachedJSSigner(source string) (*JSSigner, error) {
	if source == "" {
		return nil, errors.New("signer: source is empty")
	}
	if prog, ok := compileCache.Load(source); ok {
		return &JSSigner{
			program: prog.(*goja.Program),
			source:  source,
			timeout: DefaultTimeout,
		}, nil
	}
	prog, err := goja.Compile("signer.js", source, true)
	if err != nil {
		return nil, fmt.Errorf("signer: compile: %w", err)
	}
	compileCache.Store(source, prog)
	return &JSSigner{
		program: prog,
		source:  source,
		timeout: DefaultTimeout,
	}, nil
}
