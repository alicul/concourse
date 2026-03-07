package commands

import (
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/fly/rc"
)

type SetWebhookCommand struct {
	Webhook string `long:"webhook" required:"true" description:"Name of the webhook"`
	Type    string `long:"type" required:"true" description:"Type of the webhook (e.g. github, gitlab)"`
	Secret  string `long:"secret" description:"Optional HMAC secret for signature validation (e.g. GitHub webhook secret)"`
}

func (command *SetWebhookCommand) Execute(args []string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}

	err = target.Validate()
	if err != nil {
		return err
	}

	team := target.Team()

	webhook, err := team.SetWebhook(command.Webhook, command.Type, command.Secret)
	if err != nil {
		return err
	}

	fmt.Println("webhook saved")
	fmt.Println()
	fmt.Println("configure your external service with the following webhook URL:")
	fmt.Println()
	fmt.Println(webhook.URL)

	if command.Secret != "" {
		fmt.Println()
		fmt.Println("HMAC signature validation is enabled for this webhook.")
		fmt.Println("Configure the same secret in your external service (e.g. GitHub webhook settings).")
	}

	webhookJSON, _ := json.MarshalIndent(webhook, "", "  ")
	fmt.Printf("\nwebhook details:\n%s\n", string(webhookJSON))

	return nil
}
