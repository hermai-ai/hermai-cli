package schema

// Action kind constants describe what the agent is trying to do.
const (
	ActionKindAPICall    = "api_call"
	ActionKindNavigate   = "navigate"
	ActionKindSearch     = "search"
	ActionKindPaginate   = "paginate"
	ActionKindSubmitForm = "submit_form"
)

// Action transport constants describe how the action is executed.
const (
	ActionTransportAPICall      = "api_call"
	ActionTransportHTTPGet      = "http_get"
	ActionTransportHTTPPostForm = "http_post_form"
)

// Action requirement constants are the CANONICAL values for the Auth,
// Session, and JS fields on ActionRequirements. Schemas SHOULD use
// these strings where possible so the registry can categorize actions
// consistently, but the fields are intentionally typed as plain
// strings (not enums) because real-world sites carry requirement
// semantics that don't fit a small closed set — "user_session" vs
// "cookie_jar" vs "api_key" vs "oauth2_bearer" all appear in pushed
// schemas. Validators may warn on unknown values but must not reject
// them outright.
const (
	ActionAuthPublicOnly = "public_only"
	ActionSessionNone    = "none"
	ActionSessionCookies = "cookie_jar"
	ActionJSNotRequired  = "not_required"
	ActionJSOptional     = "optional"
)

// Action describes a browserless, agent-callable website interaction.
type Action struct {
	Name string `json:"name"`
	// Purpose is the user-voice one-liner the registry projects onto
	// the public card. Names what the action lets a caller do in a
	// sentence anyone (not just an engineer) could understand. Missing
	// purpose → action doesn't appear on the catalog card (by design —
	// the validator refuses to fabricate one from the paywalled
	// description). Example: "Save a tweet to the authenticated user's
	// drafts. Private — only visible to the signed-in account."
	Purpose      string             `json:"purpose,omitempty"`
	Description  string             `json:"description,omitempty"`
	Kind         string             `json:"kind"`
	Transport    string             `json:"transport"`
	Method       string             `json:"method,omitempty"`
	URLTemplate  string             `json:"url_template"`
	Headers      map[string]string  `json:"headers,omitempty"`
	Params       []ActionParam      `json:"params,omitempty"`
	// BodyTemplate, if non-empty, is used verbatim as the request body
	// after {{var}} substitution. Substituted values are JSON-escaped so
	// user input can't break out of the surrounding JSON string. When
	// empty, the runner synthesizes a body by JSON-marshaling every
	// Param with in="body" — useful for flat APIs, but most real-world
	// endpoints need an explicit template.
	BodyTemplate string             `json:"body_template,omitempty"`
	Result       *ActionResult      `json:"result,omitempty"`
	Requirements ActionRequirements `json:"requirements,omitempty"`
	Confidence   float64            `json:"confidence,omitempty"`
	Source       string             `json:"source,omitempty"`
}

// ActionParam describes one input accepted by an action.
type ActionParam struct {
	Name        string `json:"name"`
	In          string `json:"in"` // path, query, body, form, url
	Type        string `json:"type,omitempty"`
	Required    bool   `json:"required"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
}

// ActionResult describes the shape of data expected back from an action.
type ActionResult struct {
	EntityType     string          `json:"entity_type,omitempty"`
	Fields         []string        `json:"fields,omitempty"`
	ResponseSchema *ResponseSchema `json:"response_schema,omitempty"`
}

// ActionRequirements describes runtime constraints.
type ActionRequirements struct {
	Auth    string `json:"auth,omitempty"`
	Session string `json:"session,omitempty"`
	JS      string `json:"js,omitempty"`
}
