package concourse

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
)

func (team *team) SetWebhook(webhookName, webhookType, secret string) (atc.Webhook, error) {
	body := struct {
		Type   string `json:"type"`
		Secret string `json:"secret,omitempty"`
	}{
		Type:   webhookType,
		Secret: secret,
	}

	buf := new(bytes.Buffer)
	err := json.NewEncoder(buf).Encode(body)
	if err != nil {
		return atc.Webhook{}, err
	}

	var webhook atc.Webhook
	err = team.connection.Send(internal.Request{
		RequestName: atc.SetWebhook,
		Params: map[string]string{
			"team_name":    team.Name(),
			"webhook_name": webhookName,
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
