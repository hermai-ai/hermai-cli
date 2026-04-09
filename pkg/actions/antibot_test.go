package actions

import (
	"strings"
	"testing"
)

func TestIsAntiBotChallenge(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       bool
	}{
		{
			name:       "eBay challenge page",
			statusCode: 503,
			body:       `<html><body>Checking your browser before you access eBay.</body></html>`,
			want:       true,
		},
		{
			name:       "Walmart challenge page",
			statusCode: 403,
			body:       `<html><body><h1>Robot or human?</h1><p>Press & hold to confirm</p></body></html>`,
			want:       true,
		},
		{
			name:       "Cloudflare challenge",
			statusCode: 503,
			body:       `<html><head><title>Just a moment...</title></head><body>Enable JavaScript and cookies to continue</body></html>`,
			want:       true,
		},
		{
			name:       "200 challenge with large inline JS (eBay pattern)",
			statusCode: 200,
			body:       `<html><head><script>var a="` + strings.Repeat("x", 15000) + `";</script></head><body>Checking your browser before you access eBay. Please wait...</body></html>`,
			want:       true,
		},
		{
			name:       "normal 200 with real content",
			statusCode: 200,
			body:       `<html><body>` + strings.Repeat("This is a real page with lots of visible text content. ", 100) + `</body></html>`,
			want:       false,
		},
		{
			name:       "200 with low-confidence signal is not challenge",
			statusCode: 200,
			body:       `<html><body><p>Access to this page has been denied because you need to log in.</p></body></html>`,
			want:       false,
		},
		{
			name:       "normal 403 auth error",
			statusCode: 403,
			body:       `<html><body><h1>Forbidden</h1><p>You do not have permission to access this resource.</p></body></html>`,
			want:       false,
		},
		{
			name:       "normal 404",
			statusCode: 404,
			body:       `<html><body><h1>Not Found</h1></body></html>`,
			want:       false,
		},
		{
			name:       "short 200 with high-confidence challenge signal",
			statusCode: 200,
			body:       `<html><body>Checking your browser before you access this site.</body></html>`,
			want:       true,
		},
		{
			name:       "DataDome challenge",
			statusCode: 403,
			body:       `<html><head><script src="https://geo.captcha-delivery.com/captcha/"></script></head></html>`,
			want:       true,
		},
		{
			name:       "Cloudflare cf_chl_opt",
			statusCode: 403,
			body:       `<html><body><script>window._cf_chl_opt={}</script></body></html>`,
			want:       true,
		},
		{
			name:       "case insensitive detection",
			statusCode: 503,
			body:       `<html><body>CHECKING YOUR BROWSER before you access the site</body></html>`,
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAntiBotChallenge(tt.statusCode, tt.body)
			if got != tt.want {
				t.Errorf("isAntiBotChallenge(%d, %q...) = %v, want %v",
					tt.statusCode, truncate(tt.body, 60), got, tt.want)
			}
		})
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
