package httpclient

import (
	"crypto/tls"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"
)

// Doer is the minimal interface for executing HTTP requests.
// Both *http.Client and *tlsClientAdapter satisfy this.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Options configures the HTTP client built by New or NewStealth.
type Options struct {
	ProxyURL string
	Insecure bool
	Timeout  time.Duration
	WithJar  bool // attach a cookie jar (needed for clearance bootstrap)
}

// Default timeout applied when Options.Timeout is zero.
const DefaultTimeout = 30 * time.Second

// BrowserUserAgent is the User-Agent string used across the codebase for
// requests to target websites. Centralised here to prevent version drift.
const BrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// New creates a plain *http.Client (no TLS fingerprinting).
// Use for internal API calls (LLM, cache) where fingerprinting is unnecessary.
func New(opts Options) *http.Client {
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	transport := &http.Transport{}
	if opts.ProxyURL != "" {
		if parsed, err := url.Parse(opts.ProxyURL); err == nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}
	if opts.Insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}

	if opts.WithJar {
		jar, _ := cookiejar.New(nil)
		client.Jar = jar
	}

	return client
}
