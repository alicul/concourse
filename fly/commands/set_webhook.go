package commands

import (
	"fmt"
	"os"

	"sigs.k8s.io/yaml"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/go-concourse/concourse"
)

// SetWebhookCommand is the fly set-webhook command.
type SetWebhookCommand struct {
	WebhookName     string `short:"w" long:"webhook" required:"true" description:"Name of the webhook"`
	Type            string `short:"t" long:"type" required:"true" description:"Webhook type (e.g. github, gitlab)"`
	Secret          string `long:"secret" description:"HMAC secret. When set, ?token= is omitted from the URL and HMAC is the sole authentication mechanism."`
	SignatureHeader string `long:"signature-header" description:"HTTP header carrying the signature (e.g. X-Hub-Signature-256). Used as a hint alongside signature-algo."`
	SignatureAlgo   string `long:"signature-algo" description:"Signature validation algorithm: 'hmac-sha256' (default, GitHub/Bitbucket) or 'plain' (GitLab — header value compared directly with secret)."`
	RulesFile       string `long:"rules-file" description:"Path to YAML file defining matcher rules (list of source_field/payload_field pairs). See CONCOURSE_WEBHOOK_MATCHERS docs."`
}

func (cmd *SetWebhookCommand) Execute(args []string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	var rules []atc.WebhookMatcherRule
	if cmd.RulesFile != "" {
		data, err := os.ReadFile(cmd.RulesFile)
		if err != nil {
			return fmt.Errorf("reading rules file: %w", err)
		}
		if err := yaml.Unmarshal(data, &rules); err != nil {
			return fmt.Errorf("parsing rules file: %w", err)
		}
	}

	webhook, err := target.Team().SetWebhook(concourse.WebhookConfig{
		Name:            cmd.WebhookName,
		Type:            cmd.Type,
		Secret:          cmd.Secret,
		Rules:           rules,
		SignatureHeader: cmd.SignatureHeader,
		SignatureAlgo:   cmd.SignatureAlgo,
	})
	if err != nil {
		return err
	}

	fmt.Printf("webhook saved\n\n")
	if webhook.URL != "" {
		fmt.Printf("configure your external service with the following webhook URL:\n\n  %s\n\n", webhook.URL)
	}
	if cmd.Secret != "" {
		fmt.Printf("HMAC signature validation is enabled.\n")
		fmt.Printf("Configure the same secret in your external service.\n")
	} else {
		fmt.Printf("[!] No HMAC secret configured. Authentication is via ?token= in the URL only.\n")
		fmt.Printf("    Consider using --secret for production deployments.\n")
	}

	if len(webhook.Rules) > 0 {
		fmt.Printf("\nconfigured matcher rules (%d):\n", len(webhook.Rules))
		for i, r := range webhook.Rules {
			fmt.Printf("  rule %d: %s → %s", i+1, r.SourceField, r.PayloadField)
			if r.SourceIsPattern {
				fmt.Printf(" (source treated as pattern)")
			}
			fmt.Printf("\n")
		}
	}

	return nil
}
