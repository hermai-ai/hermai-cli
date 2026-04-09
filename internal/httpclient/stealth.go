package httpclient

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// DefaultProfile is the browser TLS+HTTP/2 fingerprint used by stealth clients.
var DefaultProfile = profiles.Chrome_146

// tlsClientAdapter wraps a bogdanfinn/tls-client HttpClient to satisfy Doer.
// It converts between net/http and fhttp types transparently.
type tlsClientAdapter struct {
	inner tls_client.HttpClient
}

// NewStealth creates a Doer with browser TLS+HTTP/2 fingerprinting.
// Use for outbound requests to target websites where anti-bot detection
// may inspect TLS ClientHello and HTTP/2 SETTINGS frames.
func NewStealth(opts Options) (Doer, error) {
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	clientOpts := []tls_client.HttpClientOption{
		tls_client.WithClientProfile(DefaultProfile),
		tls_client.WithTimeoutSeconds(int(timeout.Seconds())),
		tls_client.WithNotFollowRedirects(),
	}

	if opts.ProxyURL != "" {
		clientOpts = append(clientOpts, tls_client.WithProxyUrl(opts.ProxyURL))
	}
	if opts.Insecure {
		clientOpts = append(clientOpts, tls_client.WithInsecureSkipVerify())
	}

	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("httpclient: failed to create stealth client: %w", err)
	}

	return &tlsClientAdapter{inner: client}, nil
}

// Do converts a *net/http.Request into an *fhttp.Request, executes it
// via tls-client, and converts the *fhttp.Response back to *net/http.Response.
func (a *tlsClientAdapter) Do(req *http.Request) (*http.Response, error) {
	fReq, err := toFHTTPRequest(req)
	if err != nil {
		return nil, err
	}

	fResp, err := a.inner.Do(fReq)
	if err != nil {
		return nil, err
	}

	return toStdResponse(fResp, req), nil
}

// toFHTTPRequest converts a *net/http.Request to *fhttp.Request.
func toFHTTPRequest(req *http.Request) (*fhttp.Request, error) {
	fReq, err := fhttp.NewRequest(req.Method, req.URL.String(), req.Body)
	if err != nil {
		return nil, fmt.Errorf("httpclient: failed to convert request: %w", err)
	}

	// Copy all headers preserving order
	for key, values := range req.Header {
		for _, v := range values {
			fReq.Header.Add(key, v)
		}
	}

	// Carry context for cancellation/timeout
	fReq = fReq.WithContext(req.Context())

	return fReq, nil
}

// toStdResponse converts an *fhttp.Response to *net/http.Response.
func toStdResponse(fResp *fhttp.Response, origReq *http.Request) *http.Response {
	headers := make(http.Header, len(fResp.Header))
	for key, values := range fResp.Header {
		headers[key] = values
	}

	resp := &http.Response{
		Status:        fResp.Status,
		StatusCode:    fResp.StatusCode,
		Proto:         fResp.Proto,
		ProtoMajor:    fResp.ProtoMajor,
		ProtoMinor:    fResp.ProtoMinor,
		Header:        headers,
		Body:          fResp.Body,
		ContentLength: fResp.ContentLength,
		Uncompressed:  fResp.Uncompressed,
		Request:       origReq,
	}

	// Resolve Content-Length if not set but available in header
	if resp.ContentLength == -1 {
		if cl := headers.Get("Content-Length"); cl != "" {
			if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
				resp.ContentLength = n
			}
		}
	}

	// Parse Transfer-Encoding
	if te := headers.Get("Transfer-Encoding"); te != "" {
		resp.TransferEncoding = strings.Split(te, ",")
		for i := range resp.TransferEncoding {
			resp.TransferEncoding[i] = strings.TrimSpace(resp.TransferEncoding[i])
		}
	}

	return resp
}

// MustNewStealth is like NewStealth but panics on error (use in init paths).
func MustNewStealth(opts Options) Doer {
	d, err := NewStealth(opts)
	if err != nil {
		panic(err)
	}
	return d
}

// NewStealthOrFallback tries to create a stealth client; falls back to plain
// *http.Client on failure. Useful when fingerprinting is best-effort.
func NewStealthOrFallback(opts Options) Doer {
	d, err := NewStealth(opts)
	if err != nil {
		return New(opts)
	}
	return d
}

// redirectFollower wraps a Doer and follows redirects up to maxRedirects.
type redirectFollower struct {
	inner        Doer
	maxRedirects int
}

// NewStealthWithRedirects creates a stealth Doer that follows redirects.
// tls-client is configured with WithNotFollowRedirects so we handle it
// ourselves to keep the adapter's *http.Response.Request accurate.
func NewStealthWithRedirects(opts Options, maxRedirects int) (Doer, error) {
	d, err := NewStealth(opts)
	if err != nil {
		return nil, err
	}
	return &redirectFollower{inner: d, maxRedirects: maxRedirects}, nil
}

func (rf *redirectFollower) Do(req *http.Request) (*http.Response, error) {
	for i := 0; i <= rf.maxRedirects; i++ {
		resp, err := rf.inner.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode < 300 || resp.StatusCode >= 400 {
			return resp, nil
		}

		loc := resp.Header.Get("Location")
		if loc == "" {
			return resp, nil
		}

		// Drain and close the redirect response body
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		nextURL, err := req.URL.Parse(loc)
		if err != nil {
			return resp, nil
		}

		nextReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, nextURL.String(), nil)
		if err != nil {
			return resp, nil
		}

		// Carry forward essential headers
		for _, h := range []string{"User-Agent", "Accept", "Accept-Language", "Cookie"} {
			if v := req.Header.Get(h); v != "" {
				nextReq.Header.Set(h, v)
			}
		}

		req = nextReq
	}

	return nil, fmt.Errorf("httpclient: too many redirects (max %d)", rf.maxRedirects)
}
