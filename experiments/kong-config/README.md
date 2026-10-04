# Kong configuration prototype

This runnable experiment evaluates Kong for Concourse issue
[#9085](https://github.com/concourse/concourse/issues/9085). It parses a
representative selection of real Concourse flags, reads YAML configuration,
and prints a redacted preview. **It does not start a web node or worker.**

It is based on the fork's `master` at `e5a8402cc06c4a14ae2f4d540749cdc61427325d`,
not on the older Cobra/Viper WIP. It lives in an independent Go module so the
experiment does not change production dependencies or entrypoints.

## Run it

Use Go 1.26 or later, from this directory:

```sh
go run . --help
go run . web --help
go run . worker --help
go run . web --config examples/web.yaml
go run . worker --config examples/worker.yaml
go test ./...
go build -o concourse-kong .
```

On Unix, inspect narrow-terminal behavior and an environment override:

```sh
COLUMNS=60 go run . web --help
CONCOURSE_POSTGRES_HOST=db.internal go run . web --config examples/web.yaml
CONCOURSE_POSTGRES_PORT=5433 go run . web --config examples/web.yaml --postgres-port=5434
```

On PowerShell:

```powershell
$env:CONCOURSE_POSTGRES_HOST = 'db.internal'
go run . web --config examples/web.yaml
Remove-Item Env:CONCOURSE_POSTGRES_HOST
```

The third Unix example resolves `postgres-port` to `5434`. You can also select
the file with `CONCOURSE_CONFIG`. An explicit `--config` takes precedence;
`--config=` disables the environment-selected file.

## Coverage

| Area | Included |
| --- | --- |
| PostgreSQL | All 13 flags declared by `flag.PostgresConfig`, with their names, defaults and enums |
| Vault | Selected connection, timeout, authentication and TLS options; registered through `kong.Plugins` |
| Worker | Identity, tags, team, ephemeral mode, task limit, work directory, bind address/port, HTTP proxy, external Garden URL and TSA connection options |
| Help | Kong's own grouped help, wrapping, environment hints, and help before configuration loading or required-option validation |
| Configuration | YAML files, command-specific sections, native lists/maps, unknown-key checking and explicit precedence |

YAML keys are the exact long flag names, grouped by command. A single file can
contain both `web:` and `worker:` sections. For example:

```yaml
web:
  postgres-host: db.internal
  postgres-port: 5432
  vault-auth-param:
    role_id: example-role
worker:
  work-dir: ./worker-data
  tsa-worker-private-key: ./keys/worker_key
  tag: [linux, build]
```

Keys are case-sensitive. Map data, such as Vault authentication parameter keys,
retains its original case. Unknown keys in either section fail validation.
Explicit missing files, duplicate keys, multiple YAML documents and `null`
values also fail. Use `""`, `[]`, `{}`, `false` or `0` for intentional empty
values. Values are decoded and validated for the selected command only.

## Source precedence and compatibility

**Command line > environment > YAML > defaults.** A higher-priority value
replaces the entire lower-priority list or map. Repeating a CLI flag accumulates
values within the command-line source.

The experiment preserves these existing behaviors:

- Derived `CONCOURSE_*` variable names, plus the explicit `http_proxy` exception.
- Literal commas in repeated CLI list values; environment lists split on commas.
- Vault `--vault-auth-param=NAME:VALUE` syntax, including colons in the value.
- Empty environment strings count as supplied values; an empty boolean
  environment variable means `true`, as in go-flags.
- Explicit `false` and `0` override values from lower-priority sources.
- Required worker values can come from flags, environment variables or YAML.
- Registered `CONCOURSE_*` variables are cleared after successful parsing,
  including those belonging to another command. Unregistered variables and
  explicit proxy variables survive. Help and failed parses do not clear them.

The tests compare the ported flag names, defaults, requiredness and environment
names against the actual production Go declarations using Go's AST parser.
They also exercise precedence, typed collections, invalid input, redaction and
help at 60/80/120 columns. This module has its own test command; the repository
root's `go test ./...` does not descend into nested modules.

## How it works

- `options.go` contains native Kong declarations and three domain decoders. It
  demonstrates existing URL and IP parsing contracts through `encoding.TextUnmarshaler`
  and preserves Vault's legacy map syntax with a small `kong.MapperValue`.
- `sources.go` supplies one resolver backed by environment variables and
  `go.yaml.in/yaml/v3`. The Kong command model supplies the valid YAML keys.
  There is no separate YAML schema and no interpreter for legacy go-flags tags.
- Kong normally parses environment variables during reset, before resolvers.
  After constructing its model, the prototype retains `Flag.Envs` as metadata
  and clears `Tag.Envs` to route environment values through the resolver.
  This avoids an invalid lower-priority environment value failing a valid CLI
  override. This use of exported Kong model fields is pinned and covered by tests.
- `main.go` uses Kong's built-in help renderer. The small value formatter adds
  environment hints; Concourse does not own the column/wrapping implementation.

The dependencies are Kong `v1.16.1` and the maintained YAML package already
used by Concourse, `go.yaml.in/yaml/v3` `v3.0.4`. The experiment does not use
Viper, Koanf, `kong-yaml`, or a custom reflection-based flag binder.

## Limits and migration decision

This is a parser experiment, not a replacement `concourse` executable. Only the
options above are ported. It does not validate key/certificate contents, connect
to PostgreSQL/Vault/TSA, normalize filesystem paths like every production flag
type does, or initialize production runtime defaults. Preview output redacts
passwords, Vault tokens and authentication parameters and is diagnostic output,
not an export intended for deployment.

Kong's non-Unix help implementation uses an 80-column fallback; the wrapping
test checks the narrower layout on Linux. A production Windows port needs
explicit console-width handling. Arbitrarily long flag/environment names can
still exceed a narrow terminal's width, but do not push descriptions into an
unusable column.

Before adopting this for production, port the remaining shared option types
and registered providers, including repeatable fields with multiple defaults;
compare their actual parsing behavior against go-flags; and test `quickstart`
prefixes, all supported platforms, version/help/exit behavior, and subprocess
environment forwarding. Wire the resulting typed configuration into the real
web/worker entrypoints only after that comparison passes.
