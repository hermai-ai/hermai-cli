package probe

import (
	"errors"
	"strings"
)

// errAntiBot signals that a response is an anti-bot challenge, not real content.
// Strategies return this to trigger stealth escalation in the Probe function.
var errAntiBot = errors.New("probe: anti-bot challenge detected")

// antiBotSignals are HTML content patterns found in common anti-bot challenge pages.
// Matches on 403/429/503 responses where any single signal is sufficient.
var antiBotSignals = []string{
	"checking your browser",
	"before you access",
	"robot or human",
	"press & hold",
	"are you a human",
	"just a moment",
	"cf-browser-verification",
	"cf_chl_opt",
	"enable javascript and cookies to continue",
	"datadome",
	"geo.captcha-delivery.com",
	"captcha-delivery",
	"please verify you are a human",
	"access to this page has been denied",
}

// highConfidenceSignals are phrases found exclusively on anti-bot challenge
// pages — safe to match even on 200 responses.
var highConfidenceSignals = []string{
	"checking your browser before you access",
	"robot or human",
	"just a moment...",
	"cf-browser-verification",
	"cf_chl_opt",
	"geo.captcha-delivery.com",
	"please verify you are a human",
}

// isBlockedResponse returns true when the HTTP response looks like an anti-bot
// challenge rather than real content.
func isBlockedResponse(statusCode int, body string) bool {
	lower := strings.ToLower(body)

	switch statusCode {
	case 403, 429, 503:
		for _, signal := range antiBotSignals {
			if strings.Contains(lower, signal) {
				return true
			}
		}
	case 200:
		for _, signal := range highConfidenceSignals {
			if strings.Contains(lower, signal) {
				return true
			}
		}
	}
	return false
}
