package main

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/alecthomas/kong"
)

// This experiment ports a representative subset of Concourse's options.
// Existing flag names/defaults come from flag/postgres_config.go,
// atc/creds/vault/manager.go, and worker/{workercmd,tsa_config.go}.
type CLI struct {
	Config string        `help:"Read configuration from a YAML file." placeholder:"PATH"`
	Web    WebOptions    `cmd:"" help:"Validate web configuration and print a redacted preview."`
	Worker WorkerOptions `cmd:"" help:"Validate worker configuration and print a redacted preview."`

	sources *sources
}

type WebOptions struct {
	BindPort uint16          `default:"8080" help:"Port on which to listen for HTTP traffic."`
	Postgres PostgresOptions `embed:"" prefix:"postgres-" group:"PostgreSQL"`
	kong.Plugins

	vault *vaultPlugin
}

type PostgresOptions struct {
	Host            string        `default:"127.0.0.1" help:"The host to connect to."`
	Port            uint16        `default:"5432" help:"The port to connect to."`
	Socket          string        `help:"Path to a UNIX domain socket to connect to."`
	User            string        `help:"The user to sign in as."`
	Password        string        `help:"The user's password."`
	ApplicationName string        `default:"concourse" help:"Application name for connection tracking in pg_stat_activity."`
	SSLMode         string        `name:"sslmode" default:"disable" enum:"disable,require,verify-ca,verify-full" help:"Whether or not to use SSL."`
	SSLNegotiation  string        `name:"sslnegotiation" default:"postgres" enum:"postgres,direct" help:"Controls how SSL encryption is negotiated with the server."`
	CACert          string        `help:"CA cert file location."`
	ClientCert      string        `help:"Client cert file location."`
	ClientKey       string        `help:"Client key file location."`
	ConnectTimeout  time.Duration `default:"5m" help:"Dialing timeout; zero means wait indefinitely."`
	Database        string        `default:"atc" help:"The name of the database to use."`
}

// A plugin stands in for Concourse's dynamically registered credential managers.
// Kong supplies the embedding, prefixing and help grouping.
type vaultPlugin struct {
	Options VaultOptions `embed:"" prefix:"vault-" group:"Vault"`
}

type VaultOptions struct {
	URL                string        `name:"url" help:"Vault server address used to access secrets."`
	PathPrefix         string        `default:"/concourse" help:"Path under which to namespace credential lookup."`
	SharedPath         string        `help:"Path under which to look up shared credentials."`
	Namespace          string        `help:"Vault namespace for authentication and secret lookup."`
	LoginTimeout       time.Duration `default:"60s" help:"Timeout for Vault login."`
	QueryTimeout       time.Duration `default:"60s" help:"Timeout for a Vault query."`
	EnableKVMountCache bool          `help:"Cache KV mount version detection."`
	InsecureSkipVerify bool          `help:"Disable TLS certificate verification."`
	ClientToken        string        `help:"Client token for accessing Vault secrets."`
	AuthBackend        string        `help:"Authentication backend to use for logging in."`
	AuthParam          AuthParams    `placeholder:"NAME:VALUE" help:"Authentication parameter; repeat the flag to add parameters."`
}

type WorkerOptions struct {
	Name              string     `group:"Worker" help:"Name used during worker registration."`
	Tag               []string   `group:"Worker" sep:"none" help:"Registration tag; may be specified multiple times."`
	Team              string     `group:"Worker" help:"Team to which this worker is assigned."`
	Ephemeral         bool       `group:"Worker" help:"Remove the worker immediately upon stalling."`
	MaxActiveTasks    int        `group:"Worker" help:"Override the maximum number of active tasks for this worker."`
	WorkDir           string     `group:"Worker" required:"" help:"Directory in which to place container data."`
	HTTPProxy         string     `group:"Worker" name:"http-proxy" env:"http_proxy" help:"HTTP proxy endpoint for containers."`
	BindIP            IP         `group:"Worker" name:"bind-ip" default:"127.0.0.1" help:"IP address for the Garden server."`
	BindPort          uint16     `group:"Worker" default:"7777" help:"Port for the Garden server."`
	ExternalGardenURL URL        `group:"Worker" name:"external-garden-url" help:"Endpoint of an externally managed Garden server."`
	TSA               TSAOptions `embed:"" prefix:"tsa-" group:"TSA"`
}

type TSAOptions struct {
	Host             []string `sep:"none" default:"127.0.0.1:2222" help:"TSA host; may be specified multiple times."`
	PublicKey        string   `help:"File containing the expected TSA public key."`
	WorkerPrivateKey string   `required:"" help:"File containing the private key for TSA authentication."`
}

// URL demonstrates adapting the existing flag.URL.UnmarshalFlag behavior to
// encoding.TextUnmarshaler, which Kong supports without a reflection binder.
type URL string

func (u *URL) UnmarshalText(text []byte) error {
	value := strings.TrimRight(string(text), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("expected an absolute URL with scheme and host")
	}
	*u = URL(value)
	return nil
}

type IP string

func (ip *IP) UnmarshalText(text []byte) error {
	if net.ParseIP(string(text)) == nil {
		return fmt.Errorf("expected an IP address")
	}
	*ip = IP(text)
	return nil
}

// AuthParams retains go-flags' NAME:VALUE syntax. YAML mappings arrive as
// typed values; their keys and values must not be split or lowercased.
type AuthParams map[string]string

func (p *AuthParams) Decode(ctx *kong.DecodeContext) error {
	token, err := ctx.Scan.PopValue("authentication parameter")
	if err != nil {
		return err
	}
	if *p == nil {
		*p = AuthParams{}
	}
	switch value := token.Value.(type) {
	case string:
		key, val, _ := strings.Cut(value, ":")
		(*p)[key] = val
	case map[string]any:
		for key, val := range value {
			text, ok := val.(string)
			if !ok {
				return fmt.Errorf("authentication parameter %q must be a string", key)
			}
			(*p)[key] = text
		}
	default:
		return fmt.Errorf("expected NAME:VALUE or a YAML mapping")
	}
	return nil
}
