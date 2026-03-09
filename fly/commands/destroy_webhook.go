package commands

import (
	"errors"
	"fmt"

	"github.com/concourse/concourse/fly/commands/internal/interaction"
	"github.com/concourse/concourse/fly/rc"
)

type DestroyWebhookCommand struct {
	Webhook         string `long:"webhook" required:"true" description:"Name of the webhook to destroy"`
	SkipInteractive bool   `long:"non-interactive" description:"Force destroy without confirmation"`
}

func (command *DestroyWebhookCommand) Execute(args []string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}

	err = target.Validate()
	if err != nil {
		return err
	}

	team := target.Team()

	fmt.Printf("!!! this will remove the webhook `%s`\n\n", command.Webhook)

	if !command.SkipInteractive {
		confirm, err := interaction.Input("please type the webhook name to confirm", false)
		if err != nil {
			return err
		}

		if confirm != command.Webhook {
			return errors.New("incorrect webhook name; bailing out")
		}
	}

	err = team.DestroyWebhook(command.Webhook)
	if err != nil {
		return err
	}

	fmt.Println("webhook destroyed")
	return nil
}
