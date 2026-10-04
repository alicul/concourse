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

func (p *concourseParser) Validate(_ *kong.Application) error { return nil }

func (p *concourseParser) loadConfig(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("read configuration: %w", err)
	}
	defer f.Close()
	decoder := yaml.NewDecoder(f)
	if err := decoder.Decode(&p.config); err != nil {
		return fmt.Errorf("configuration %s: %w", filename, err)
	}
	if p.config == nil {
		return errors.New("configuration must contain a mapping of command names to flags")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("configuration must contain exactly one YAML document")
	}
	var invalid []string
	for command, values := range p.config {
		binding, ok := p.commands[command]
		if !ok || values == nil {
			invalid = append(invalid, command)
			continue
		}
		known := map[string]bool{}
		for _, option := range binding.options {
			known[option.option.LongNameWithNamespace()] = true
		}
		for name, value := range values {
			if !known[name] || value == nil {
				invalid = append(invalid, command+"."+name)
			}
		}
	}
	if len(invalid) > 0 {
		sort.Strings(invalid)
		return fmt.Errorf("unknown or null configuration keys: %s", strings.Join(invalid, ", "))
	}
	return nil
}

// CLI values have already been selected by Kong and skip resolution. For the
// remaining flags select exactly one source, including empty/false/zero values.
// Only the selected command is resolved, so another command's secrets and key
// paths are never decoded as a side effect of starting a worker or web node.
func (p *concourseParser) Resolve(_ *kong.Context, parent *kong.Path, flag *kong.Flag) (any, error) {
	binding := p.options[flag]
	if binding == nil {
		return nil, nil
	}
	option := binding.option
	if value, ok := os.LookupEnv(option.EnvDefaultKey); ok {
		if option.EnvDefaultDelim != "" {
			parts := strings.Split(value, option.EnvDefaultDelim)
			out := make([]any, len(parts))
			for i, part := range parts {
				out[i] = part
			}
			return out, nil
		}
		return value, nil
	}
	if value, ok := p.config[parent.Node().Name][flag.Name]; ok {
		return value, nil
	}
	if len(option.Default) > 0 {
		kind := option.Field().Type.Kind()
		if kind != reflect.Slice && kind != reflect.Map {
			return option.Default[len(option.Default)-1], nil
		}
		out := make([]any, len(option.Default))
		for i, value := range option.Default {
			out[i] = value
		}
		return out, nil
	}
	return nil, nil
}
