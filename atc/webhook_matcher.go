package atc

import (
	"encoding/json"
	"regexp"
	"strings"
)

// WebhookMatcherRule maps one resource source field to one webhook payload field.
// All applicable rules (i.e. where the resource has the source field set) are
// evaluated with AND semantics: every applicable rule must pass for the resource
// to be triggered.
type WebhookMatcherRule struct {
	// SourceField is the key to look up in the resource's source config (e.g. "uri", "branch").
	SourceField string `yaml:"source_field" json:"source_field"`

	// SourcePattern is an optional regex applied to the source field value.
	// The first capture group is used as the identifier. If omitted, the raw value is used.
	SourcePattern string `yaml:"source_pattern,omitempty" json:"source_pattern,omitempty"`

	// PayloadField is a dot-separated path into the webhook JSON payload
	// (e.g. "repository.full_name", "ref").
	PayloadField string `yaml:"payload_field" json:"payload_field"`

	// PayloadPattern is an optional regex applied to the extracted payload value.
	// The first capture group is used. If omitted, the raw value is used.
	// Example: "refs/heads/(.+)" to extract branch name from "refs/heads/main".
	PayloadPattern string `yaml:"payload_pattern,omitempty" json:"payload_pattern,omitempty"`

	// SourceIsPattern: when true, the extracted source value is treated as a regex
	// and tested against the extracted payload value (instead of string equality).
	// Use this for fields like tag_filter or tag_regexp whose values are patterns.
	SourceIsPattern bool `yaml:"source_is_pattern,omitempty" json:"source_is_pattern,omitempty"`

	// Compiled patterns — internal, populated at load time.
	compiledSourcePattern  *regexp.Regexp
	compiledPayloadPattern *regexp.Regexp
}

// WebhookMatcher holds operator-configured rules for matching a specific webhook
// type to resources of a specific resource type.
type WebhookMatcher struct {
	Rules          []WebhookMatcherRule `yaml:"rules" json:"rules"`
	SignatureHeader string              `yaml:"signature_header,omitempty" json:"signature_header,omitempty"`
	// SignatureAlgo specifies how to validate the HMAC secret when one is
	// configured. Valid values:
	//   "hmac-sha256" (default) — GitHub / Bitbucket: validates "sha256=<hex>" in the signature header
	//   "plain"                 — GitLab: compares the header value directly with the secret (constant-time)
	SignatureAlgo string `yaml:"signature_algo,omitempty" json:"signature_algo,omitempty"`
}

// Supported signature algorithms.
const (
	SignatureAlgoHMACSHA256 = "hmac-sha256"
	SignatureAlgoPlain      = "plain"
)

// NewWebhookMatcher constructs a WebhookMatcher with all rule patterns pre-compiled.
// Used when building a matcher from per-webhook DB config (fly set-webhook --rules-file).
func NewWebhookMatcher(m WebhookMatcher) *WebhookMatcher {
	for i := range m.Rules {
		if m.Rules[i].SourcePattern != "" {
			m.Rules[i].compiledSourcePattern, _ = regexp.Compile(m.Rules[i].SourcePattern)
		}
		if m.Rules[i].PayloadPattern != "" {
			m.Rules[i].compiledPayloadPattern, _ = regexp.Compile(m.Rules[i].PayloadPattern)
		}
	}
	return &m
}

// webhookMatchers stores the loaded global operator matchers keyed by resource type,
// then webhook type. e.g. webhookMatchers["git"]["github"] = &WebhookMatcher{...}
var webhookMatchers = map[string]map[string]*WebhookMatcher{}

// LoadWebhookMatchers loads operator-configured matchers from a parsed YAML structure
// (from the CONCOURSE_WEBHOOK_MATCHERS config file). Patterns are compiled eagerly.
func LoadWebhookMatchers(input map[string]map[string]WebhookMatcher) {
	webhookMatchers = map[string]map[string]*WebhookMatcher{}
	for resourceType, byWebhookType := range input {
		webhookMatchers[resourceType] = map[string]*WebhookMatcher{}
		for webhookType, m := range byWebhookType {
			compiled := NewWebhookMatcher(m)
			webhookMatchers[resourceType][webhookType] = compiled
		}
	}
}

// FindWebhookMatcher returns the operator-configured matcher for the given resource
// type and webhook type, or nil if none is configured.
func FindWebhookMatcher(resourceType, webhookType string) *WebhookMatcher {
	if byType, ok := webhookMatchers[resourceType]; ok {
		return byType[webhookType]
	}
	return nil
}

// MatchResourceSource evaluates all rules against the given resource source config
// and webhook payload. Returns true if the resource should be triggered.
//
// Rule evaluation:
//   - If the resource's source does not have a rule's source_field set (empty or absent),
//     that rule is SKIPPED (the resource is not constrained on that dimension).
//   - If the source field IS set, the rule is evaluated:
//     - Extract identifier from source value (via source_pattern, or raw value).
//     - Extract identifier from payload (via payload_field dot-path + payload_pattern).
//     - Compare: if source_is_pattern=true, test payload value against source value as regex;
//       otherwise use case-insensitive string equality.
//   - All applicable (non-skipped) rules must pass. If any fails, returns false.
//   - If ALL rules are skipped (no matching source fields on resource), returns true
//     (wildcard — resource receives all events of this webhook type).
func (m *WebhookMatcher) MatchResourceSource(source map[string]interface{}, payload []byte) bool {
	var payloadMap map[string]interface{}
	if err := json.Unmarshal(payload, &payloadMap); err != nil {
		return false
	}

	for _, rule := range m.Rules {
		// Step 1: get source field value. Skip rule if absent or empty.
		rawSourceVal, ok := source[rule.SourceField]
		if !ok {
			continue
		}
		sourceStr, ok := rawSourceVal.(string)
		if !ok || sourceStr == "" {
			continue
		}

		// Step 2: extract identifier from source value.
		sourceID := extractWithPattern(sourceStr, rule.compiledSourcePattern)

		// Step 3: extract identifier from payload.
		payloadStr := extractJSONField(payloadMap, rule.PayloadField)
		if payloadStr == "" {
			// Payload doesn't have the field — can't match.
			return false
		}
		payloadID := extractWithPattern(payloadStr, rule.compiledPayloadPattern)

		// Step 4: compare.
		var matched bool
		if rule.SourceIsPattern {
			// Source value is itself a regex pattern (e.g. tag_filter: "v.*")
			re, err := regexp.Compile(sourceID)
			if err != nil {
				return false
			}
			matched = re.MatchString(payloadID)
		} else {
			matched = strings.EqualFold(sourceID, payloadID)
		}

		if !matched {
			return false
		}
	}

	return true
}

// extractWithPattern extracts the first capture group from value using re,
// or returns the raw value if re is nil.
func extractWithPattern(value string, re *regexp.Regexp) string {
	if re == nil {
		return value
	}
	matches := re.FindStringSubmatch(value)
	if len(matches) > 1 {
		return matches[1]
	}
	return value
}

// extractJSONField extracts a string value from a nested JSON map using a
// dot-separated path (e.g. "repository.full_name").
func extractJSONField(data map[string]interface{}, path string) string {
	parts := strings.SplitN(path, ".", 2)
	val, ok := data[parts[0]]
	if !ok {
		return ""
	}
	if len(parts) == 1 {
		if s, ok := val.(string); ok {
			return s
		}
		return ""
	}
	nested, ok := val.(map[string]interface{})
	if !ok {
		return ""
	}
	return extractJSONField(nested, parts[1])
}
