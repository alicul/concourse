package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/v8"
	"github.com/jessevdk/go-flags"
)

func kongTestParser(t *testing.T) (*ConcourseCommand, *concourseParser, *bytes.Buffer) {
	t.Helper()
	cmd := &ConcourseCommand{}
	stdout := &bytes.Buffer{}
	p, err := newConcourseParser(cmd, stdout)
	if err != nil {
		t.Fatal(err)
	}
	return cmd, p, stdout
}

func kongTestConfig(t *testing.T, body string) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "concourse.yaml")
	if err := os.WriteFile(filename, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func kongTestKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "worker-key")
	data := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(filename, data, 0600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestKongConcourseHelp(t *testing.T) {
	t.Setenv("CONCOURSE_CONFIG", "/missing/concourse.yaml")
	t.Setenv("CONCOURSE_POSTGRES_PORT", "invalid")
	t.Setenv("CONCOURSE_TSA_HOST_KEY", "/missing/secret-key")
	for _, width := range []string{"40", "60", "80", "120"} {
		t.Run(width, func(t *testing.T) {
			t.Setenv("COLUMNS", width)
			for command, flag := range map[string]string{
				"web": "--vault-url", "worker": "--tsa-worker-private-key",
				"quickstart": "--worker-tsa-worker-private-key", "migrate": "--postgres-host",
				"land-worker": "--name", "retire-worker": "--name", "generate-key": "--filename",
			} {
				cmd, p, stdout := kongTestParser(t)
				execute, _, err := p.parse([]string{command, "--help"})
				if err != nil || execute != nil || !strings.Contains(stdout.String(), flag) {
					t.Fatalf("%s help: execute=%T err=%v output=%s", command, execute, err, stdout.String())
				}
				if cmd.Web.TSACommand.HostKey != nil || cmd.Web.Postgres.Port != 0 {
					t.Fatal("help decoded runtime settings")
				}
				if width == "40" && command == "worker" && !strings.Contains(stdout.String(), "    The name to set for the worker") {
					t.Fatal("narrow help squeezed descriptions into an unreadable column")
				}
			}
		})
	}
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}, {"help", "web"}} {
		_, p, stdout := kongTestParser(t)
		if execute, _, err := p.parse(args); err != nil || execute != nil || !strings.Contains(stdout.String(), "Usage: concourse") {
			t.Fatalf("help %v: %v", args, err)
		}
	}
	if os.Getenv("CONCOURSE_CONFIG") == "" {
		t.Fatal("help cleared the environment")
	}
}

func TestKongConcourseVersion(t *testing.T) {
	t.Setenv("CONCOURSE_CONFIG", "/missing/concourse.yaml")
	t.Setenv("CONCOURSE_POSTGRES_PORT", "invalid")
	for _, args := range [][]string{{"--version"}, {"-v"}} {
		_, p, stdout := kongTestParser(t)
		if execute, _, err := p.parse(args); err != nil || execute != nil || stdout.String() != concourse.Version+"\n" {
			t.Fatalf("version: execute=%T err=%v output=%q", execute, err, stdout.String())
		}
	}
}

func TestKongConcourseDefaultsMatchLegacy(t *testing.T) {
	key := kongTestKey(t)
	dir := t.TempDir()
	for command, arguments := range map[string][]string{
		"web":           {"--tsa-host-key", key},
		"worker":        {"--work-dir", dir, "--tsa-worker-private-key", key},
		"quickstart":    {"--worker-work-dir", dir},
		"migrate":       {},
		"land-worker":   {"--name", "worker", "--tsa-worker-private-key", key},
		"retire-worker": {"--name", "worker", "--tsa-worker-private-key", key},
		"generate-key":  {"--filename", filepath.Join(dir, "generated")},
	} {
		t.Run(command, func(t *testing.T) {
			_, modern, _ := kongTestParser(t)
			_, legacy, _ := kongTestParser(t)
			args := append([]string{command}, arguments...)
			legacy.schema.CommandHandler = func(flags.Commander, []string) error { return nil }
			if _, err := legacy.schema.ParseArgs(args); err != nil {
				t.Fatal(err)
			}
			if _, _, err := modern.parse(args); err != nil {
				t.Fatal(err)
			}
			for _, option := range modern.commands[command].options {
				name := option.option.LongNameWithNamespace()
				got := option.option.Value()
				want := legacy.schema.Find(command).FindOptionByLongName(name).Value()
				// Kong leaves an unset map nil; go-flags allocates an empty map.
				// Compare their configuration values rather than allocation state.
				if reflect.TypeOf(got).Kind() == reflect.Map && reflect.ValueOf(got).Len() == 0 && reflect.ValueOf(want).Len() == 0 {
					continue
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s: Kong=%#v legacy=%#v", name, got, want)
				}
			}
		})
	}
}

func TestKongConcourseWebConfiguration(t *testing.T) {
	key := kongTestKey(t)
	filename := kongTestConfig(t, fmt.Sprintf(`web:
  tsa-host-key: %q
  postgres-host: from-file
  postgres-port: 6543
  bind-port: 8099
  vault-url: https://vault.example.test
  vault-auth-param:
    RoleID: Role:Value
  main-team-local-user: [test]
  add-local-user: [test:password]
worker:
  tsa-worker-private-key: /missing/unused-worker-key
`, key))
	t.Setenv("CONCOURSE_POSTGRES_HOST", "from-env")
	t.Setenv("CONCOURSE_POSTGRES_PORT", "invalid-overridden-env")
	cmd, p, _ := kongTestParser(t)
	execute, _, err := p.parse([]string{"web", "--config", filename, "--postgres-port", "7654"})
	if err != nil {
		t.Fatal(err)
	}
	if execute != &cmd.Web || cmd.Web.Postgres.Host != "from-env" || cmd.Web.Postgres.Port != 7654 || cmd.Web.RunCommand.BindPort != 8099 {
		t.Fatalf("configuration didn't populate WebCommand: %#v", cmd.Web.Postgres)
	}
	if cmd.Web.TSACommand.HostKey == nil || cmd.Web.TSACommand.HostKey.PrivateKey == nil {
		t.Fatal("TSA key was not decoded by Concourse")
	}
	if got := p.schema.Find("web").FindOptionByLongName("vault-auth-param").Value(); !reflect.DeepEqual(got, map[string]string{"RoleID": "Role:Value"}) {
		t.Fatalf("dynamic Vault flags: %#v", got)
	}
	if err := cmd.Web.populateSharedFlags(); err != nil {
		t.Fatal(err)
	}
	if cmd.Web.Auth.AuthFlags.Clients[cmd.Web.TSACommand.ClientID] == "" {
		t.Fatal("real web shared authentication setup was not usable")
	}
}

func TestKongConcourseWorkerConfiguration(t *testing.T) {
	key := kongTestKey(t)
	dir := t.TempDir()
	filename := kongTestConfig(t, fmt.Sprintf(`worker:
  work-dir: %q
  tsa-worker-private-key: %q
  name: from-file
  tag: [file-one, file-two]
  tsa-host: [file-host:2222]
  ephemeral: true
  bind-port: 0
  max-active-tasks: 12
  http-proxy: http://file-proxy:3128
`, dir, key))
	t.Setenv("CONCOURSE_NAME", "from-env")
	t.Setenv("CONCOURSE_TAG", "env-one,env-two")
	t.Setenv("CONCOURSE_TSA_HOST", "env-one:2222,env-two:2222")
	t.Setenv("http_proxy", "http://env-proxy:3128")
	cmd, p, _ := kongTestParser(t)
	execute, remaining, err := p.parse([]string{"--config", filename, "worker", "--name", "from-cli", "--tag", "cli,comma", "--tag", "cli-two", "--ephemeral=false", "--", "argument"})
	if err != nil {
		t.Fatal(err)
	}
	if execute != &cmd.Worker || !reflect.DeepEqual(remaining, []string{"argument"}) {
		t.Fatal("didn't select the actual WorkerCommand")
	}
	worker := cmd.Worker.Worker
	if worker.Name != "from-cli" || worker.Ephemeral || worker.HTTPProxy != "http://env-proxy:3128" || worker.MaxActiveTasks != 12 {
		t.Fatalf("worker settings: %#v", worker)
	}
	if !reflect.DeepEqual(worker.Tags, []string{"cli,comma", "cli-two"}) || !reflect.DeepEqual(cmd.Worker.TSA.Hosts, []string{"env-one:2222", "env-two:2222"}) {
		t.Fatalf("source replacement: tags=%v TSA=%v", worker.Tags, cmd.Worker.TSA.Hosts)
	}
	if cmd.Worker.BindPort != 0 || cmd.Worker.WorkDir.Path() != dir || cmd.Worker.TSA.WorkerPrivateKey.PrivateKey == nil {
		t.Fatal("typed worker configuration was not applied")
	}
}

func TestKongConcourseRequiredAndValidation(t *testing.T) {
	for _, args := range [][]string{
		{"worker"}, {"generate-key"}, {"web"}, {"unknown"},
		{"generate-key", "--filename", "unused", "--type", "invalid"},
		{"generate-key", "--filename", "unused", "--bits", "not-an-integer"},
		{"generate-key", "--unknown"},
	} {
		_, p, _ := kongTestParser(t)
		if _, _, err := p.parse(args); err == nil {
			t.Errorf("expected an error for %v", args)
		}
	}
	for _, body := range []string{
		"worker:\n  typo: 1\n", "missing-command: {}\n", "worker:\n  name: null\n",
		"worker:\n  name: first\n  name: second\n", "worker: {}\n---\nworker: {}\n",
		"[]\n", "worker: null\n", "generate-key:\n  bits: {wrong: type}\n",
		"generate-key:\n  bits: [2048]\n",
		"web:\n  typo-in-inactive-command: true\n",
	} {
		filename := kongTestConfig(t, body)
		_, p, _ := kongTestParser(t)
		if _, _, err := p.parse([]string{"generate-key", "--filename", "unused", "--config", filename}); err == nil {
			t.Errorf("expected a configuration error for %q", body)
		}
	}
}

func TestKongConcourseConfigPathPrecedence(t *testing.T) {
	t.Setenv("CONCOURSE_CONFIG", "/missing/environment-config")
	filename := kongTestConfig(t, "generate-key:\n  filename: unused\n  bits: 2048\n")
	for _, args := range [][]string{
		{"--config", filename, "generate-key"},
		{"--config=", "generate-key", "--filename", "unused"},
	} {
		_, p, _ := kongTestParser(t)
		if _, _, err := p.parse(args); err != nil {
			t.Fatal(err)
		}
	}
	_, p, _ := kongTestParser(t)
	if _, _, err := p.parse([]string{"generate-key", "--filename", "unused"}); err == nil {
		t.Fatal("missing configured file was silently ignored")
	}
	t.Setenv("CONCOURSE_CONFIG", filename)
	cmd, p, _ := kongTestParser(t)
	if _, _, err := p.parse([]string{"generate-key"}); err != nil || cmd.GenerateKey.Bits != 2048 {
		t.Fatalf("environment config path: %v", err)
	}
}

func TestKongConcourseKeyExecution(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "generated-key")
	config := kongTestConfig(t, fmt.Sprintf("generate-key:\n  filename: %q\n  type: ssh\n  bits: 2048\n", filename))
	t.Setenv("CONCOURSE_CONFIG", config)
	t.Setenv("CONCOURSE_BITS", "3072")
	t.Setenv("CONCOURSE_POSTGRES_PASSWORD", "unused-secret")
	t.Setenv("CONCOURSE_GARDEN_UNREGISTERED_OPTION", "keep")
	t.Setenv("http_proxy", "http://keep-proxy:3128")
	if err := runConcourse([]string{"generate-key", "--bits", "2048"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil || key.N.BitLen() != 2048 {
		t.Fatalf("real GenerateKeyCommand didn't receive the CLI override: %v", err)
	}
	if _, err := os.Stat(filename + ".pub"); err != nil {
		t.Fatal("GenerateKeyCommand didn't write the requested SSH public key", err)
	}
	for _, name := range []string{"CONCOURSE_CONFIG", "CONCOURSE_BITS", "CONCOURSE_POSTGRES_PASSWORD"} {
		if _, ok := os.LookupEnv(name); ok {
			t.Fatalf("%s leaked through Concourse's execution boundary", name)
		}
	}
	if os.Getenv("http_proxy") != "http://keep-proxy:3128" || os.Getenv("CONCOURSE_GARDEN_UNREGISTERED_OPTION") != "keep" {
		t.Fatal("unregistered or explicitly shared environment was cleared")
	}
}
