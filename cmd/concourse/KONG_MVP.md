# Kong MVP in the Concourse executable

This branch changes `cmd/concourse/main.go`: the production `concourse`
executable uses Kong v1.16.1 to parse arguments and print help. Parsed CLI,
environment and YAML values populate the existing command implementations,
which then execute normally. This is based on the fork's `master`, not the
earlier isolated `experiments/kong-config` module or the Cobra/Viper branch.

All seven commands are wired: `web`, `worker`, `quickstart`, `migrate`,
`land-worker`, `retire-worker` and `generate-key`. `fly` is unchanged.

## Build and run

Use the Go version specified in the root `go.mod`:

```sh
go build -o concourse ./cmd/concourse
./concourse --help
./concourse help web
./concourse worker --help
./concourse --version
```

Help uses Kong's model and printer, including grouped flags, short aliases,
environment names, defaults and choices. On narrow terminals, descriptions
appear below flag names so long names do not squeeze them into tiny columns.
Help works before required-value validation,
configuration-file loading and private-key decoding. A missing configuration
file or invalid runtime environment therefore does not prevent help or version
output. Required values remain enforced for command execution.

## Configuration files

Use `--config PATH` (before or after the command), or `CONCOURSE_CONFIG`.
`--config=` disables the environment-specified configuration file. Paths inside
the file follow the existing Concourse flag decoders and are relative to the
process's working directory, not the configuration file's directory.

The file contains command names and their existing, flat, kebab-case flag names:

```yaml
generate-key:
  filename: ./worker-key
  type: ssh
  bits: 2048

worker:
  work-dir: ./worker-data
  tsa-worker-private-key: ./worker-key
  tsa-host: [127.0.0.1:2222]
  name: kong-worker
  tag: [linux, mvp]

web:
  tsa-host-key: ./tsa-host-key
  tsa-authorized-keys: ./worker-key.pub
  postgres-host: 127.0.0.1
  postgres-user: concourse
  postgres-database: concourse
  bind-port: 8080
  main-team-local-user: [test]
  add-local-user:
    test: test-password
```

For example, save the configuration as `concourse.yaml`, then:

```sh
./concourse --config concourse.yaml generate-key
./concourse generate-key --filename ./tsa-host-key --type ssh
./concourse --config concourse.yaml worker
# Requires an existing PostgreSQL database and the generated TSA host key:
./concourse --config concourse.yaml web
```

The resolution order is CLI > environment > YAML > declared defaults.
Each flag selects one source. Collections from a higher-priority source replace
lower-priority collections; repeated CLI collection flags accumulate as before.
Explicit `false`, `0`, empty strings, empty lists and empty maps are retained.
Environment collection flags retain Concourse's comma delimiter; CLI values
retain literal commas. Map keys and values retain case and colons.

Dynamic credential-manager and authentication flags use the same resolution
path. For example, `web.vault-auth-param` accepts a YAML map of Vault parameter
names to values. Secret and key-file types still use Concourse's existing
decoders. Only the selected command's values are decoded, so a missing key
file configured for another command does not stop execution.

Unknown command names, unknown flag names, null values, duplicate YAML keys
and multiple YAML documents are errors. Unknown keys are checked even in
inactive command sections. Native YAML sequences are for collection flags,
and native YAML maps are for map flags; nested collection values are not
supported by this MVP. Recognized `CONCOURSE_` variables, including the config
path, are cleared before executing the command, preserving the existing
Concourse execution boundary. Explicit shared proxy variables and unregistered
Garden variables retain the existing behavior.

## Adapter boundary

Kong owns the actual grammar, help, required flags and value resolution.
`kong.go` temporarily reads go-flags metadata and uses `Option.Set` for existing
custom conversions. It never calls go-flags `Parse` in production. This keeps
the shared flag declarations, dynamic registrations and platform-specific
flags usable without migrating every package at once.

This is a runnable integration MVP, not complete removal of go-flags. A follow-up
can replace the adapter with native Kong declarations and mappers in shared
packages. Unlike go-flags, unset maps remain nil; the web command now initializes
its client map before writing the derived service credentials.

## Validation

The focused tests cover all command defaults against the old parser, help at
several widths, required flags, invalid configuration, source precedence,
dynamic Vault flags, key decoding and shared web authentication setup. They
also execute the real `GenerateKeyCommand` and check the key files it writes.

```sh
go test ./cmd/concourse -run '^TestKong' -count=1
```

An opt-in Linux/root smoke test starts the built executable as a real worker
using Houdini and the naive Baggageclaim driver. It checks Garden's `/ping`,
Baggageclaim's `/volumes`, the worker's health response, work-directory use and
graceful shutdown. A local SSH fixture checks authentication and the worker's
registration payload; it does not simulate an ATC or database. This test does
not require an external TSA or PostgreSQL. Registration against a real web
node is outside its scope.

```sh
go build -o /tmp/concourse-kong ./cmd/concourse
CONCOURSE_KONG_TEST_BINARY=/tmp/concourse-kong \
  go test ./cmd/concourse -run '^TestKong' -count=1
```

The existing `cmd/concourse` Ginkgo web integration tests require PostgreSQL's
`initdb` and server binaries. They cannot run in an environment without those
dependencies; the worker smoke test does not replace that coverage.
