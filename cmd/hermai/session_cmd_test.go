package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hermai-ai/hermai-cli/pkg/config"
)

func TestFetchSessionCardUsesAuthenticatedBootstrapEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/schemas/shop.com/session-bootstrap" {
			t.Fatalf("path = %q, want /v1/schemas/shop.com/session-bootstrap", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer hm_sk_test" {
			t.Fatalf("Authorization = %q, want bearer platform key", got)
		}
		if got := r.Header.Get("X-Hermai-Intent"); got == "" {
			t.Fatal("X-Hermai-Intent header missing")
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"site":                       "shop.com",
				"requires_session_bootstrap": true,
				"session": map[string]any{
					"bootstrap_url":    "https://shop.com/",
					"required_cookies": []string{"sid"},
				},
			},
		}); err != nil {
			t.Fatalf("write response: %v", err)
		}
	}))
	defer server.Close()

	card, err := fetchSessionCard(config.Config{
		Platform: config.PlatformConfig{
			URL: server.URL,
			Key: "hm_sk_test",
		},
	}, "shop.com")
	if err != nil {
		t.Fatalf("fetchSessionCard: %v", err)
	}
	if card.Site != "shop.com" {
		t.Fatalf("site = %q, want shop.com", card.Site)
	}
	if card.Session.BootstrapURL != "https://shop.com/" {
		t.Fatalf("bootstrap_url = %q", card.Session.BootstrapURL)
	}
	if got := card.Session.RequiredCookies; len(got) != 1 || got[0] != "sid" {
		t.Fatalf("required_cookies = %v, want [sid]", got)
	}
}
