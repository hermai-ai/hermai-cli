package signer

import (
	"context"
	"testing"
)

// realisticSigner approximates an X-style transaction-id computation:
// concat (method, path, cookies, body), run it through SHA256 twice,
// HMAC with a key derived from a cookie, return the hex.
const realisticSigner = `
function sign(input) {
  var path = input.url.replace(/^https?:\/\/[^/]+/, "");
  var cookieBlob = (input.cookies["ct0"] || "") + "|" + (input.cookies["auth_token"] || "");
  var material = input.method + "|" + path + "|" + cookieBlob + "|" + input.body + "|" + input.now_ms;
  var h1 = hermai.sha256(material);
  var h2 = hermai.sha256(h1 + cookieBlob);
  var hmac = hermai.hmacSha256(input.cookies["ct0"] || "k", h2);
  return {
    url: input.url,
    headers: {
      "x-client-transaction-id": hermai.base64EncodeURL(hmac).substring(0, 94),
      "x-csrf-token": input.cookies["ct0"] || ""
    }
  };
}
`

func BenchmarkJSSigner_Realistic(b *testing.B) {
	s, err := NewJSSigner(realisticSigner)
	if err != nil {
		b.Fatal(err)
	}
	in := Input{
		Method:  "POST",
		URL:     "https://x.com/i/api/graphql/c50A_puUoQGK_4SXseYz3A/CreateTweet",
		Headers: map[string]string{"content-type": "application/json"},
		Body:    `{"variables":{"tweet_text":"hi","dark_request":false},"queryId":"c50A_puUoQGK_4SXseYz3A"}`,
		Cookies: map[string]string{"ct0": "abc123def", "auth_token": "tokentokentoken"},
		NowMS:   1700000000000,
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Sign(ctx, in); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSSigner_CompileCache(b *testing.B) {
	// Per-call cost when the program is already compiled & cached —
	// this is the common CLI path where the same schema is used many
	// times in one session.
	_, err := CachedJSSigner(realisticSigner)
	if err != nil {
		b.Fatal(err)
	}
	in := Input{Method: "GET", URL: "https://x.com/", NowMS: 1, Cookies: map[string]string{"ct0": "x"}}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s, _ := CachedJSSigner(realisticSigner)
		if _, err := s.Sign(ctx, in); err != nil {
			b.Fatal(err)
		}
	}
}
