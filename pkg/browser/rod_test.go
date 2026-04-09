package browser

import (
	"testing"
	"time"
)

func TestCaptureOpts_ZeroValues(t *testing.T) {
	opts := CaptureOpts{}

	if opts.Timeout != 0 {
		t.Errorf("default Timeout should be zero value, got %v", opts.Timeout)
	}
	if opts.WaitAfterLoad != 0 {
		t.Errorf("default WaitAfterLoad should be zero value, got %v", opts.WaitAfterLoad)
	}
	if opts.ProxyURL != "" {
		t.Errorf("default ProxyURL should be empty, got %q", opts.ProxyURL)
	}
	if opts.BrowserPath != "" {
		t.Errorf("default BrowserPath should be empty, got %q", opts.BrowserPath)
	}
}

func TestIsJSONContentType(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		want        bool
	}{
		{name: "application/json", contentType: "application/json", want: true},
		{name: "application/json with charset", contentType: "application/json; charset=utf-8", want: true},
		{name: "application/graphql+json", contentType: "application/graphql+json", want: true},
		{name: "application/graphql", contentType: "application/graphql", want: true},
		{name: "text/json", contentType: "text/json", want: true},
		{name: "text/html", contentType: "text/html", want: false},
		{name: "text/css", contentType: "text/css", want: false},
		{name: "image/png", contentType: "image/png", want: false},
		{name: "empty", contentType: "", want: false},
		{name: "application/javascript", contentType: "application/javascript", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isJSONContentType(tt.contentType)
			if got != tt.want {
				t.Errorf("isJSONContentType(%q) = %v, want %v", tt.contentType, got, tt.want)
			}
		})
	}
}

func TestErrAuthWall(t *testing.T) {
	if ErrAuthWall == nil {
		t.Fatal("ErrAuthWall should not be nil")
	}
	if ErrAuthWall.Error() != "hermai: page requires authentication" {
		t.Errorf("unexpected error message: %s", ErrAuthWall.Error())
	}
}

func TestDefaultConstants(t *testing.T) {
	if defaultTimeout.Seconds() != 60 {
		t.Errorf("defaultTimeout should be 60s, got %v", defaultTimeout)
	}
	if defaultWaitAfterLoad != 1500*time.Millisecond {
		t.Errorf("defaultWaitAfterLoad should be 1.5s, got %v", defaultWaitAfterLoad)
	}
}
