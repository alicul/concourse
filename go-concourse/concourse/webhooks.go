package concourse

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
)

// WebhookConfig holds all fields for creating/updating a webhook.
// Rules are the multi-field matcher configuration (1:1 with CONCOURSE_WEBHOOK_MATCHERS YAML schema).
type WebhookConfig struct {
	Name            string
	Type            string
	Secret          string
	Rules           []atc.WebhookMatcherRule
	SignatureHeader string
	// SignatureAlgo specifies how to validate the signature when a secret is set.
	// "hmac-sha256" (default) — GitHub, Bitbucket: validates "sha256=<hex>" in the signature header.
	// "plain"                 — GitLab: compares the header value directly to the secret (constant-time).
	SignatureAlgo string
}

func (team *team) SetWebhook(cfg WebhookConfig) (atc.Webhook, error) {
	buf := new(bytes.Buffer)
	err := json.NewEncoder(buf).Encode(atc.Webhook{
		Name:            cfg.Name,
		Type:            cfg.Type,
		Secret:          cfg.Secret,
		Rules:           cfg.Rules,
		SignatureHeader: cfg.SignatureHeader,
		SignatureAlgo:   cfg.SignatureAlgo,
	})
	if err != nil {
		return atc.Webhook{}, err
	}

	var webhook atc.Webhook
	err = team.connection.Send(internal.Request{
		RequestName: atc.SetWebhook,
		Params: map[string]string{
			"team_name":    team.Name(),
			"webhook_name": cfg.Name,
		},
		Header: http.Header{
			"Content-Type": {"application/json"},
		},
		Body: buf,
	}, &internal.Response{
		Result: &webhook,
	})
	if err != nil {
		return atc.Webhook{}, err
	}

	return webhook, nil
}

func (team *team) DestroyWebhook(webhookName string) error {
	return team.connection.Send(internal.Request{
		RequestName: atc.DestroyWebhook,
		Params: map[string]string{
			"team_name":    team.Name(),
			"webhook_name": webhookName,
		},
	}, &internal.Response{})
}

func (team *team) ListWebhooks() ([]atc.Webhook, error) {
	var webhooks []atc.Webhook
	err := team.connection.Send(internal.Request{
		RequestName: atc.ListWebhooks,
		Params: map[string]string{
			"team_name": team.Name(),
		},
	}, &internal.Response{
		Result: &webhooks,
	})
	if err != nil {
		return nil, err
	}

	return webhooks, nil
}
