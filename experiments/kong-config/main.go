package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"go.yaml.in/yaml/v3"
)

var errHelp = errors.New("help requested")

func newParser(stdout, stderr io.Writer, options ...kong.Option) (*CLI, *kong.Kong, error) {
	source := &sources{}
	cli := &CLI{sources: source}
	cli.Web.vault = &vaultPlugin{}
	cli.Web.Plugins = kong.Plugins{cli.Web.vault}
	opts := []kong.Option{
		kong.Name("concourse-kong"),
		kong.Description("Concourse configuration prototype. Prints redacted configuration; does not start services."),
		kong.Writers(stdout, stderr),
		kong.DefaultEnvars("CONCOURSE"),
		kong.Resolvers(source),
		kong.ValueFormatter(helpValue),
		kong.Help(func(options kong.HelpOptions, ctx *kong.Context) error {
			if err := kong.DefaultHelpPrinter(options, ctx); err != nil {
				return err
			}
			return errHelp // Keep help testable without exiting the host process.
		}),
	}
	opts = append(opts, options...)
	parser, err := kong.New(cli, opts...)
	if err == nil {
		err = source.prepare(parser)
	}
	return cli, parser, err
}

func run(args []string, stdout, stderr io.Writer) int {
	cli, parser, err := newParser(stdout, stderr)
	if err == nil {
		var ctx *kong.Context
		ctx, err = parser.Parse(args)
		if errors.Is(err, errHelp) {
			return 0
		}
		if err == nil {
			cli.sources.clearConsumedEnv()
			err = printConfig(ctx, stdout)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	return 0
}

func printConfig(ctx *kong.Context, w io.Writer) error {
	values := map[string]any{}
	for _, flag := range ctx.Selected().Flags {
		value := flag.Target.Interface()
		switch flag.Name {
		case "postgres-password", "vault-client-token", "vault-auth-param":
			value = "<redacted>"
		default:
			if duration, ok := value.(time.Duration); ok {
				value = duration.String()
			}
		}
		values[flag.Name] = value
	}
	encoder := yaml.NewEncoder(w)
	encoder.SetIndent(2)
	return encoder.Encode(map[string]any{strings.TrimSpace(ctx.Command()): values})
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
