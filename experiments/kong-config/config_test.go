package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
)

func cleanEnvironment(t *testing.T) {
	t.Helper()
	for _, env := range os.Environ() {
		name, _, _ := strings.Cut(env, "=")
		if strings.HasPrefix(name, "CONCOURSE_") || name == "http_proxy" {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func configFile(t *testing.T, text string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(name, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func parseConfig(t *testing.T, args ...string) (*CLI, *kong.Context, error) {
	t.Helper()
	cli, parser, err := newParser(io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := parser.Parse(args)
	return cli, ctx, err
}

func TestDefaultsAndDynamicProvider(t *testing.T) {
	cleanEnvironment(t)
	cli, _, err := parseConfig(t, "web")
	if err != nil {
		t.Fatal(err)
	}
	if cli.Web.Postgres.Host != "127.0.0.1" || cli.Web.Postgres.Port != 5432 || cli.Web.Postgres.ConnectTimeout != 5*time.Minute {
		t.Fatalf("unexpected PostgreSQL defaults: %+v", cli.Web.Postgres)
	}
	if cli.Web.vault.Options.PathPrefix != "/concourse" || cli.Web.vault.Options.QueryTimeout != time.Minute {
		t.Fatalf("dynamic Vault defaults: %+v", cli.Web.vault.Options)
	}
}

func TestPrecedenceAndExplicitEmptyValues(t *testing.T) {
	cleanEnvironment(t)
	file := configFile(t, `web:
  postgres-host: file-host
  postgres-port: 5433
  postgres-password: file-password
  postgres-connect-timeout: 12s
  vault-insecure-skip-verify: true
  vault-auth-param: {RoleID: file-role, secret_id: 'with:colon,comma'}
`)
	t.Setenv("CONCOURSE_POSTGRES_HOST", "env-host")
	t.Setenv("CONCOURSE_POSTGRES_PASSWORD", "")
	t.Setenv("CONCOURSE_VAULT_INSECURE_SKIP_VERIFY", "true")
	cli, _, err := parseConfig(t, "web", "--config", file, "--postgres-port=0", "--vault-insecure-skip-verify=false")
	if err != nil {
		t.Fatal(err)
	}
	pg := cli.Web.Postgres
	if pg.Host != "env-host" || pg.Port != 0 || pg.Password != "" || pg.ConnectTimeout != 12*time.Second {
		t.Fatalf("precedence failed: host=%q port=%d password-empty=%t timeout=%s", pg.Host, pg.Port, pg.Password == "", pg.ConnectTimeout)
	}
	if cli.Web.vault.Options.InsecureSkipVerify {
		t.Fatal("an explicit CLI false must override true from lower sources")
	}
	if got := cli.Web.vault.Options.AuthParam["secret_id"]; got != "with:colon,comma" {
		t.Fatalf("YAML map value changed: %q", got)
	}
	if got := cli.Web.vault.Options.AuthParam["RoleID"]; got != "file-role" {
		t.Fatal("YAML map keys must preserve their case")
	}
	cli, _, err = parseConfig(t, "web", "--config", file, "--postgres-host=")
	if err != nil || cli.Web.Postgres.Host != "" {
		t.Fatalf("an explicit empty CLI string must win: %v", err)
	}
}

func TestCLIOverridesInvalidLowerSource(t *testing.T) {
	cleanEnvironment(t)
	t.Setenv("CONCOURSE_POSTGRES_PORT", "not-a-number")
	file := configFile(t, "web:\n  postgres-port: also-not-a-number\n")
	cli, _, err := parseConfig(t, "web", "--config", file, "--postgres-port", "5434")
	if err != nil || cli.Web.Postgres.Port != 5434 {
		t.Fatalf("overridden lower sources must not be decoded: %v", err)
	}
}

func TestWorkerRequiredValuesCollectionsAndProxy(t *testing.T) {
	cleanEnvironment(t)
	file := configFile(t, `worker:
  work-dir: /tmp/concourse-worker
  tsa-worker-private-key: /tmp/worker-key
  tsa-host: [one.example:2222, two.example:2222]
  tag: [from-file]
  ephemeral: true
  max-active-tasks: 20
`)
	t.Setenv("CONCOURSE_TAG", "env-one,env-two")
	t.Setenv("http_proxy", "http://proxy.example:8080")
	cli, _, err := parseConfig(t, "worker", "--config", file, "--tag", "cli,one", "--tag", "cli-two", "--ephemeral=false", "--max-active-tasks=0")
	if err != nil {
		t.Fatal(err)
	}
	w := cli.Worker
	if !reflect.DeepEqual(w.Tag, []string{"cli,one", "cli-two"}) || !reflect.DeepEqual(w.TSA.Host, []string{"one.example:2222", "two.example:2222"}) {
		t.Fatalf("CLI lists must replace env/config; YAML lists must replace defaults: tags=%v hosts=%v", w.Tag, w.TSA.Host)
	}
	if w.Ephemeral || w.MaxActiveTasks != 0 || w.HTTPProxy != "http://proxy.example:8080" {
		t.Fatalf("worker values: %+v", w)
	}
	cli, _, err = parseConfig(t, "worker", "--config", file)
	if err != nil || !reflect.DeepEqual(cli.Worker.Tag, []string{"env-one", "env-two"}) {
		t.Fatalf("environment list must split commas: %v", err)
	}
	file = configFile(t, "worker:\n  work-dir: /tmp/work\n  tsa-worker-private-key: /tmp/key\n  tsa-host: []\n")
	cli, _, err = parseConfig(t, "worker", "--config", file)
	if err != nil || len(cli.Worker.TSA.Host) != 0 {
		t.Fatalf("explicit empty YAML list must replace defaults: %v", err)
	}
}

func TestAuthParamSourceReplacement(t *testing.T) {
	cleanEnvironment(t)
	file := configFile(t, "web:\n  vault-auth-param: {from_file: value}\n")
	t.Setenv("CONCOURSE_VAULT_AUTH_PARAM", "RoleID:one,secret_id:two:three")
	cli, _, err := parseConfig(t, "web", "--config", file)
	if err != nil || !reflect.DeepEqual(cli.Web.vault.Options.AuthParam, AuthParams{"RoleID": "one", "secret_id": "two:three"}) {
		t.Fatalf("environment map: %v", err)
	}
	cli, _, err = parseConfig(t, "web", "--config", file, "--vault-auth-param", "first:with:colon", "--vault-auth-param", "second:with,comma")
	if err != nil || !reflect.DeepEqual(cli.Web.vault.Options.AuthParam, AuthParams{"first": "with:colon", "second": "with,comma"}) {
		t.Fatalf("CLI maps must replace, not merge, lower sources: %v", err)
	}
}

func TestEmptyBooleanEnvironment(t *testing.T) {
	cleanEnvironment(t)
	t.Setenv("CONCOURSE_VAULT_INSECURE_SKIP_VERIFY", "")
	cli, _, err := parseConfig(t, "web")
	if err != nil || !cli.Web.vault.Options.InsecureSkipVerify {
		t.Fatalf("legacy empty boolean env means true: %v", err)
	}
}

func TestWorkerRequirementsFromEnvironment(t *testing.T) {
	cleanEnvironment(t)
	t.Setenv("CONCOURSE_WORK_DIR", "/tmp/work")
	t.Setenv("CONCOURSE_TSA_WORKER_PRIVATE_KEY", "/tmp/key")
	cli, _, err := parseConfig(t, "worker")
	if err != nil || cli.Worker.WorkDir != "/tmp/work" || cli.Worker.TSA.WorkerPrivateKey != "/tmp/key" {
		t.Fatalf("environment must satisfy required options: %v", err)
	}
	if _, _, err := parseConfig(t, "worker", "--bind-ip=not-an-address"); err == nil {
		t.Fatal("bind-ip must be a valid IP address")
	}
}

func TestConfigPathFromEnvironmentAndParserReuse(t *testing.T) {
	cleanEnvironment(t)
	t.Setenv("CONCOURSE_CONFIG", configFile(t, "web:\n  postgres-host: env-file\n"))
	cli, _, err := parseConfig(t, "web")
	if err != nil || cli.Web.Postgres.Host != "env-file" {
		t.Fatalf("CONCOURSE_CONFIG: %v", err)
	}
	cli, _, err = parseConfig(t, "--config", configFile(t, "web:\n  postgres-host: cli-file\n"), "web")
	if err != nil || cli.Web.Postgres.Host != "cli-file" {
		t.Fatalf("CLI config path must win: %v", err)
	}
	cli, parser, err := newParser(io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse([]string{"web"}); err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse([]string{"web", "--config="}); err != nil || cli.Web.Postgres.Host != "127.0.0.1" {
		t.Fatalf("config values leaked across parses: %v", err)
	}
}

func TestInvalidConfiguration(t *testing.T) {
	cleanEnvironment(t)
	for _, tc := range []struct{ name, yaml, message string }{
		{"unknown command", "weeb: {}", "weeb"},
		{"unknown key", "web: {postgres-hots: bad}", "web.postgres-hots"},
		{"unknown unselected key", "worker: {typo: bad}", "worker.typo"},
		{"null", "web: {postgres-host: null}", "null"},
		{"null command", "web: null", "web"},
		{"duplicate", "web:\n  postgres-port: 5432\n  postgres-port: 5433", "already defined"},
		{"multiple documents", "web: {}\n---\nworker: {}", "one YAML document"},
		{"invalid enum", "web: {postgres-sslmode: invalid}", "postgres-sslmode"},
		{"invalid duration", "web: {postgres-connect-timeout: yesterday}", "postgres-connect-timeout"},
		{"out of range", "web: {postgres-port: 65536}", "postgres-port"},
		{"wrong map value", "web: {vault-auth-param: {role: 123}}", "must be a string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseConfig(t, "web", "--config", configFile(t, tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("expected error containing %q, got %v", tc.message, err)
			}
		})
	}
	if _, _, err := parseConfig(t, "worker"); err == nil {
		t.Fatal("worker must require work-dir and TSA private-key path")
	}
	if _, _, err := parseConfig(t, "web", "--config", filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("an explicit missing config file must fail")
	}
}

func TestURLDecoder(t *testing.T) {
	cleanEnvironment(t)
	args := []string{"worker", "--work-dir=/tmp/work", "--tsa-worker-private-key=/tmp/key", "--external-garden-url=https://garden.example/"}
	cli, _, err := parseConfig(t, args...)
	if err != nil || cli.Worker.ExternalGardenURL != "https://garden.example" {
		t.Fatalf("URL decoder failed: %v", err)
	}
	args[len(args)-1] = "--external-garden-url=garden.example"
	if _, _, err := parseConfig(t, args...); err == nil {
		t.Fatal("URL must have a scheme and host")
	}
}

func TestHelpWithoutRuntimeConfiguration(t *testing.T) {
	cleanEnvironment(t)
	t.Setenv("CONCOURSE_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	t.Setenv("CONCOURSE_POSTGRES_PORT", "invalid")
	for _, width := range []string{"60", "80", "120"} {
		t.Run(width, func(t *testing.T) {
			t.Setenv("COLUMNS", width)
			for _, command := range []string{"web", "worker"} {
				var out, errs bytes.Buffer
				if code := run([]string{command, "--help"}, &out, &errs); code != 0 || errs.Len() != 0 {
					t.Fatalf("help failed: code=%d stderr=%s", code, &errs)
				}
				for _, want := range []string{"Usage:", "CONCOURSE_CONFIG"} {
					if !strings.Contains(out.String(), want) {
						t.Fatalf("help missing %q", want)
					}
				}
				if command == "web" && (!strings.Contains(out.String(), "PostgreSQL") || !strings.Contains(out.String(), "Vault") || !strings.Contains(out.String(), "CONCOURSE_VAULT_AUTH_PARAM")) {
					t.Fatal("dynamic provider and PostgreSQL groups/environment hints must be visible")
				}
				// An option or an unbroken environment name may be longer than a
				// narrow terminal; descriptions must still have usable columns.
				if runtime.GOOS == "linux" && width == "60" && strings.Contains(out.String(), strings.Repeat(" ", 40)) {
					t.Fatal("help over-indents descriptions on a narrow terminal")
				}
			}
		})
	}
	if os.Getenv("CONCOURSE_POSTGRES_PORT") != "invalid" {
		t.Fatal("help must not consume environment variables")
	}
}

func TestRedactionAndEnvironmentCleanup(t *testing.T) {
	cleanEnvironment(t)
	t.Setenv("CONCOURSE_POSTGRES_PASSWORD", "do-not-print-this")
	t.Setenv("CONCOURSE_VAULT_CLIENT_TOKEN", "do-not-print-token")
	t.Setenv("CONCOURSE_TAG", "registered-for-other-command")
	t.Setenv("CONCOURSE_GARDEN_UNREGISTERED", "forward-to-gdn")
	t.Setenv("http_proxy", "http://proxy.example")
	var out, errs bytes.Buffer
	if code := run([]string{"web", "--vault-auth-param=secret_id:do-not-print-param"}, &out, &errs); code != 0 {
		t.Fatalf("run failed: %s", &errs)
	}
	if strings.Contains(out.String(), "do-not-print") || !strings.Contains(out.String(), "<redacted>") {
		t.Fatal("preview must redact passwords, tokens and authentication parameters")
	}
	for _, name := range []string{"CONCOURSE_POSTGRES_PASSWORD", "CONCOURSE_VAULT_CLIENT_TOKEN", "CONCOURSE_TAG"} {
		if _, exists := os.LookupEnv(name); exists {
			t.Fatalf("registered environment variable survived: %s", name)
		}
	}
	if os.Getenv("CONCOURSE_GARDEN_UNREGISTERED") != "forward-to-gdn" || os.Getenv("http_proxy") != "http://proxy.example" {
		t.Fatal("unknown and explicit non-CONCOURSE environment variables must survive")
	}
}

func TestParseHelpSentinel(t *testing.T) {
	cleanEnvironment(t)
	_, _, err := parseConfig(t, "--help")
	if !errors.Is(err, errHelp) {
		t.Fatalf("expected help, got %v", err)
	}
}
