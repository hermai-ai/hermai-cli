package actionrunner

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

// TestXcom_EndToEnd_DryRun proves the full pipeline: a schema that
// embeds the signer + bootstrap JS (built inline from testdata so no
// per-site schema file lives in this CLI repo) drives the runner,
// resolves cached cookies + state, runs the signer, and produces a
// signed request ready to fire.
//
// Live verification against x.com is deliberately out of scope — that
// requires real user cookies and mutates the user's account. This test
// validates the wiring from schema JSON through to an outgoing request.
func TestXcom_EndToEnd_DryRun(t *testing.T) {
	signerJS, err := os.ReadFile(filepath.Join("testdata", "xcom", "signer.js"))
	if err != nil {
		t.Fatalf("read signer fixture: %v", err)
	}
	bootstrapJS, err := os.ReadFile(filepath.Join("testdata", "xcom", "bootstrap.js"))
	if err != nil {
		t.Fatalf("read bootstrap fixture: %v", err)
	}

	sch := &schema.Schema{
		ID:         "x.com@test",
		Domain:     "x.com",
		URLPattern: "https://x.com/.*",
		SchemaType: schema.SchemaTypeAPI,
		Version:    1,
		Actions: []schema.Action{
			{
				Name:        "CreateDraftTweet",
				Kind:        schema.ActionKindAPICall,
				Transport:   schema.ActionTransportAPICall,
				Method:      "POST",
				URLTemplate: "https://x.com/i/api/graphql/cH9HZWz_EW9gnswvA4ZRiQ/CreateDraftTweet",
				Headers: map[string]string{
					"Authorization":             "Bearer test-bearer-value",
					"Content-Type":              "application/json",
					"X-Twitter-Auth-Type":       "OAuth2Session",
					"X-Twitter-Active-User":     "yes",
					"X-Twitter-Client-Language": "en",
				},
				Params: []schema.ActionParam{
					{Name: "text", In: "body", Type: "string", Required: true},
				},
				BodyTemplate: `{"variables":{"post_tweet_request":{"auto_populate_reply_metadata":false,"status":"{{text}}","exclude_reply_user_ids":[],"media_ids":[],"thread_tweets":[]}},"queryId":"cH9HZWz_EW9gnswvA4ZRiQ"}`,
			},
		},
		Runtime: &schema.Runtime{
			BootstrapJS:         string(bootstrapJS),
			SignerJS:            string(signerJS),
			AllowedHosts:        []string{"x.com", "abs.twimg.com"},
			BootstrapTTLSeconds: 3600,
		},
	}

	// httptest binds loopback; we won't actually reach the network in
	// dry-run, but the Request.HTTPClient is still required.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("dry-run must not hit the network; got %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	dir := t.TempDir()
	siteDir := filepath.Join(dir, "x.com")
	writeCookies(t, siteDir, map[string]string{
		"auth_token": "fake-auth-token-for-e2e",
		"ct0":        "csrf-token-abc123",
		"twid":       "u=1234567890",
	})
	// Pre-seed bootstrap state so we don't try to fetch real x.com in
	// the test. The JS-sandbox bootstrap is covered end-to-end by
	// TestXcomBootstrapJS_MatchesGoReference.
	writeState(t, siteDir, map[string]string{
		"key_b64":                  "aGVybWFpLXRlc3Qta2V5LWZvci11bml0LXRlc3RpbmctYm9vdHN0cmFwLTEyMzQ1Ng==",
		"animation_key":            "ff000ee147ae147ae1805eb851eb851eb805eb851eb851eb80ee147ae147ae1800",
		"additional_random_number": "3",
	}, time.Now().UTC(), 3600)

	result, err := Run(context.Background(), Request{
		Schema:      sch,
		ActionName:  "CreateDraftTweet",
		Args:        map[string]string{"text": "drafted by hermai"},
		SessionsDir: dir,
		HTTPClient:  srv.Client(),
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.SignedReq == nil {
		t.Fatal("dry-run returned no SignedReq")
	}

	req := result.SignedReq
	if req.Method != "POST" {
		t.Errorf("method = %q, want POST", req.Method)
	}
	if !strings.Contains(req.URL.String(), "/i/api/graphql/cH9HZWz_EW9gnswvA4ZRiQ/CreateDraftTweet") {
		t.Errorf("URL does not match CreateDraftTweet: %s", req.URL.String())
	}
	if auth := req.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
		t.Errorf("Authorization header missing Bearer prefix: %q", auth)
	}
	txid := req.Header.Get("X-Client-Transaction-Id")
	if txid == "" {
		t.Error("x-client-transaction-id header missing — signer didn't run")
	}
	if len(txid) < 80 {
		t.Errorf("x-client-transaction-id length = %d, suspiciously short", len(txid))
	}
	if csrf := req.Header.Get("X-Csrf-Token"); csrf != "csrf-token-abc123" {
		t.Errorf("x-csrf-token = %q, want csrf-token-abc123", csrf)
	}
	cookie := req.Header.Get("Cookie")
	for _, want := range []string{"auth_token=fake-auth-token-for-e2e", "ct0=csrf-token-abc123"} {
		if !strings.Contains(cookie, want) {
			t.Errorf("cookie header missing %q: got %q", want, cookie)
		}
	}
	body, _ := io.ReadAll(req.Body)
	if !strings.Contains(string(body), `"status":"drafted by hermai"`) {
		t.Errorf("body missing user text in status field: %s", string(body))
	}
	if !strings.Contains(string(body), `"post_tweet_request"`) {
		t.Errorf("body missing GraphQL variables wrapper: %s", string(body))
	}

	t.Logf("E2E dry-run OK:\n  url=%s\n  txid=%s\n  body=%s", req.URL, txid, string(body))
}
