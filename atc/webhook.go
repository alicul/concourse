package atc

// Webhook represents a team-scoped shared webhook endpoint.
type Webhook struct {
	ID              int                  `json:"id"`
	Name            string               `json:"name"`
	Type            string               `json:"type"`
	Token           string               `json:"token,omitempty"`
	Secret          string               `json:"secret,omitempty"`
	Rules           []WebhookMatcherRule `json:"rules,omitempty"`
	SignatureHeader string               `json:"signature_header,omitempty"`
	SignatureAlgo   string               `json:"signature_algo,omitempty"`
	TeamID          int                  `json:"team_id"`
	URL             string               `json:"url,omitempty"`
}

// WebhookSubscription defines a webhook subscription on a resource.
// The Type must match the type of a webhook created via fly set-webhook.
// The Filter is a JSON object used for Postgres containment-based payload filtering.
type WebhookSubscription struct {
	Type   string         `json:"type"`
	Filter map[string]any `json:"filter,omitempty"`
}
