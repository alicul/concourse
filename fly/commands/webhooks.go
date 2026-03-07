package commands

import (
	"fmt"
	"os"

	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/fatih/color"
)

type WebhooksCommand struct{}

func (command *WebhooksCommand) Execute(args []string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}

	err = target.Validate()
	if err != nil {
		return err
	}

	team := target.Team()

	webhooks, err := team.ListWebhooks()
	if err != nil {
		return err
	}

	table := ui.Table{
		Headers: ui.TableRow{
			{Contents: "name", Color: color.New(color.Bold)},
			{Contents: "type", Color: color.New(color.Bold)},
		},
	}

	for _, webhook := range webhooks {
		table.Data = append(table.Data, []ui.TableCell{
			{Contents: webhook.Name},
			{Contents: webhook.Type},
		})
	}

	if len(webhooks) == 0 {
		fmt.Println("no webhooks configured")
		return nil
	}

	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}
