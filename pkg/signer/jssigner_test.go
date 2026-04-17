package signer

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJSSigner_NominalSign(t *testing.T) {
	src := `
		function sign(input) {
			return { url: input.url, headers: { "x-hello": "world" } };
		}
	`
	s, err := NewJSSigner(src)
	if err != nil {
		t.Fatalf("NewJSSigner: %v", err)
	}
	out, err := s.Sign(context.Background(), Input{
		Method: "GET",
		URL:    "https://example.com/api",
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if out.URL != "https://example.com/api" {
		t.Errorf("URL = %q, want unchanged", out.URL)
	}
	if out.Headers["x-hello"] != "world" {
		t.Errorf("headers = %v, want x-hello: world", out.Headers)
	}
}

func TestJSSigner_InputPassThrough(t *testing.T) {
	// Round-trip every Input field through the signer to confirm the
	// bridge from Go → JS covers the full contract.
	src := `
		function sign(input) {
			return {
				headers: {
					"seen-method": input.method,
					"seen-url": input.url,
					"seen-body": input.body,
					"seen-header-x": input.headers["x-existing"],
					"seen-cookie-auth": input.cookies["auth_token"],
					"seen-now": String(input.now_ms)
				}
			};
		}
	`
	s, _ := NewJSSigner(src)
	out, err := s.Sign(context.Background(), Input{
		Method:  "POST",
		URL:     "https://x.com/graphql/CreateTweet",
		Headers: map[string]string{"x-existing": "v"},
		Body:    `{"text":"hi"}`,
		Cookies: map[string]string{"auth_token": "abc123"},
		NowMS:   1700000000000,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	want := map[string]string{
		"seen-method":      "POST",
		"seen-url":         "https://x.com/graphql/CreateTweet",
		"seen-body":        `{"text":"hi"}`,
		"seen-header-x":    "v",
		"seen-cookie-auth": "abc123",
		"seen-now":         "1700000000000",
	}
	for k, v := range want {
		if out.Headers[k] != v {
			t.Errorf("header %q = %q, want %q", k, out.Headers[k], v)
		}
	}
}

func TestJSSigner_SHA256MatchesStdlib(t *testing.T) {
	src := `
		function sign(input) {
			return { headers: { "x-digest": hermai.sha256(input.body) } };
		}
	`
	s, _ := NewJSSigner(src)
	body := "the quick brown fox"
	out, err := s.Sign(context.Background(), Input{Body: body})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	sum := sha256.Sum256([]byte(body))
	want := hex.EncodeToString(sum[:])
	if out.Headers["x-digest"] != want {
		t.Errorf("digest = %q, want %q", out.Headers["x-digest"], want)
	}
}

func TestJSSigner_HMACMatchesStdlib(t *testing.T) {
	src := `
		function sign(input) {
			return { headers: { "x-sig": hermai.hmacSha256("secret-key", input.body) } };
		}
	`
	s, _ := NewJSSigner(src)
	body := "payload to sign"
	out, err := s.Sign(context.Background(), Input{Body: body})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	mac := hmac.New(sha256.New, []byte("secret-key"))
	mac.Write([]byte(body))
	want := hex.EncodeToString(mac.Sum(nil))
	if out.Headers["x-sig"] != want {
		t.Errorf("hmac = %q, want %q", out.Headers["x-sig"], want)
	}
}

func TestJSSigner_Timeout(t *testing.T) {
	src := `function sign(input) { while (true) {} }`
	s, _ := NewJSSigner(src)
	s = s.WithTimeout(50 * time.Millisecond)
	start := time.Now()
	_, err := s.Sign(context.Background(), Input{})
	elapsed := time.Since(start)
	if !errors.Is(err, ErrSignerTimeout) {
		t.Fatalf("err = %v, want ErrSignerTimeout", err)
	}
	if elapsed > 250*time.Millisecond {
		t.Errorf("took %v, want under 250ms (interrupt did not fire promptly)", elapsed)
	}
}

func TestJSSigner_ContextCancel(t *testing.T) {
	src := `function sign(input) { while (true) {} }`
	s, _ := NewJSSigner(src)
	s = s.WithTimeout(10 * time.Second) // make sure context wins, not the timer
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := s.Sign(ctx, Input{})
	if err == nil {
		t.Fatal("expected error from cancellation, got nil")
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Errorf("took too long to respond to context cancel: %v", time.Since(start))
	}
}

func TestJSSigner_RuntimeError(t *testing.T) {
	src := `function sign(input) { throw new Error("nope"); }`
	s, _ := NewJSSigner(src)
	_, err := s.Sign(context.Background(), Input{})
	if !errors.Is(err, ErrSignerRuntime) {
		t.Fatalf("err = %v, want ErrSignerRuntime", err)
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("err = %q, expected underlying JS message", err.Error())
	}
}

func TestJSSigner_MissingSignFunction(t *testing.T) {
	src := `var x = 1;` // compiles, but defines no sign
	s, _ := NewJSSigner(src)
	_, err := s.Sign(context.Background(), Input{})
	if !errors.Is(err, ErrSignerRuntime) {
		t.Fatalf("err = %v, want ErrSignerRuntime", err)
	}
	if !strings.Contains(err.Error(), "sign") {
		t.Errorf("err = %q, expected mention of missing sign", err.Error())
	}
}

func TestJSSigner_CompileError(t *testing.T) {
	_, err := NewJSSigner(`function sign(input) { this is not js`)
	if err == nil {
		t.Fatal("expected compile error, got nil")
	}
}

func TestJSSigner_EmptySource(t *testing.T) {
	_, err := NewJSSigner("")
	if err == nil {
		t.Fatal("expected error for empty source")
	}
}

func TestJSSigner_NoStateLeakBetweenCalls(t *testing.T) {
	// If a signer mutates a module-scope variable, the second call must
	// NOT see the first call's mutation — each Sign gets a fresh runtime.
	src := `
		var counter = 0;
		function sign(input) {
			counter += 1;
			return { headers: { "x-counter": String(counter) } };
		}
	`
	s, _ := NewJSSigner(src)
	for i := 0; i < 3; i++ {
		out, err := s.Sign(context.Background(), Input{})
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if out.Headers["x-counter"] != "1" {
			t.Errorf("iter %d: counter = %q, want 1 (state must not leak)", i, out.Headers["x-counter"])
		}
	}
}

func TestJSSigner_Concurrent(t *testing.T) {
	// Fan out many Sign calls; each should return its own input's digest
	// with no crosstalk.
	src := `
		function sign(input) {
			return { headers: { "x-digest": hermai.sha256(input.body) } };
		}
	`
	s, _ := NewJSSigner(src)

	const N = 64
	var wg sync.WaitGroup
	errs := make([]error, N)
	got := make([]string, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := strings.Repeat("x", i+1)
			out, err := s.Sign(context.Background(), Input{Body: body})
			if err != nil {
				errs[i] = err
				return
			}
			got[i] = out.Headers["x-digest"]
		}(i)
	}
	wg.Wait()

	for i := 0; i < N; i++ {
		if errs[i] != nil {
			t.Fatalf("iter %d: %v", i, errs[i])
		}
		sum := sha256.Sum256([]byte(strings.Repeat("x", i+1)))
		want := hex.EncodeToString(sum[:])
		if got[i] != want {
			t.Errorf("iter %d: digest mismatch, got %q want %q", i, got[i], want)
		}
	}
}

func TestJSSigner_OutputShape_OmittedFields(t *testing.T) {
	// Signers may omit url or headers. Empty output is legal (a no-op
	// signer). Parser must handle null/undefined cleanly.
	src := `function sign(input) { return {}; }`
	s, _ := NewJSSigner(src)
	out, err := s.Sign(context.Background(), Input{URL: "https://x.com"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if out.URL != "" || len(out.Headers) != 0 {
		t.Errorf("empty signer result should produce zero-value Output; got %+v", out)
	}
}

func TestJSSigner_OutputShape_NullReturn(t *testing.T) {
	src := `function sign(input) { return null; }`
	s, _ := NewJSSigner(src)
	out, err := s.Sign(context.Background(), Input{})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if out.URL != "" || len(out.Headers) != 0 {
		t.Errorf("null signer result should produce zero-value Output; got %+v", out)
	}
}

func TestCachedJSSigner_ReusesProgram(t *testing.T) {
	// Same source → two signers should share the underlying compiled
	// program. Identity check via pointer comparison.
	src := `function sign(input) { return { headers: { "x-k": "v" } }; }`
	a, err := CachedJSSigner(src)
	if err != nil {
		t.Fatalf("first CachedJSSigner: %v", err)
	}
	b, err := CachedJSSigner(src)
	if err != nil {
		t.Fatalf("second CachedJSSigner: %v", err)
	}
	if a.program != b.program {
		t.Error("expected shared goja.Program between two CachedJSSigner calls with identical source")
	}
}

func TestJSSigner_HexRoundTrip(t *testing.T) {
	src := `
		function sign(input) {
			var h = hermai.hex(input.body);
			return { headers: { "x-hex": h } };
		}
	`
	s, _ := NewJSSigner(src)
	out, err := s.Sign(context.Background(), Input{Body: "AB"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// "AB" -> "4142"
	if out.Headers["x-hex"] != "4142" {
		t.Errorf("hex = %q, want 4142", out.Headers["x-hex"])
	}
}

func TestJSSigner_Base64DecodeAllAlphabets(t *testing.T) {
	// Decode the same 3-byte value via all four alphabet/padding combos
	// and assert each succeeds. "hi?" differs across alphabets because
	// it produces "/" (standard) vs "_" (URL-safe); "fo" differs across
	// padding conventions (2 bytes → 4 chars with "=" or 3 chars without).
	src := `
		function sign(input) {
			return { headers: {
				"std-padded":  hermai.base64Decode("aGk/"),  // std alphabet, no padding needed
				"url-padded":  hermai.base64Decode("aGk_"),  // URL-safe alphabet
				"std-padded2": hermai.base64Decode("Zm8="),  // std alphabet, 2-byte with = padding
				"std-nopad":   hermai.base64Decode("Zm8")    // std alphabet, 2-byte no padding
			}};
		}
	`
	s, _ := NewJSSigner(src)
	out, err := s.Sign(context.Background(), Input{})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if got := out.Headers["std-padded"]; got != "hi?" {
		t.Errorf("std-padded = %q, want \"hi?\"", got)
	}
	if got := out.Headers["url-padded"]; got != "hi?" {
		t.Errorf("url-padded = %q, want \"hi?\"", got)
	}
	if got := out.Headers["std-padded2"]; got != "fo" {
		t.Errorf("std-padded2 = %q, want \"fo\"", got)
	}
	if got := out.Headers["std-nopad"]; got != "fo" {
		t.Errorf("std-nopad = %q, want \"fo\"", got)
	}
}

func TestJSSigner_Base64DecodeInvalidInput(t *testing.T) {
	src := `
		function sign(input) {
			try { return { headers: { "v": hermai.base64Decode("!!!not-base64!!!") } }; }
			catch (e) { return { headers: { "err": String(e) } }; }
		}
	`
	s, _ := NewJSSigner(src)
	out, err := s.Sign(context.Background(), Input{})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !strings.Contains(out.Headers["err"], "base64") {
		t.Errorf("error should mention base64, got: %q", out.Headers["err"])
	}
}

func TestJSSigner_Base64(t *testing.T) {
	src := `
		function sign(input) {
			return { headers: {
				"x-b64": hermai.base64Encode(input.body),
				"x-b64url": hermai.base64EncodeURL(input.body)
			} };
		}
	`
	s, _ := NewJSSigner(src)
	out, err := s.Sign(context.Background(), Input{Body: "hi?"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if out.Headers["x-b64"] != "aGk/" {
		t.Errorf("base64 = %q, want aGk/", out.Headers["x-b64"])
	}
	if out.Headers["x-b64url"] != "aGk_" {
		t.Errorf("base64url = %q, want aGk_", out.Headers["x-b64url"])
	}
}
