package browser

import (
	"testing"
)

func TestFilterHAR_KeepsJSONDropsOther(t *testing.T) {
	har := &HARLog{
		Entries: []HAREntry{
			{
				Request:  HARRequest{Method: "GET", URL: "https://api.example.com/data"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
			{
				Request:  HARRequest{Method: "GET", URL: "https://example.com/style.css"},
				Response: HARResponse{Status: 200, ContentType: "text/css"},
			},
			{
				Request:  HARRequest{Method: "GET", URL: "https://example.com/logo.png"},
				Response: HARResponse{Status: 200, ContentType: "image/png"},
			},
			{
				Request:  HARRequest{Method: "POST", URL: "https://api.example.com/graphql"},
				Response: HARResponse{Status: 200, ContentType: "application/graphql+json"},
			},
			{
				Request:  HARRequest{Method: "GET", URL: "https://api.example.com/feed"},
				Response: HARResponse{Status: 200, ContentType: "text/json"},
			},
			{
				Request:  HARRequest{Method: "POST", URL: "https://api.example.com/query"},
				Response: HARResponse{Status: 200, ContentType: "application/graphql"},
			},
		},
	}

	result := FilterHAR(har)

	if len(result.Entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(result.Entries))
	}

	urls := make([]string, len(result.Entries))
	for i, e := range result.Entries {
		urls[i] = e.Request.URL
	}

	expected := []string{
		"https://api.example.com/data",
		"https://api.example.com/graphql",
		"https://api.example.com/feed",
		"https://api.example.com/query",
	}
	for i, want := range expected {
		if urls[i] != want {
			t.Errorf("entry %d: expected URL %s, got %s", i, want, urls[i])
		}
	}
}

func TestFilterHAR_DropsNoiseURLs(t *testing.T) {
	har := &HARLog{
		Entries: []HAREntry{
			{
				Request:  HARRequest{Method: "GET", URL: "https://api.example.com/users"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
			{
				Request:  HARRequest{Method: "POST", URL: "https://analytics.example.com/collect"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
			{
				Request:  HARRequest{Method: "POST", URL: "https://api.example.com/tracking/event"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
			{
				Request:  HARRequest{Method: "GET", URL: "https://api.example.com/beacon/ping"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
			{
				Request:  HARRequest{Method: "POST", URL: "https://sentry.io/api/123/envelope"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
			{
				Request:  HARRequest{Method: "GET", URL: "https://api.example.com/healthz"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
			{
				Request:  HARRequest{Method: "POST", URL: "https://api.segment.io/v1/track"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
			{
				Request:  HARRequest{Method: "GET", URL: "https://www.googletagmanager.com/gtag/js"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
			{
				Request:  HARRequest{Method: "POST", URL: "https://api.mixpanel.com/track"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
			{
				Request:  HARRequest{Method: "POST", URL: "https://api.amplitude.com/2/httpapi"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
			{
				Request:  HARRequest{Method: "POST", URL: "https://connect.facebook.net/fbevents/track"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
			{
				Request:  HARRequest{Method: "POST", URL: "https://script.hotjar.com/log"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
		},
	}

	result := FilterHAR(har)

	if len(result.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result.Entries))
	}
	if result.Entries[0].Request.URL != "https://api.example.com/users" {
		t.Errorf("expected users URL, got %s", result.Entries[0].Request.URL)
	}
}

func TestFilterHAR_DeduplicatesByMethodAndURL(t *testing.T) {
	har := &HARLog{
		Entries: []HAREntry{
			{
				Request:  HARRequest{Method: "GET", URL: "https://api.example.com/users"},
				Response: HARResponse{Status: 200, ContentType: "application/json", Body: "first"},
			},
			{
				Request:  HARRequest{Method: "GET", URL: "https://api.example.com/users"},
				Response: HARResponse{Status: 200, ContentType: "application/json", Body: "second"},
			},
			{
				Request:  HARRequest{Method: "POST", URL: "https://api.example.com/users"},
				Response: HARResponse{Status: 201, ContentType: "application/json"},
			},
		},
	}

	result := FilterHAR(har)

	if len(result.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result.Entries))
	}

	if result.Entries[0].Response.Body != "first" {
		t.Errorf("expected first occurrence to be kept, got body %q", result.Entries[0].Response.Body)
	}
	if result.Entries[1].Request.Method != "POST" {
		t.Errorf("expected POST method for second entry, got %s", result.Entries[1].Request.Method)
	}
}

func TestFilterHAR_CombinedFiltering(t *testing.T) {
	har := &HARLog{
		Entries: []HAREntry{
			{
				Request:  HARRequest{Method: "GET", URL: "https://api.example.com/products"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
			{
				Request:  HARRequest{Method: "GET", URL: "https://api.example.com/products"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
			{
				Request:  HARRequest{Method: "GET", URL: "https://example.com/style.css"},
				Response: HARResponse{Status: 200, ContentType: "text/css"},
			},
			{
				Request:  HARRequest{Method: "POST", URL: "https://analytics.example.com/event"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
			{
				Request:  HARRequest{Method: "GET", URL: "https://api.example.com/orders"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
		},
	}

	result := FilterHAR(har)

	if len(result.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result.Entries))
	}

	if result.Entries[0].Request.URL != "https://api.example.com/products" {
		t.Errorf("expected products URL first, got %s", result.Entries[0].Request.URL)
	}
	if result.Entries[1].Request.URL != "https://api.example.com/orders" {
		t.Errorf("expected orders URL second, got %s", result.Entries[1].Request.URL)
	}
}

func TestFilterHAR_EmptyHAR(t *testing.T) {
	har := &HARLog{Entries: []HAREntry{}}

	result := FilterHAR(har)

	if len(result.Entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(result.Entries))
	}
}

func TestFilterHAR_DoesNotMutateInput(t *testing.T) {
	har := &HARLog{
		Entries: []HAREntry{
			{
				Request:  HARRequest{Method: "GET", URL: "https://api.example.com/data"},
				Response: HARResponse{Status: 200, ContentType: "application/json"},
			},
			{
				Request:  HARRequest{Method: "GET", URL: "https://example.com/style.css"},
				Response: HARResponse{Status: 200, ContentType: "text/css"},
			},
		},
	}

	originalLen := len(har.Entries)
	_ = FilterHAR(har)

	if len(har.Entries) != originalLen {
		t.Errorf("input was mutated: expected %d entries, got %d", originalLen, len(har.Entries))
	}
}
