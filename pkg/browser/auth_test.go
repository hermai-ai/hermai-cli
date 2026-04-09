package browser

import (
	"testing"
)

func TestDetectAuth_HTTPStatus401(t *testing.T) {
	signals := AuthSignals{
		HTTPStatus:  401,
		FinalURL:    "https://example.com/dashboard",
		DOMSnapshot: "<html><body>Unauthorized</body></html>",
	}

	if !DetectAuth(signals) {
		t.Error("expected true for 401 status")
	}
}

func TestDetectAuth_HTTPStatus403(t *testing.T) {
	signals := AuthSignals{
		HTTPStatus:  403,
		FinalURL:    "https://example.com/dashboard",
		DOMSnapshot: "<html><body>Forbidden</body></html>",
	}

	if !DetectAuth(signals) {
		t.Error("expected true for 403 status")
	}
}

func TestDetectAuth_HTTPStatus200(t *testing.T) {
	signals := AuthSignals{
		HTTPStatus:  200,
		FinalURL:    "https://example.com/dashboard",
		DOMSnapshot: "<html><body>Welcome</body></html>",
	}

	if DetectAuth(signals) {
		t.Error("expected false for 200 status with normal content")
	}
}

func TestDetectAuth_HTTPStatus500(t *testing.T) {
	signals := AuthSignals{
		HTTPStatus:  500,
		FinalURL:    "https://example.com/dashboard",
		DOMSnapshot: "<html><body>Server Error</body></html>",
	}

	if DetectAuth(signals) {
		t.Error("expected false for 500 status with normal content")
	}
}

func TestDetectAuth_LoginURL(t *testing.T) {
	loginURLs := []string{
		"https://example.com/login",
		"https://example.com/signin",
		"https://example.com/sign-in",
		"https://example.com/sign_in",
		"https://example.com/auth/callback",
		"https://example.com/sso/redirect",
		"https://example.com/authorize",
		"https://example.com/oauth/token",
	}

	for _, u := range loginURLs {
		signals := AuthSignals{
			HTTPStatus:  200,
			FinalURL:    u,
			DOMSnapshot: "<html><body>Page</body></html>",
		}

		if !DetectAuth(signals) {
			t.Errorf("expected true for login URL %s", u)
		}
	}
}

func TestDetectAuth_LoginURLCaseInsensitive(t *testing.T) {
	signals := AuthSignals{
		HTTPStatus:  200,
		FinalURL:    "https://example.com/Login",
		DOMSnapshot: "<html><body>Page</body></html>",
	}

	if !DetectAuth(signals) {
		t.Error("expected true for /Login (case insensitive)")
	}
}

func TestDetectAuth_NormalURL(t *testing.T) {
	normalURLs := []string{
		"https://example.com/dashboard",
		"https://example.com/api/users",
		"https://example.com/products/catalog",
		"https://example.com/blog/logging-best-practices",
	}

	for _, u := range normalURLs {
		signals := AuthSignals{
			HTTPStatus:  200,
			FinalURL:    u,
			DOMSnapshot: "<html><body>Content</body></html>",
		}

		if DetectAuth(signals) {
			t.Errorf("expected false for normal URL %s", u)
		}
	}
}

func TestDetectAuth_PasswordInputInDOM(t *testing.T) {
	signals := AuthSignals{
		HTTPStatus:  200,
		FinalURL:    "https://example.com/page",
		DOMSnapshot: `<html><body><input type="password" name="pass"></body></html>`,
	}

	if !DetectAuth(signals) {
		t.Error("expected true for password input in DOM")
	}
}

func TestDetectAuth_SignInTextInDOM(t *testing.T) {
	signals := AuthSignals{
		HTTPStatus:  200,
		FinalURL:    "https://example.com/page",
		DOMSnapshot: `<html><body><h1>Sign In</h1></body></html>`,
	}

	if !DetectAuth(signals) {
		t.Error("expected true for 'Sign In' text in DOM")
	}
}

func TestDetectAuth_SignUpTextInDOM(t *testing.T) {
	signals := AuthSignals{
		HTTPStatus:  200,
		FinalURL:    "https://example.com/page",
		DOMSnapshot: `<html><body><button>Sign Up</button></body></html>`,
	}

	if !DetectAuth(signals) {
		t.Error("expected true for 'Sign Up' text in DOM")
	}
}

func TestDetectAuth_LogInTextInDOM(t *testing.T) {
	signals := AuthSignals{
		HTTPStatus:  200,
		FinalURL:    "https://example.com/page",
		DOMSnapshot: `<html><body><a href="/auth">Log In</a></body></html>`,
	}

	if !DetectAuth(signals) {
		t.Error("expected true for 'Log In' text in DOM")
	}
}

func TestDetectAuth_LogOnTextInDOM(t *testing.T) {
	signals := AuthSignals{
		HTTPStatus:  200,
		FinalURL:    "https://example.com/page",
		DOMSnapshot: `<html><body><button>Log On</button></body></html>`,
	}

	if !DetectAuth(signals) {
		t.Error("expected true for 'Log On' text in DOM")
	}
}

func TestDetectAuth_NormalDOMContent(t *testing.T) {
	signals := AuthSignals{
		HTTPStatus:  200,
		FinalURL:    "https://example.com/page",
		DOMSnapshot: `<html><body><h1>Welcome to our store</h1><p>Browse products</p></body></html>`,
	}

	if DetectAuth(signals) {
		t.Error("expected false for normal DOM content")
	}
}

func TestDetectAuth_AllSignalsFalse(t *testing.T) {
	signals := AuthSignals{
		HTTPStatus:  200,
		FinalURL:    "https://example.com/products",
		DOMSnapshot: "<html><body>Products page</body></html>",
	}

	if DetectAuth(signals) {
		t.Error("expected false when all signals are negative")
	}
}

func TestDetectAuth_CombinedSignals(t *testing.T) {
	signals := AuthSignals{
		HTTPStatus:  403,
		FinalURL:    "https://example.com/login?redirect=/dashboard",
		DOMSnapshot: `<html><body><input type="password"><button>Sign In</button></body></html>`,
	}

	if !DetectAuth(signals) {
		t.Error("expected true when multiple auth signals are present")
	}
}
