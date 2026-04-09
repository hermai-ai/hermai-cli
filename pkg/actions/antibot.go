package actions

import "strings"

// antiBotSignals are HTML content patterns found in common anti-bot challenge pages.
// Each entry is a lowercase substring that indicates the response is a challenge, not real content.
var antiBotSignals = []string{
	// Akamai / eBay
	"checking your browser",
	"before you access",
	// PerimeterX / Walmart
	"robot or human",
	"press & hold",
	"are you a human",
	// Cloudflare
	"just a moment",
	"cf-browser-verification",
	"cf_chl_opt",
	"enable javascript and cookies to continue",
	// DataDome
	"datadome",
	"geo.captcha-delivery.com",
	// Generic captcha / challenge
	"captcha-delivery",
	"please verify you are a human",
	"access to this page has been denied",
}

// isAntiBotChallenge returns true when the HTTP response looks like an anti-bot
// challenge page rather than real content. Matches known challenge patterns in
// the response body. For non-200 status codes (403/429/503), any single signal
// is sufficient. For 200 responses, we require a high-confidence signal —
// phrases that never appear on legitimate pages.
func isAntiBotChallenge(statusCode int, body string) bool {
	lower := strings.ToLower(body)

	switch statusCode {
	case 403, 429, 503:
		for _, signal := range antiBotSignals {
			if strings.Contains(lower, signal) {
				return true
			}
		}
		return false
	case 200:
		// 200 challenges: only match high-confidence signals that are
		// unambiguous challenge markers (never on real content pages).
		for _, signal := range highConfidenceSignals {
			if strings.Contains(lower, signal) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// highConfidenceSignals are phrases found exclusively on anti-bot challenge
// pages. They never appear on legitimate content pages, so they're safe to
// match even on 200 responses.
var highConfidenceSignals = []string{
	"checking your browser before you access",
	"robot or human",
	"just a moment...",
	"cf-browser-verification",
	"cf_chl_opt",
	"geo.captcha-delivery.com",
	"please verify you are a human",
}
