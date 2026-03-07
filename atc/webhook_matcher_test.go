package atc_test

import (
	"encoding/json"
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestWebhookMatcherBasicMatch(t *testing.T) {
	atc.LoadWebhookMatchers(map[string]map[string]atc.WebhookMatcher{
		"git": {
			"github": {
				SourceField:   "uri",
				SourcePattern: `github\.com/(.+?)(?:\.git)?$`,
				PayloadField:  "repository.full_name",
			},
		},
	})

	matcher, found := atc.FindWebhookMatcher("git", "github")
	if !found {
		t.Fatal("expected matcher to be found")
	}

	source := atc.Source{"uri": "https://github.com/example/repo.git"}
	payload := json.RawMessage(`{"repository":{"full_name":"example/repo"}}`)

	if !matcher.MatchResourceSource(source, payload) {
		t.Error("expected match for matching repo")
	}
}

func TestWebhookMatcherNoMatch(t *testing.T) {
	atc.LoadWebhookMatchers(map[string]map[string]atc.WebhookMatcher{
		"git": {
			"github": {
				SourceField:   "uri",
				SourcePattern: `github\.com/(.+?)(?:\.git)?$`,
				PayloadField:  "repository.full_name",
			},
		},
	})

	matcher, _ := atc.FindWebhookMatcher("git", "github")

	source := atc.Source{"uri": "https://github.com/example/repo.git"}
	payload := json.RawMessage(`{"repository":{"full_name":"other/repo"}}`)

	if matcher.MatchResourceSource(source, payload) {
		t.Error("expected no match for different repo")
	}
}

func TestWebhookMatcherGitLabProvider(t *testing.T) {
	atc.LoadWebhookMatchers(map[string]map[string]atc.WebhookMatcher{
		"git": {
			"gitlab": {
				SourceField:   "uri",
				SourcePattern: `gitlab\.com/(.+?)(?:\.git)?$`,
				PayloadField:  "project.path_with_namespace",
			},
		},
	})

	matcher, found := atc.FindWebhookMatcher("git", "gitlab")
	if !found {
		t.Fatal("expected gitlab matcher to be found")
	}

	source := atc.Source{"uri": "https://gitlab.com/group/project.git"}
	payload := json.RawMessage(`{"project":{"path_with_namespace":"group/project"}}`)

	if !matcher.MatchResourceSource(source, payload) {
		t.Error("expected match for gitlab repo")
	}
}

func TestWebhookMatcherWrongProvider(t *testing.T) {
	atc.LoadWebhookMatchers(map[string]map[string]atc.WebhookMatcher{
		"git": {
			"github": {
				SourceField:   "uri",
				SourcePattern: `github\.com/(.+?)(?:\.git)?$`,
				PayloadField:  "repository.full_name",
			},
		},
	})

	matcher, _ := atc.FindWebhookMatcher("git", "github")

	// GitLab URI should NOT match the GitHub pattern
	source := atc.Source{"uri": "https://gitlab.com/example/repo.git"}
	payload := json.RawMessage(`{"repository":{"full_name":"example/repo"}}`)

	if matcher.MatchResourceSource(source, payload) {
		t.Error("expected no match for gitlab URI against github pattern")
	}
}

func TestWebhookMatcherNoSourceField(t *testing.T) {
	atc.LoadWebhookMatchers(map[string]map[string]atc.WebhookMatcher{
		"git": {
			"github": {
				SourceField:   "uri",
				SourcePattern: `github\.com/(.+?)(?:\.git)?$`,
				PayloadField:  "repository.full_name",
			},
		},
	})

	matcher, _ := atc.FindWebhookMatcher("git", "github")

	// Resource source missing the "uri" field
	source := atc.Source{"url": "https://github.com/example/repo"}
	payload := json.RawMessage(`{"repository":{"full_name":"example/repo"}}`)

	if matcher.MatchResourceSource(source, payload) {
		t.Error("expected no match when source field is missing")
	}
}

func TestWebhookMatcherCaseInsensitive(t *testing.T) {
	atc.LoadWebhookMatchers(map[string]map[string]atc.WebhookMatcher{
		"git": {
			"github": {
				SourceField:   "uri",
				SourcePattern: `github\.com/(.+?)(?:\.git)?$`,
				PayloadField:  "repository.full_name",
			},
		},
	})

	matcher, _ := atc.FindWebhookMatcher("git", "github")

	source := atc.Source{"uri": "https://github.com/Example/Repo.git"}
	payload := json.RawMessage(`{"repository":{"full_name":"example/repo"}}`)

	if !matcher.MatchResourceSource(source, payload) {
		t.Error("expected case-insensitive match")
	}
}

func TestWebhookMatcherRegistryImage(t *testing.T) {
	atc.LoadWebhookMatchers(map[string]map[string]atc.WebhookMatcher{
		"registry-image": {
			"dockerhub": {
				SourceField:  "repository",
				PayloadField: "repository.repo_name",
			},
		},
	})

	matcher, found := atc.FindWebhookMatcher("registry-image", "dockerhub")
	if !found {
		t.Fatal("expected dockerhub matcher to be found")
	}

	source := atc.Source{"repository": "myorg/myimage"}
	payload := json.RawMessage(`{"repository":{"repo_name":"myorg/myimage"}}`)

	if !matcher.MatchResourceSource(source, payload) {
		t.Error("expected match for registry-image")
	}
}

func TestWebhookMatcherNotFound(t *testing.T) {
	atc.LoadWebhookMatchers(map[string]map[string]atc.WebhookMatcher{})

	_, found := atc.FindWebhookMatcher("git", "github")
	if found {
		t.Error("expected matcher not to be found")
	}
}

func TestExtractJSONFieldNested(t *testing.T) {
	payload := json.RawMessage(`{"a":{"b":{"c":"deep"}}}`)

	atc.LoadWebhookMatchers(map[string]map[string]atc.WebhookMatcher{
		"test": {
			"test": {
				SourceField:  "name",
				PayloadField: "a.b.c",
			},
		},
	})

	matcher, _ := atc.FindWebhookMatcher("test", "test")
	source := atc.Source{"name": "deep"}

	if !matcher.MatchResourceSource(source, payload) {
		t.Error("expected match for deeply nested field")
	}
}
