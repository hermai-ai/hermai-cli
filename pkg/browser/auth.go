package browser

import (
	"strings"
)

// AuthSignals holds the signals used to detect auth walls.
type AuthSignals struct {
	HTTPStatus  int
	FinalURL    string
	DOMSnapshot string
}

// loginPathPatterns are URL path fragments that indicate a login/auth redirect.
var loginPathPatterns = []string{
	"/login",
	"/signin",
	"/sign-in",
	"/sign_in",
	"/auth",
	"/sso",
	"/authorize",
	"/oauth",
}

// loginDOMPatterns are DOM content fragments that indicate a login page.
var loginDOMPatterns = []string{
	`type="password"`,
	"sign in",
	"sign up",
	"log in",
	"log on",
}

// DetectAuth returns true if the given signals indicate the page is behind
// an authentication wall.
//
// Checks (any match returns true):
//  1. HTTP 401 or 403
//  2. FinalURL contains a login-related path (case-insensitive)
//  3. DOMSnapshot contains login-related content (case-insensitive)
func DetectAuth(signals AuthSignals) bool {
	if signals.HTTPStatus == 401 || signals.HTTPStatus == 403 {
		return true
	}

	if hasLoginPath(signals.FinalURL) {
		return true
	}

	if hasLoginDOM(signals.DOMSnapshot) {
		return true
	}

	return false
}

func hasLoginPath(rawURL string) bool {
	urlLower := strings.ToLower(rawURL)

	for _, pattern := range loginPathPatterns {
		if strings.Contains(urlLower, pattern) {
			return true
		}
	}

	return false
}

func hasLoginDOM(dom string) bool {
	domLower := strings.ToLower(dom)

	for _, pattern := range loginDOMPatterns {
		if strings.Contains(domLower, pattern) {
			return true
		}
	}

	return false
}
