package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/concourse/concourse/v8"
	"github.com/jessevdk/go-flags"
	"github.com/vito/twentythousandtonnesofcrudeoil"
)

var errKongHelp = errors.New("help requested")
var errKongVersion = errors.New("version requested")

type concourseCLI struct {
	Config  string `help:"Read a YAML configuration file ($CONCOURSE_CONFIG)." placeholder:"PATH"`
	Version bool   `short:"v" help:"Print the version of Concourse and exit."`
	parser  *concourseParser
}

func (cli *concourseCLI) BeforeReset(ctx *kong.Context) error {
	for _, flag := range ctx.Model.Flags {
		if flag.Name == "version" && ctx.FlagValue(flag) == true {
			return errKongVersion
		}
	}
	return nil
}

func (cli *concourseCLI) BeforeResolve(ctx *kong.Context) error {
	filename := os.Getenv("CONCOURSE_CONFIG")
	for _, path := range ctx.Path {
		if path.Flag != nil && path.Flag.Name == "config" && path.Flag.Target.Type().Kind() == reflect.String {
			filename = ctx.FlagValue(path.Flag).(string)
			break
		}
	}
	if filename == "" {
		return nil
	}
	return cli.parser.loadConfig(filename)
}

type optionBinding struct {
	option *flags.Option
	group  string
	target reflect.Value
}

type commandBinding struct {
	execute flags.Commander
	model   reflect.Value
	options []*optionBinding
}

type concourseParser struct {
	kong     *kong.Kong
	schema   *flags.Parser
	commands map[string]*commandBinding
	options  map[*kong.Flag]*optionBinding
	config   map[string]map[string]any
	stdout   io.Writer
}

// The go-flags parser is used only to read Concourse's existing declarations
// and dynamic credential/authentication registrations. Kong owns argument
// parsing, required flags, resolution and help; no legacy Parse call is made.
// Option.Set retains the existing custom decoders (keys, ciphers, paths, URLs).
// This adapter allows a runnable MVP without rewriting every shared flag type.
func newConcourseParser(cmd *ConcourseCommand, stdout io.Writer) (*concourseParser, error) {
	schema := flags.NewParser(cmd, flags.None)
	schema.NamespaceDelimiter = "-"
	cmd.LessenRequirements(schema)
	cmd.Web.WireDynamicFlags(schema.Find("web"))
	cmd.Quickstart.WebCommand.WireDynamicFlags(schema.Find("quickstart"))
	twentythousandtonnesofcrudeoil.TheEnvironmentIsPerfectlySafe(schema, "CONCOURSE_")
	p := &concourseParser{
		schema: schema, stdout: stdout,
		commands: map[string]*commandBinding{}, options: map[*kong.Flag]*optionBinding{},
	}
	executors := map[string]flags.Commander{
		"web": &cmd.Web, "worker": &cmd.Worker, "migrate": &cmd.Migrate,
		"quickstart": &cmd.Quickstart, "land-worker": &cmd.LandWorker,
		"retire-worker": &cmd.RetireWorker, "generate-key": &cmd.GenerateKey,
	}
	opts := []kong.Option{
		kong.Name("concourse"), kong.Description("Concourse CI."), kong.Writers(stdout, stdout),
		kong.Resolvers(p), kong.HelpOptions{NoExpandSubcommands: true},
		kong.Help(func(options kong.HelpOptions, ctx *kong.Context) error {
			if err := concourseHelp(options, ctx); err != nil {
				return err
			}
			return errKongHelp // Return before Kong's help hook can call os.Exit.
		}),
	}
	for _, command := range schema.Commands() {
		binding := &commandBinding{execute: executors[command.Name]}
		if binding.execute == nil {
			return nil, fmt.Errorf("no executor for %s", command.Name)
		}
		collectOptions(command.Group, "", false, &binding.options)
		fields := []reflect.StructField{}
		for i, option := range binding.options {
			o := option.option
			if option.group == command.ShortDescription {
				option.group = "General"
			}
			help := o.Description + " ($" + o.EnvDefaultKey + ")."
			if len(o.Default) > 0 {
				value := strings.Join(o.Default, ", ")
				if o.DefaultMask != "" {
					value = o.DefaultMask
				}
				help += " Default: " + value + "."
			}
			if len(o.Choices) > 0 {
				help += " Choices: " + strings.Join(o.Choices, ", ") + "."
			}
			tag := fmt.Sprintf("name:%q help:%q group:%q", o.LongNameWithNamespace(), help, option.group)
			if o.Required {
				tag += ` required:""`
			}
			if o.Hidden {
				tag += ` hidden:""`
			}
			if o.ShortName != 0 {
				tag += fmt.Sprintf(" short:%q", string(o.ShortName))
			}
			if o.ValueName != "" {
				tag += fmt.Sprintf(" placeholder:%q", o.ValueName)
			} else {
				tag += ` placeholder:"VALUE"`
			}
			fields = append(fields, reflect.StructField{Name: fmt.Sprintf("Option%d", i), Type: reflect.TypeOf(pendingValue{}), Tag: reflect.StructTag(tag)})
		}
		fields = append(fields, reflect.StructField{Name: "Args", Type: reflect.TypeOf([]string{}), Tag: `arg:"" optional:"" help:"Arguments passed to the command."`})
		binding.model = reflect.New(reflect.StructOf(fields))
		for i, option := range binding.options {
			option.target = binding.model.Elem().Field(i)
			opts = append(opts, kong.ValueMapper(option.target.Addr().Interface(), optionMapper{option.option}))
		}
		p.commands[command.Name] = binding
		opts = append(opts, kong.DynamicCommand(command.Name, command.ShortDescription, "", binding.model.Interface()))
	}
	var err error
	p.kong, err = kong.New(&concourseCLI{parser: p}, opts...)
	if err != nil {
		return nil, err
	}
	for _, command := range p.kong.Model.Children {
		for _, flag := range command.Flags {
			for _, option := range p.commands[command.Name].options {
				if flag.Name == option.option.LongNameWithNamespace() {
					p.options[flag] = option
					break
				}
			}
		}
	}
	return p, nil
}

func collectOptions(group *flags.Group, name string, hidden bool, options *[]*optionBinding) {
	if group.ShortDescription != "" {
		name = group.ShortDescription
	}
	hidden = hidden || group.Hidden
	for _, option := range group.Options() {
		option.Hidden = option.Hidden || hidden
		*options = append(*options, &optionBinding{option: option, group: name})
	}
	for _, child := range group.Groups() {
		collectOptions(child, name, hidden, options)
	}
}

func (p *concourseParser) parse(args []string) (flags.Commander, []string, error) {
	// Also support the conventional "concourse help [command]" spelling.
	if len(args) > 0 && args[0] == "help" {
		args = append(append([]string{}, args[1:]...), "--help")
	}
	p.config = nil
	ctx, err := p.kong.Parse(args)
	if errors.Is(err, errKongHelp) {
		return nil, nil, nil
	}
	if errors.Is(err, errKongVersion) {
		_, err = fmt.Fprintln(p.stdout, concourse.Version)
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, err
	}
	var selected *commandBinding
	for _, path := range ctx.Path {
		if path.Command != nil {
			selected = p.commands[path.Command.Name]
		}
	}
	if selected == nil {
		return nil, nil, errors.New("expected a command")
	}
	for _, binding := range selected.options {
		for _, value := range binding.target.Interface().(pendingValue).Values {
			if err := binding.option.Set(&value); err != nil {
				return nil, nil, fmt.Errorf("--%s: %w", binding.option.LongNameWithNamespace(), err)
			}
		}
	}
	remaining := selected.model.Elem().FieldByName("Args").Interface().([]string)
	return selected.execute, remaining, nil
}

// Values are kept as tokens until parsing succeeds. Help/version therefore do
// not read private-key files, connect services, or change Concourse's settings.
type pendingValue struct{ Values []string }

type optionMapper struct{ option *flags.Option }

func (m optionMapper) IsBool() bool { return m.option.Field().Type.Kind() == reflect.Bool }

func (m optionMapper) Decode(ctx *kong.DecodeContext, target reflect.Value) error {
	var input any
	if m.IsBool() && ctx.Scan.Peek().Type != kong.FlagValueToken {
		input = true
	} else {
		kind := m.option.Field().Type.Kind()
		if kind >= reflect.Int && kind <= reflect.Float64 {
			ctx.Scan.AllowHyphenPrefixedParameters(true)
		}
		token, err := ctx.Scan.PopValue("value")
		if err != nil {
			return err
		}
		input = token.Value
	}
	values, err := m.strings(input)
	if err != nil {
		return err
	}
	old := target.Interface().(pendingValue).Values
	kind := m.option.Field().Type.Kind()
	if kind == reflect.Map || kind == reflect.Slice {
		values = append(old, values...)
	}
	target.Set(reflect.ValueOf(pendingValue{Values: values}))
	return nil
}

func (m optionMapper) strings(value any) ([]string, error) {
	switch value := value.(type) {
	case []any:
		kind := m.option.Field().Type.Kind()
		if kind != reflect.Slice && kind != reflect.Map {
			return nil, errors.New("expected a scalar value")
		}
		out := []string{}
		for _, item := range value {
			text, err := scalarText(item)
			if err != nil {
				return nil, err
			}
			out = append(out, text)
		}
		return out, nil
	case map[string]any:
		if m.option.Field().Type.Kind() != reflect.Map {
			return nil, errors.New("expected a scalar or list value")
		}
		keys := []string{}
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := []string{}
		for _, key := range keys {
			text, err := scalarText(value[key])
			if err != nil {
				return nil, err
			}
			out = append(out, key+":"+text)
		}
		return out, nil
	default:
		text, err := scalarText(value)
		return []string{text}, err
	}
}

func scalarText(value any) (string, error) {
	switch value := value.(type) {
	case string:
		return value, nil
	case bool, int, int64, uint64, float64:
		return fmt.Sprint(value), nil
	default:
		return "", errors.New("expected a string, boolean or number")
	}
}
