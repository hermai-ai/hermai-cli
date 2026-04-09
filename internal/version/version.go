package version

// Version is set at build time via ldflags.
var Version = "dev"

// UserAgent returns the User-Agent header value for HTTP requests.
func UserAgent() string {
	return "hermai/" + Version
}
