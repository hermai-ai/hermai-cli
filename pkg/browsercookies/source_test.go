package browsercookies

import (
	"net/http"
	"testing"
	"time"

	"github.com/browserutils/kooky"
)

func TestNormalizeDomain(t *testing.T) {
	cases := []struct {
		in   string
		want string
		err  bool
	}{
		{"x.com", "x.com", false},
		{"X.COM", "x.com", false},
		{"https://x.com/", "x.com", false},
		{"https://x.com/foo/bar", "x.com", false},
		{"www.x.com", "x.com", false},
		{"www.X.com/", "x.com", false},
		{"http://api.example.com/v1", "api.example.com", false},
		{"  x.com  ", "x.com", false},

		// Errors
		{"", "", true},
		{"   ", "", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := NormalizeDomain(c.in)
			if (err != nil) != c.err {
				t.Fatalf("err = %v, wantErr %v", err, c.err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestCookieMatchesDomain(t *testing.T) {
	cases := []struct {
		cookie, target string
		want           bool
	}{
		// Exact matches
		{"x.com", "x.com", true},
		{".x.com", "x.com", true},
		{"X.COM", "x.com", true},

		// Cookie for parent domain — sent to subdomains
		{"x.com", "api.x.com", true},
		{".x.com", "api.x.com", true},
		{"example.com", "deeply.nested.example.com", true},

		// Cookie for subdomain — NOT sent to parent
		{"api.x.com", "x.com", false},
		{".api.x.com", "x.com", false},

		// Unrelated domains
		{"x.com", "y.com", false},
		{"x.com", "example.com", false},
		{"notx.com", "x.com", false}, // suffix-like but not a real subdomain
	}
	for _, c := range cases {
		got := cookieMatchesDomain(c.cookie, c.target)
		if got != c.want {
			t.Errorf("cookieMatchesDomain(%q, %q) = %v, want %v", c.cookie, c.target, got, c.want)
		}
	}
}

func TestDedupeAndConvert_PicksFreshestDuplicate(t *testing.T) {
	// Same cookie name in two browsers. Hermai should return the one with
	// the most recent Expires / Creation timestamp.
	old := &kooky.Cookie{
		Cookie:   http.Cookie{Name: "auth_token", Value: "stale-value", Domain: ".x.com", Path: "/"},
		Creation: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	fresh := &kooky.Cookie{
		Cookie:   http.Cookie{Name: "auth_token", Value: "fresh-value", Domain: ".x.com", Path: "/"},
		Creation: time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
	}

	s := &Source{}
	got := s.dedupeAndConvert([]*kooky.Cookie{old, fresh}, "x.com")
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Value != "fresh-value" {
		t.Errorf("got value %q, want fresh-value (dedupe should keep the newest)", got[0].Value)
	}
}

func TestDedupeAndConvert_DropsOutOfScopeCookies(t *testing.T) {
	// A cookie for a different site must never leak into the result even if
	// the upstream filter failed to drop it.
	onDomain := &kooky.Cookie{
		Cookie: http.Cookie{Name: "session", Value: "ok", Domain: ".x.com", Path: "/"},
	}
	offDomain := &kooky.Cookie{
		Cookie: http.Cookie{Name: "session", Value: "leak", Domain: ".y.com", Path: "/"},
	}

	s := &Source{}
	got := s.dedupeAndConvert([]*kooky.Cookie{onDomain, offDomain}, "x.com")
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (off-domain cookie must be dropped)", len(got))
	}
	if got[0].Value != "ok" {
		t.Errorf("got value %q, want 'ok'", got[0].Value)
	}
}

func TestDedupeAndConvert_NilSafe(t *testing.T) {
	s := &Source{}
	got := s.dedupeAndConvert([]*kooky.Cookie{nil, nil}, "x.com")
	if len(got) != 0 {
		t.Errorf("got %d, want 0", len(got))
	}
}

