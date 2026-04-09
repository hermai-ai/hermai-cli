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

// Action requirement constants keep runtime behavior explicit.
const (
	ActionAuthPublicOnly = "public_only"
	ActionSessionNone    = "none"
	ActionSessionCookies = "cookie_jar"
	ActionJSNotRequired  = "not_required"
	ActionJSOptional     = "optional"
)

// Action describes a browserless, agent-callable website interaction.
type Action struct {
	Name         string             `json:"name"`
	Description  string             `json:"description,omitempty"`
	Kind         string             `json:"kind"`
	Transport    string             `json:"transport"`
	Method       string             `json:"method,omitempty"`
	URLTemplate  string             `json:"url_template"`
	Headers      map[string]string  `json:"headers,omitempty"`
	Params       []ActionParam      `json:"params,omitempty"`
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
