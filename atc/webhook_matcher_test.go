package atc_test

import (
	"encoding/json"
	"testing"

	"github.com/concourse/concourse/atc"
)

// mkMatcher is a test helper that builds a WebhookMatcher from a list of rules.
func mkMatcher(sigHeader string, rules ...atc.WebhookMatcherRule) atc.WebhookMatcher {
	return atc.WebhookMatcher{Rules: rules, SignatureHeader: sigHeader}
}

func uriRule(sourcePattern, payloadField string) atc.WebhookMatcherRule {
	return atc.WebhookMatcherRule{
		SourceField:   "uri",
		SourcePattern: sourcePattern,
		PayloadField:  payloadField,
	}
}

func branchRule() atc.WebhookMatcherRule {
	return atc.WebhookMatcherRule{
		SourceField:    "branch",
		PayloadField:   "ref",
		PayloadPattern: `refs/heads/(.+)`,
	}
}

func tagRule() atc.WebhookMatcherRule {
	return atc.WebhookMatcherRule{
		SourceField:     "tag_filter",
		PayloadField:    "ref",
		PayloadPattern:  `refs/tags/(.+)`,
		SourceIsPattern: true,
	}
}

const githubURIPattern = `github\.com/(.+?)(?:\.git)?$`

func loadGitHub(rules ...atc.WebhookMatcherRule) *atc.WebhookMatcher {
	atc.LoadWebhookMatchers(map[string]map[string]atc.WebhookMatcher{
		"git": {"github": mkMatcher("X-Hub-Signature-256", rules...)},
	})
	return atc.FindWebhookMatcher("git", "github")
}

// --- Basic URI matching ---------------------------------------------------

func TestWebhookMatcherBasicMatch(t *testing.T) {
	m := loadGitHub(uriRule(githubURIPattern, "repository.full_name"))
	source := atc.Source{"uri": "https://github.com/example/repo.git"}
	payload := json.RawMessage(`{"repository":{"full_name":"example/repo"}}`)
	if !m.MatchResourceSource(source, payload) {
		t.Error("expected match for matching repo")
	}
}

func TestWebhookMatcherNoMatch(t *testing.T) {
	m := loadGitHub(uriRule(githubURIPattern, "repository.full_name"))
	source := atc.Source{"uri": "https://github.com/example/repo.git"}
	payload := json.RawMessage(`{"repository":{"full_name":"other/repo"}}`)
	if m.MatchResourceSource(source, payload) {
		t.Error("expected no match for different repo")
	}
}

func TestWebhookMatcherCaseInsensitive(t *testing.T) {
	m := loadGitHub(uriRule(githubURIPattern, "repository.full_name"))
	source := atc.Source{"uri": "https://github.com/Example/Repo.git"}
	payload := json.RawMessage(`{"repository":{"full_name":"example/repo"}}`)
	if !m.MatchResourceSource(source, payload) {
		t.Error("expected case-insensitive match")
	}
}

// --- URI source missing / wrong field ------------------------------------

func TestWebhookMatcherNoSourceField(t *testing.T) {
	m := loadGitHub(uriRule(githubURIPattern, "repository.full_name"))
	// Source does NOT have "uri" — single rule is skipped → wildcard → triggers
	source := atc.Source{"url": "https://github.com/example/repo"}
	payload := json.RawMessage(`{"repository":{"full_name":"example/repo"}}`)
	if !m.MatchResourceSource(source, payload) {
		t.Error("expected wildcard match when source field is absent")
	}
}

func TestWebhookMatcherWrongProvider(t *testing.T) {
	m := loadGitHub(uriRule(githubURIPattern, "repository.full_name"))
	// GitLab URI won't match github pattern → empty extract → false
	source := atc.Source{"uri": "https://gitlab.com/example/repo.git"}
	payload := json.RawMessage(`{"repository":{"full_name":"example/repo"}}`)
	if m.MatchResourceSource(source, payload) {
		t.Error("expected no match for gitlab URI against github pattern")
	}
}

// --- Multi-rule AND: branch filtering ------------------------------------

func TestWebhookMatcherBranchMatch(t *testing.T) {
	m := loadGitHub(
		uriRule(githubURIPattern, "repository.full_name"),
		branchRule(),
	)
	source := atc.Source{
		"uri":    "https://github.com/example/repo.git",
		"branch": "main",
	}
	payload := json.RawMessage(`{"repository":{"full_name":"example/repo"},"ref":"refs/heads/main"}`)
	if !m.MatchResourceSource(source, payload) {
		t.Error("expected match: push to main, resource watches main")
	}
}

func TestWebhookMatcherBranchMismatch(t *testing.T) {
	m := loadGitHub(
		uriRule(githubURIPattern, "repository.full_name"),
		branchRule(),
	)
	source := atc.Source{
		"uri":    "https://github.com/example/repo.git",
		"branch": "main",
	}
	// Push to develop — should NOT trigger resource watching main
	payload := json.RawMessage(`{"repository":{"full_name":"example/repo"},"ref":"refs/heads/develop"}`)
	if m.MatchResourceSource(source, payload) {
		t.Error("expected no match: push to develop, resource watches main")
	}
}

// --- Multi-rule AND: tag_filter (source_is_pattern) ----------------------

func TestWebhookMatcherTagMatch(t *testing.T) {
	m := loadGitHub(
		uriRule(githubURIPattern, "repository.full_name"),
		tagRule(),
	)
	source := atc.Source{
		"uri":        "https://github.com/example/repo.git",
		"tag_filter": `v\d+\.\d+\.\d+`, // matches "v1.2.3"
	}
	payload := json.RawMessage(`{"repository":{"full_name":"example/repo"},"ref":"refs/tags/v1.2.3"}`)
	if !m.MatchResourceSource(source, payload) {
		t.Error("expected match: tag v1.2.3 matches tag_filter v\\d+\\.\\d+\\.\\d+")
	}
}

func TestWebhookMatcherTagMismatch(t *testing.T) {
	m := loadGitHub(
		uriRule(githubURIPattern, "repository.full_name"),
		tagRule(),
	)
	source := atc.Source{
		"uri":        "https://github.com/example/repo.git",
		"tag_filter": `v\d+\.\d+\.\d+`,
	}
	// Push to branch — ref is not a tag → refs/tags/ pattern won't match → no match
	payload := json.RawMessage(`{"repository":{"full_name":"example/repo"},"ref":"refs/heads/main"}`)
	if m.MatchResourceSource(source, payload) {
		t.Error("expected no match: branch push against a tag-only resource")
	}
}

// --- Skip-if-absent: resource without branch gets all branch pushes ------

func TestWebhookMatcherBranchAbsentIsWildcard(t *testing.T) {
	m := loadGitHub(
		uriRule(githubURIPattern, "repository.full_name"),
		branchRule(),
	)
	// Resource only has `uri`, no `branch` — branch rule is skipped
	source := atc.Source{"uri": "https://github.com/example/repo.git"}
	payload := json.RawMessage(`{"repository":{"full_name":"example/repo"},"ref":"refs/heads/develop"}`)
	if !m.MatchResourceSource(source, payload) {
		t.Error("expected wildcard match: resource has no branch constraint")
	}
}

// --- Both branch and tag_filter: payload_pattern skip enables coexistence --
//
// When a resource has both branch and tag_filter, rules whose payload_pattern
// doesn't match the event ref are skipped (not failed). This allows both
// branch pushes and tag pushes to trigger the resource independently.

func TestWebhookMatcherBranchAndTagFilterBranchPush(t *testing.T) {
	m := loadGitHub(
		uriRule(githubURIPattern, "repository.full_name"),
		branchRule(),
		tagRule(),
	)
	source := atc.Source{
		"uri":        "https://github.com/example/repo.git",
		"branch":     "main",
		"tag_filter": `v.*`,
	}
	payload := json.RawMessage(`{"repository":{"full_name":"example/repo"},"ref":"refs/heads/main"}`)
	if !m.MatchResourceSource(source, payload) {
		t.Error("expected match: push to main, tag rule skipped (payload_pattern refs/tags/ doesn't match)")
	}
}

func TestWebhookMatcherBranchAndTagFilterTagPush(t *testing.T) {
	m := loadGitHub(
		uriRule(githubURIPattern, "repository.full_name"),
		branchRule(),
		tagRule(),
	)
	source := atc.Source{
		"uri":        "https://github.com/example/repo.git",
		"branch":     "main",
		"tag_filter": `v.*`,
	}
	payload := json.RawMessage(`{"repository":{"full_name":"example/repo"},"ref":"refs/tags/v1.0.0"}`)
	if !m.MatchResourceSource(source, payload) {
		t.Error("expected match: tag v1.0.0 matches tag_filter, branch rule skipped (payload_pattern refs/heads/ doesn't match)")
	}
}

func TestWebhookMatcherBranchAndTagFilterWrongBranch(t *testing.T) {
	m := loadGitHub(
		uriRule(githubURIPattern, "repository.full_name"),
		branchRule(),
		tagRule(),
	)
	source := atc.Source{
		"uri":        "https://github.com/example/repo.git",
		"branch":     "main",
		"tag_filter": `v.*`,
	}
	payload := json.RawMessage(`{"repository":{"full_name":"example/repo"},"ref":"refs/heads/develop"}`)
	if m.MatchResourceSource(source, payload) {
		t.Error("expected no match: push to develop, resource watches main")
	}
}

func TestWebhookMatcherBranchAndTagFilterWrongTag(t *testing.T) {
	m := loadGitHub(
		uriRule(githubURIPattern, "repository.full_name"),
		branchRule(),
		tagRule(),
	)
	source := atc.Source{
		"uri":        "https://github.com/example/repo.git",
		"branch":     "main",
		"tag_filter": `v.*`,
	}
	payload := json.RawMessage(`{"repository":{"full_name":"example/repo"},"ref":"refs/tags/release-1.0"}`)
	if m.MatchResourceSource(source, payload) {
		t.Error("expected no match: tag release-1.0 doesn't match tag_filter v.*")
	}
}

// --- GitLab ---------------------------------------------------------------

func TestWebhookMatcherGitLabProvider(t *testing.T) {
	atc.LoadWebhookMatchers(map[string]map[string]atc.WebhookMatcher{
		"git": {"gitlab": mkMatcher("X-Gitlab-Token",
			atc.WebhookMatcherRule{
				SourceField:   "uri",
				SourcePattern: `gitlab\.com/(.+?)(?:\.git)?$`,
				PayloadField:  "project.path_with_namespace",
			},
		)},
	})
	m := atc.FindWebhookMatcher("git", "gitlab")
	source := atc.Source{"uri": "https://gitlab.com/group/project.git"}
	payload := json.RawMessage(`{"project":{"path_with_namespace":"group/project"}}`)
	if !m.MatchResourceSource(source, payload) {
		t.Error("expected match for gitlab repo")
	}
}

// --- registry-image -------------------------------------------------------

func TestWebhookMatcherRegistryImage(t *testing.T) {
	atc.LoadWebhookMatchers(map[string]map[string]atc.WebhookMatcher{
		"registry-image": {"dockerhub": mkMatcher("",
			atc.WebhookMatcherRule{
				SourceField:  "repository",
				PayloadField: "repository.repo_name",
			},
		)},
	})
	m := atc.FindWebhookMatcher("registry-image", "dockerhub")
	source := atc.Source{"repository": "myorg/myimage"}
	payload := json.RawMessage(`{"repository":{"repo_name":"myorg/myimage"}}`)
	if !m.MatchResourceSource(source, payload) {
		t.Error("expected match for registry-image")
	}
}

// --- Not found -----------------------------------------------------------

func TestWebhookMatcherNotFound(t *testing.T) {
	atc.LoadWebhookMatchers(map[string]map[string]atc.WebhookMatcher{})
	m := atc.FindWebhookMatcher("git", "github")
	if m != nil {
		t.Error("expected nil matcher when not configured")
	}
}

// --- Deeply nested JSON payload ------------------------------------------

func TestExtractJSONFieldNested(t *testing.T) {
	atc.LoadWebhookMatchers(map[string]map[string]atc.WebhookMatcher{
		"test": {"test": mkMatcher("",
			atc.WebhookMatcherRule{SourceField: "name", PayloadField: "a.b.c"},
		)},
	})
	m := atc.FindWebhookMatcher("test", "test")
	source := atc.Source{"name": "deep"}
	payload := json.RawMessage(`{"a":{"b":{"c":"deep"}}}`)
	if !m.MatchResourceSource(source, payload) {
		t.Error("expected match for deeply nested field")
	}
}
