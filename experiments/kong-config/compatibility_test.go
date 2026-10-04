package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Read the actual production declarations without importing the entire server
// dependency graph. This catches accidental renaming/default drift in the port
// and also alerts us when production changes after the prototype is written.
func TestFlagSurfaceMatchesProduction(t *testing.T) {
	cleanEnvironment(t)
	_, parser, err := newParser(io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(here), "..", "..")
	type origin struct{ file, structure, prefix string }
	sources := map[string][]origin{
		"web": {
			{"atc/atccmd/command.go", "RunCommand", ""},
			{"flag/postgres_config.go", "PostgresConfig", "postgres-"},
			{"atc/creds/vault/manager.go", "VaultManager", "vault-"},
			{"atc/creds/vault/manager.go", "TLSConfig", "vault-"},
			{"atc/creds/vault/manager.go", "AuthConfig", "vault-"},
		},
		"worker": {
			{"worker/workercmd/worker.go", "WorkerCommand", ""},
			{"worker/workercmd/worker_config.go", "WorkerConfig", ""},
			{"worker/tsa_config.go", "TSAConfig", "tsa-"},
		},
	}
	for _, command := range parser.Model.Children {
		expected := map[string]reflect.StructTag{}
		for _, source := range sources[command.Name] {
			file, err := parserGoFile(filepath.Join(root, source.file))
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(file, func(node ast.Node) bool {
				typeSpec, ok := node.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != source.structure {
					return true
				}
				structure := typeSpec.Type.(*ast.StructType)
				for _, field := range structure.Fields.List {
					if field.Tag == nil {
						continue
					}
					text, err := strconv.Unquote(field.Tag.Value)
					if err != nil {
						t.Fatal(err)
					}
					tag := reflect.StructTag(text)
					if name := tag.Get("long"); name != "" {
						expected[source.prefix+name] = tag
					}
				}
				return false
			})
		}
		for _, flag := range command.Flags {
			tag, found := expected[flag.Name]
			if !found {
				t.Errorf("%s --%s has no production counterpart", command.Name, flag.Name)
				continue
			}
			if flag.Default != tag.Get("default") {
				t.Errorf("%s --%s default=%q, production=%q", command.Name, flag.Name, flag.Default, tag.Get("default"))
			}
			if flag.Required != (tag.Get("required") == "true") {
				t.Errorf("%s --%s requiredness differs", command.Name, flag.Name)
			}
			env := tag.Get("env")
			if env == "" {
				env = "CONCOURSE_" + strings.ToUpper(strings.ReplaceAll(flag.Name, "-", "_"))
			}
			if !reflect.DeepEqual(flag.Envs, []string{env}) {
				t.Errorf("%s --%s env=%v, production=%s", command.Name, flag.Name, flag.Envs, env)
			}
		}
	}
}

func parserGoFile(path string) (*ast.File, error) {
	return parser.ParseFile(token.NewFileSet(), path, nil, 0)
}
