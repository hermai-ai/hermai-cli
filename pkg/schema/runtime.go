package schema

// Runtime describes the JavaScript the CLI runs to (a) bootstrap
// per-session state like animation keys and (b) sign each outgoing
// request. Both are optional — sites with no anti-bot signing (most GET
// schemas) don't need a Runtime at all.
//
// The CLI's sandbox executes this JS in goja (see pkg/signer). Bootstrap
// JS gets fetch + HTML parsing capabilities gated by AllowedHosts;
// signer JS is a pure function of the per-request input plus the state
// bootstrap produced.
//
// This type was added when we pivoted from compiled-in per-site
// bootstrap code to schema-resident JS: new Path-1 sites now ship as
// schemas rather than CLI releases.
type Runtime struct {
	// BootstrapJS is source code defining a global `bootstrap(input)`
	// function. Runs when the session's cached state is missing or past
	// BootstrapTTLSeconds. Must return an object whose values are all
	// strings — every key becomes a field on the signer's State map.
	BootstrapJS string `json:"bootstrap_js,omitempty"`

	// SignerJS is source code defining a global `sign(input)` function.
	// Runs before each outgoing request. Returns `{url, headers}`: url
	// may be the input URL with extra query parameters appended (e.g.
	// TikTok's X-Bogus); headers are merged onto the request, overriding
	// same-named existing values.
	SignerJS string `json:"signer_js,omitempty"`

	// AllowedHosts restricts hermai.fetch inside BootstrapJS. Exact
	// hostname match, case-insensitive. Required when BootstrapJS calls
	// fetch — an empty list blocks every outbound call.
	AllowedHosts []string `json:"allowed_hosts,omitempty"`

	// BootstrapTTLSeconds is how long bootstrap state stays valid
	// before re-running. Zero means use the CLI default (3600 = 1h).
	// Schemas for sites with aggressive key rotation (TikTok's msToken)
	// should set this low; X is comfortable with an hour.
	BootstrapTTLSeconds int `json:"bootstrap_ttl_seconds,omitempty"`
}

// NeedsBootstrap reports whether the runtime has bootstrap JS to run.
func (r *Runtime) NeedsBootstrap() bool {
	return r != nil && r.BootstrapJS != ""
}

// NeedsSigner reports whether the runtime has a per-request signer.
func (r *Runtime) NeedsSigner() bool {
	return r != nil && r.SignerJS != ""
}
