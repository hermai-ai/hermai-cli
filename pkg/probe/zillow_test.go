package probe

import (
	"encoding/json"
	"net/url"
	"testing"
)

func TestIsZillowSearch(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://www.zillow.com/san-francisco-ca/", true},
		{"https://www.zillow.com/san-francisco-ca/rentals/", true},
		{"https://www.zillow.com/san-francisco-ca/rentals/5-_beds/", true},
		{"https://www.zillow.com/san-francisco-ca/rentals/5-_beds/2_p/", true},
		{"https://zillow.com/san-francisco-ca/", true},
		{"https://www.zillow.com/homedetails/123-Main-St/12345_zpid/", false},
		{"https://www.zillow.com/", false},
		{"https://www.zillow.com/mortgage-calculator/", false},
		{"https://github.com/golang/go", false},
		{"https://www.redfin.com/city/san-francisco/", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := IsZillowSearch(tt.url); got != tt.want {
				t.Errorf("IsZillowSearch(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestNormalizeZillowSearchURL(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantRentals bool
		wantBeds    int
		wantPage    int
		unchanged   bool // expect input == output
	}{
		{
			name:        "rentals with beds",
			input:       "https://www.zillow.com/san-francisco-ca/rentals/5-_beds/",
			wantRentals: true,
			wantBeds:    5,
		},
		{
			name:        "rentals only",
			input:       "https://www.zillow.com/san-francisco-ca/rentals/",
			wantRentals: true,
		},
		{
			name:        "base city search",
			input:       "https://www.zillow.com/san-francisco-ca/",
			wantRentals: false,
		},
		{
			name:     "with pagination",
			input:    "https://www.zillow.com/san-francisco-ca/rentals/5-_beds/2_p/",
			wantBeds: 5,
			wantPage: 2,
		},
		{
			name:      "already has searchQueryState",
			input:     "https://www.zillow.com/san-francisco-ca/rentals/?searchQueryState=%7B%22test%22%3Atrue%7D",
			unchanged: true,
		},
		{
			name:      "detail page unchanged",
			input:     "https://www.zillow.com/homedetails/123-Main/12345_zpid/",
			unchanged: true,
		},
		{
			name:      "non-zillow unchanged",
			input:     "https://github.com/golang/go",
			unchanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeZillowSearchURL(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.unchanged {
				if got != tt.input {
					t.Errorf("expected unchanged URL, got %q", got)
				}
				return
			}

			parsed, err := url.Parse(got)
			if err != nil {
				t.Fatalf("failed to parse output URL: %v", err)
			}

			raw := parsed.Query().Get("searchQueryState")
			if raw == "" {
				t.Fatal("expected searchQueryState in query, got none")
			}

			var state map[string]any
			if err := json.Unmarshal([]byte(raw), &state); err != nil {
				t.Fatalf("failed to parse searchQueryState JSON: %v", err)
			}

			filterState, _ := state["filterState"].(map[string]any)
			if filterState == nil {
				t.Fatal("expected filterState in searchQueryState")
			}

			// Check rentals filter
			if tt.wantRentals {
				fr, _ := filterState["fr"].(map[string]any)
				if fr == nil || fr["value"] != true {
					t.Error("expected fr.value=true for rentals filter")
				}
			}

			// Check beds filter
			if tt.wantBeds > 0 {
				beds, _ := filterState["beds"].(map[string]any)
				if beds == nil {
					t.Fatalf("expected beds filter, got nil")
				}
				min, _ := beds["min"].(float64)
				if int(min) != tt.wantBeds {
					t.Errorf("beds.min: got %v, want %d", min, tt.wantBeds)
				}
			}

			// Check pagination
			if tt.wantPage > 1 {
				pagination, _ := state["pagination"].(map[string]any)
				if pagination == nil {
					t.Fatal("expected pagination object")
				}
				page, _ := pagination["currentPage"].(float64)
				if int(page) != tt.wantPage {
					t.Errorf("pagination.currentPage: got %v, want %d", page, tt.wantPage)
				}
			}
		})
	}
}
