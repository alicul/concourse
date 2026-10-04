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
	"go.yaml.in/yaml/v3"
)

// Flag names are the configuration schema; there is no parallel YAML struct
// or legacy struct-tag interpreter. Each command is a top-level YAML section.
type sources struct {
	config map[string]map[string]any
	model  *kong.Application
}

// Kong normally parses environment variables during Reset, before resolvers.
// Keep their metadata for help but let Resolve own their values. This makes
// CLI > environment > YAML > default explicit and avoids parsing an invalid
// environment value when a CLI argument already overrides it.
func (s *sources) prepare(k *kong.Kong) error {
	s.model = k.Model
	return kong.Visit(k.Model, func(node kong.Visitable, next kong.Next) error {
		if flag, ok := node.(*kong.Flag); ok {
			flag.Tag.Envs = nil
		}
		return next(nil)
	})
}

func (c *CLI) BeforeResolve(ctx *kong.Context) error {
	c.sources.config = nil
	var filename string
	for _, flag := range ctx.Model.Flags {
		if flag.Name != "config" {
			continue
		}
		filename = os.Getenv("CONCOURSE_CONFIG")
		for _, path := range ctx.Path {
			if path.Flag == flag {
				filename = ctx.FlagValue(flag).(string)
				break
			}
		}
	}
	if filename == "" {
		return nil
	}
	f, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("read configuration: %w", err)
	}
	defer f.Close()
	if err := c.sources.load(f); err != nil {
		return fmt.Errorf("configuration %s: %w", filename, err)
	}
	return nil
}

func (s *sources) load(r io.Reader) error {
	decoder := yaml.NewDecoder(r)
	if err := decoder.Decode(&s.config); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("expected one YAML document")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return s.Validate(s.model)
}

func (s *sources) Validate(app *kong.Application) error {
	known := map[string]map[string]bool{}
	for _, cmd := range app.Children {
		known[cmd.Name] = map[string]bool{}
		for _, flag := range cmd.Flags {
			known[cmd.Name][flag.Name] = true
		}
	}
	var invalid []string
	for command, values := range s.config {
		flags, ok := known[command]
		if !ok || values == nil {
			invalid = append(invalid, command)
			continue
		}
		for name, value := range values {
			if !flags[name] {
				invalid = append(invalid, command+"."+name)
			} else if value == nil {
				invalid = append(invalid, command+"."+name+" (null is not supported; use an explicit empty value)")
			}
		}
	}
	if len(invalid) > 0 {
		sort.Strings(invalid)
		return fmt.Errorf("unknown or invalid configuration keys: %s", strings.Join(invalid, ", "))
	}
	return nil
}

func (s *sources) Resolve(_ *kong.Context, parent *kong.Path, flag *kong.Flag) (any, error) {
	for _, name := range flag.Envs {
		value, exists := os.LookupEnv(name)
		if !exists {
			continue
		}
		switch flag.Target.Kind() {
		case reflect.Bool:
			if value == "" {
				return true, nil // go-flags treats an explicitly empty boolean env as true.
			}
		case reflect.Slice:
			parts := strings.Split(value, ",")
			values := make([]any, len(parts))
			for i, part := range parts {
				values[i] = part
			}
			return values, nil
		case reflect.Map:
			values := map[string]any{}
			for _, pair := range strings.Split(value, ",") {
				key, val, _ := strings.Cut(pair, ":")
				values[key] = val
			}
			return values, nil
		}
		return value, nil
	}
	return s.config[parent.Node().Name][flag.Name], nil
}

func helpValue(value *kong.Value) string {
	text := kong.DefaultHelpValueFormatter(value)
	if value.Flag != nil && len(value.Flag.Envs) > 0 {
		text += " ($" + strings.Join(value.Flag.Envs, ", $") + ")"
	}
	return text
}

// Called only after successful parsing. Explicit proxy variables and unknown
// CONCOURSE_GARDEN_* variables survive, as they must for worker subprocesses.
func (s *sources) clearConsumedEnv() {
	_ = kong.Visit(s.model, func(node kong.Visitable, next kong.Next) error {
		if flag, ok := node.(*kong.Flag); ok {
			for _, name := range flag.Envs {
				if strings.HasPrefix(name, "CONCOURSE_") {
					_ = os.Unsetenv(name)
				}
			}
		}
		return next(nil)
	})
}
