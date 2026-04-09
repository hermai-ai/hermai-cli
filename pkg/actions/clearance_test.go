package actions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClearanceResult_CookieHeader(t *testing.T) {
	tests := []struct {
		name    string
		cr      *ClearanceResult
		wantLen int // number of "=" in output (one per cookie)
	}{
		{name: "nil result", cr: nil, wantLen: 0},
		{name: "empty cookies", cr: &ClearanceResult{Cookies: map[string]string{}}, wantLen: 0},
		{name: "one cookie", cr: &ClearanceResult{Cookies: map[string]string{"a": "1"}}, wantLen: 1},
		{name: "two cookies", cr: &ClearanceResult{Cookies: map[string]string{"a": "1", "b": "2"}}, wantLen: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := tt.cr.cookieHeader()
			if tt.wantLen == 0 && header != "" {
				t.Errorf("expected empty header, got %q", header)
			}
			if tt.wantLen > 0 && header == "" {
				t.Errorf("expected non-empty header")
			}
		})
	}
}

func TestMergeClearanceCookies(t *testing.T) {
	base := HTTPOptions{
		ProxyURL: "http://proxy:8080",
		HeaderOverrides: map[string]string{
			"X-Custom": "value",
		},
	}

	t.Run("nil clearance leaves opts unchanged", func(t *testing.T) {
		merged := mergeClearanceCookies(base, nil)
		if merged.HeaderOverrides["Cookie"] != "" {
			t.Errorf("expected no Cookie header, got %q", merged.HeaderOverrides["Cookie"])
		}
		if merged.HeaderOverrides["X-Custom"] != "value" {
			t.Error("lost existing header override")
		}
	})

	t.Run("adds clearance cookies", func(t *testing.T) {
		cr := &ClearanceResult{Cookies: map[string]string{"sess": "abc123"}}
		merged := mergeClearanceCookies(base, cr)
		if merged.HeaderOverrides["Cookie"] != "sess=abc123" {
			t.Errorf("expected Cookie header 'sess=abc123', got %q", merged.HeaderOverrides["Cookie"])
		}
		if merged.ProxyURL != base.ProxyURL {
			t.Error("ProxyURL should be preserved")
		}
	})

	t.Run("appends to existing cookie header", func(t *testing.T) {
		optsWithCookie := HTTPOptions{
			HeaderOverrides: map[string]string{
				"Cookie": "existing=val",
			},
		}
		cr := &ClearanceResult{Cookies: map[string]string{"new": "val2"}}
		merged := mergeClearanceCookies(optsWithCookie, cr)
		cookie := merged.HeaderOverrides["Cookie"]
		if cookie != "existing=val; new=val2" {
			t.Errorf("expected merged cookie header, got %q", cookie)
		}
	})

	t.Run("does not mutate original opts", func(t *testing.T) {
		cr := &ClearanceResult{Cookies: map[string]string{"x": "y"}}
		_ = mergeClearanceCookies(base, cr)
		if base.HeaderOverrides["Cookie"] != "" {
			t.Error("original opts mutated")
		}
	})
}

func TestHTTPBootstrap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session_id", Value: "test123"})
		http.SetCookie(w, &http.Cookie{Name: "clearance", Value: "abc"})
		w.WriteHeader(200)
	}))
	defer server.Close()

	cookies := httpBootstrap(context.Background(), server.URL, HTTPOptions{})
	if len(cookies) < 2 {
		t.Fatalf("expected at least 2 cookies, got %d", len(cookies))
	}
	if cookies["session_id"] != "test123" {
		t.Errorf("expected session_id=test123, got %q", cookies["session_id"])
	}
	if cookies["clearance"] != "abc" {
		t.Errorf("expected clearance=abc, got %q", cookies["clearance"])
	}
}

func TestDomainFromTargetURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://www.ebay.com/sch/i.html?_nkw=ipad", "ebay.com"},
		{"https://www.walmart.com/search?q=ipad", "walmart.com"},
		{"https://developer.mozilla.org/en-US/search?q=fetch", "developer.mozilla.org"},
		{"https://amazon.com/s?k=ipad", "amazon.com"},
		{"invalid-url", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := domainFromTargetURL(tt.input)
			if got != tt.want {
				t.Errorf("domainFromTargetURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
