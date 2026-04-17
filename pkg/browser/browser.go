package browser

import (
	"context"
	"time"
)

// Service defines the browser capture interface.
type Service interface {
	Capture(ctx context.Context, targetURL string, opts CaptureOpts) (*CaptureResult, error)
	Close() error
}

// CaptureOpts holds options for a browser capture session.
type CaptureOpts struct {
	ProxyURL      string
	BrowserPath   string
	Timeout       time.Duration
	WaitAfterLoad time.Duration
	Cookies       []string // name=value pairs to inject before navigation
	// Headful launches a visible Chrome window so the operator can
	// perform real-user interactions (save draft, add to cart, etc.)
	// while the capture runs. Default false — headless for CI/automated
	// discovery.
	Headful bool
}

// CaptureResult holds the output of a browser capture session.
type CaptureResult struct {
	HAR          *HARLog
	DOMSnapshot  string
	RenderedHTML string
}
