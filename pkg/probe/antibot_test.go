package probe

import "testing"

func TestIsBlockedResponse(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       bool
	}{
		{
			name:       "cloudflare 403 just a moment",
			statusCode: 403,
			body:       `<html><body><h1>Just a moment...</h1><p>Checking your browser</p></body></html>`,
			want:       true,
		},
		{
			name:       "normal 403 forbidden",
			statusCode: 403,
			body:       `{"error": "forbidden", "message": "access denied"}`,
			want:       false,
		},
		{
			name:       "503 checking your browser",
			statusCode: 503,
			body:       `<html><body>Checking your browser before you access the website</body></html>`,
			want:       true,
		},
		{
			name:       "429 with anti-bot markers",
			statusCode: 429,
			body:       `<html><body>Please verify you are a human</body></html>`,
			want:       true,
		},
		{
			name:       "429 plain rate limit",
			statusCode: 429,
			body:       `{"error": "rate limit exceeded", "retry_after": 60}`,
			want:       false,
		},
		{
			name:       "200 with cf_chl_opt challenge",
			statusCode: 200,
			body:       `<html><head><script>var cf_chl_opt={}</script></head><body></body></html>`,
			want:       true,
		},
		{
			name:       "200 normal HTML",
			statusCode: 200,
			body:       `<html><body><h1>Welcome to my site</h1></body></html>`,
			want:       false,
		},
		{
			name:       "200 datadome challenge",
			statusCode: 200,
			body:       `<html><body><iframe src="https://geo.captcha-delivery.com/captcha"></iframe></body></html>`,
			want:       true,
		},
		{
			name:       "404 not found",
			statusCode: 404,
			body:       `<html><body>Not found</body></html>`,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isBlockedResponse(tt.statusCode, tt.body)
			if got != tt.want {
				t.Errorf("isBlockedResponse(%d, ...) = %v, want %v", tt.statusCode, got, tt.want)
			}
		})
	}
}
