package atc

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// WebhookMatcher defines how to match a webhook payload to a resource
// based on the resource's source configuration. Configured per resource type
// per webhook type in the operator's base-resource-type-defaults YAML.
type WebhookMatcher struct {
	// SourceField is the key within the resource's source config that identifies it.
	// e.g. "uri" for git resources, "repository" for registry-image.
	SourceField string `yaml:"source_field" json:"source_field"`

	// SourcePattern is a regex applied to the source field value to extract
	// the identifier. The first capture group is used. If empty, the raw value is used.
	// e.g. "github\\.com/(.+?)(?:\\.git)?$" extracts "owner/repo" from a GitHub URI.
	SourcePattern string `yaml:"source_pattern" json:"source_pattern"`

	// PayloadField is a dot-separated path into the webhook payload JSON to extract
	// the identifier to match against.
	// e.g. "repository.full_name" extracts the repo name from a GitHub webhook payload.
	PayloadField string `yaml:"payload_field" json:"payload_field"`

	// SignatureHeader is the HTTP header containing the webhook signature.
	// e.g. "X-Hub-Signature-256" for GitHub, "X-Gitlab-Token" for GitLab.
	SignatureHeader string `yaml:"signature_header,omitempty" json:"signature_header,omitempty"`

	// SignatureAlgo is the algorithm used for signature validation.
	// Supported: "hmac-sha256" (GitHub/Bitbucket), "plain" (GitLab).
	// If empty, no signature validation is performed via matcher.
	SignatureAlgo string `yaml:"signature_algo,omitempty" json:"signature_algo,omitempty"`

	// compiledPattern is the pre-compiled regex for SourcePattern.
	// Compiled eagerly at load time via LoadWebhookMatchers.
	compiledPattern *regexp.Regexp
}

// webhookMatchers stores the loaded matchers keyed by resource type then webhook type.
// e.g. webhookMatchers["git"]["github"] = &WebhookMatcher{...}
var webhookMatchers = map[string]map[string]*WebhookMatcher{}

// LoadWebhookMatchers loads webhook matchers from the parsed defaults config.
// Regex patterns are compiled eagerly at load time for early error detection
// and to avoid recompilation on each match.
func LoadWebhookMatchers(matchers map[string]map[string]WebhookMatcher) {
	compiled := map[string]map[string]*WebhookMatcher{}
	for resourceType, byWebhookType := range matchers {
		compiled[resourceType] = map[string]*WebhookMatcher{}
		for webhookType, m := range byWebhookType {
			matcher := &WebhookMatcher{
				SourceField:     m.SourceField,
				SourcePattern:   m.SourcePattern,
				PayloadField:    m.PayloadField,
				SignatureHeader: m.SignatureHeader,
				SignatureAlgo:   m.SignatureAlgo,
			}
			if matcher.SourcePattern != "" {
				matcher.compiledPattern, _ = regexp.Compile(matcher.SourcePattern)
			}
			compiled[resourceType][webhookType] = matcher
		}
	}
	webhookMatchers = compiled
}

// FindWebhookMatcher looks up a matcher for a given resource type and webhook type.
func FindWebhookMatcher(resourceType, webhookType string) (*WebhookMatcher, bool) {
	byType, ok := webhookMatchers[resourceType]
	if !ok {
		return nil, false
	}
	matcher, ok := byType[webhookType]
	return matcher, ok
}

// MatchResourceSource checks if a resource's source config matches a webhook payload
// using the given matcher. Returns true if the extracted identifiers match.
func (m *WebhookMatcher) MatchResourceSource(source Source, payload json.RawMessage) bool {
	if m.SourceField == "" || m.PayloadField == "" {
		return false
	}

	// Extract identifier from resource source
	sourceValue, ok := source[m.SourceField]
	if !ok {
		return false
	}

	sourceStr, ok := sourceValue.(string)
	if !ok {
		return false
	}

	sourceID := sourceStr
	if m.SourcePattern != "" {
		if m.compiledPattern == nil {
			return false
		}
		matches := m.compiledPattern.FindStringSubmatch(sourceStr)
		if len(matches) < 2 {
			return false
		}
		sourceID = matches[1]
	}

	// Extract identifier from webhook payload using dot-path
	payloadID := extractJSONField(payload, m.PayloadField)
	if payloadID == "" {
		return false
	}

	return strings.EqualFold(sourceID, payloadID)
}

// extractJSONField extracts a string value from JSON using a dot-separated path.
// e.g. extractJSONField(payload, "repository.full_name") returns the string value.
func extractJSONField(data json.RawMessage, path string) string {
	parts := strings.Split(path, ".")

	var current interface{}
	if err := json.Unmarshal(data, &current); err != nil {
		return ""
	}

	for _, part := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return ""
		}
		current, ok = m[part]
		if !ok {
			return ""
		}
	}

	switch v := current.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%g", v)
	default:
		return ""
	}
}
