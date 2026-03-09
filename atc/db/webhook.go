package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"time"

	sq "github.com/Masterminds/squirrel"

	"github.com/concourse/concourse/atc"
)

//counterfeiter:generate . Webhook
type Webhook interface {
	ID() int
	Name() string
	Type() string
	Token() string
	Secret() string
	Rules() []atc.WebhookMatcherRule
	SignatureHeader() string
	SignatureAlgo() string
	TeamID() int
	CreatedAt() time.Time
	UpdatedAt() time.Time

	// Matcher returns a WebhookMatcher built from per-webhook DB config.
	// Returns (nil, false) if no rules are configured.
	Matcher() (*atc.WebhookMatcher, bool)
}

type webhook struct {
	id              int
	name            string
	type_           string
	token           string
	secret          string
	rules           []atc.WebhookMatcherRule
	signatureHeader string
	signatureAlgo   string
	teamID          int
	createdAt       time.Time
	updatedAt       time.Time
}

func (w *webhook) ID() int                          { return w.id }
func (w *webhook) Name() string                     { return w.name }
func (w *webhook) Type() string                     { return w.type_ }
func (w *webhook) Token() string                    { return w.token }
func (w *webhook) Secret() string                   { return w.secret }
func (w *webhook) Rules() []atc.WebhookMatcherRule  { return w.rules }
func (w *webhook) SignatureHeader() string          { return w.signatureHeader }
func (w *webhook) SignatureAlgo() string            { return w.signatureAlgo }
func (w *webhook) TeamID() int                      { return w.teamID }
func (w *webhook) CreatedAt() time.Time             { return w.createdAt }
func (w *webhook) UpdatedAt() time.Time             { return w.updatedAt }

// Matcher builds a WebhookMatcher from per-webhook rules stored in the DB.
// Per-webhook matchers (set via fly set-webhook) take precedence over global
// operator-level matchers loaded from the CONCOURSE_WEBHOOK_MATCHERS config file.
func (w *webhook) Matcher() (*atc.WebhookMatcher, bool) {
	if len(w.rules) == 0 {
		return nil, false
	}
	m := atc.NewWebhookMatcher(atc.WebhookMatcher{
		Rules:           w.rules,
		SignatureHeader: w.signatureHeader,
		SignatureAlgo:   w.signatureAlgo,
	})
	return m, true
}

var webhookColumns = []string{
	"id", "name", "type", "token",
	"secret", "nonce",
	"matcher_rules", "signature_header", "signature_algo",
	"team_id", "created_at", "updated_at",
}

func scanWebhook(row scannable, es interface{ Decrypt(string, *string) ([]byte, error) }) (Webhook, error) {
	w := &webhook{}
	var encryptedSecret sql.NullString
	var nonce sql.NullString
	var rulesJSON []byte
	err := row.Scan(
		&w.id, &w.name, &w.type_, &w.token,
		&encryptedSecret, &nonce,
		&rulesJSON, &w.signatureHeader, &w.signatureAlgo,
		&w.teamID, &w.createdAt, &w.updatedAt,
	)
	if err != nil {
		return nil, err
	}
	if encryptedSecret.Valid && encryptedSecret.String != "" {
		decrypted, err := es.Decrypt(encryptedSecret.String, &nonce.String)
		if err != nil {
			w.secret = encryptedSecret.String
		} else {
			w.secret = string(decrypted)
		}
	}
	if len(rulesJSON) > 0 && string(rulesJSON) != "[]" && string(rulesJSON) != "null" {
		if err := json.Unmarshal(rulesJSON, &w.rules); err != nil {
			return nil, err
		}
	}
	return w, nil
}

func generateWebhookToken() (string, error) {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// WebhookConfig holds all configurable fields for creating/updating a webhook.
type WebhookConfig struct {
	Name            string
	Type            string
	Secret          string
	Rules           []atc.WebhookMatcherRule
	SignatureHeader string
	SignatureAlgo   string
}

// SaveWebhook upserts a webhook for the team from a WebhookConfig.
// The secret is encrypted at rest using Concourse's encryption strategy.
// The matcher rules are stored as JSONB.
func (t *team) SaveWebhook(cfg WebhookConfig) (Webhook, error) {
	token, err := generateWebhookToken()
	if err != nil {
		return nil, err
	}

	var encryptedSecret string
	var nonceStr *string
	if cfg.Secret != "" {
		encrypted, nonce, err := t.conn.EncryptionStrategy().Encrypt([]byte(cfg.Secret))
		if err != nil {
			return nil, err
		}
		encryptedSecret = encrypted
		nonceStr = nonce
	}

	rulesJSON, err := json.Marshal(cfg.Rules)
	if err != nil {
		return nil, err
	}
	if rulesJSON == nil {
		rulesJSON = []byte("[]")
	}

	var id int
	var createdAt, updatedAt time.Time
	err = psql.Insert("webhooks").
		Columns("name", "type", "token", "secret", "nonce",
			"matcher_rules", "signature_header", "signature_algo",
			"team_id", "updated_at").
		Values(
			cfg.Name, cfg.Type, token, encryptedSecret, nonceStr,
			string(rulesJSON), cfg.SignatureHeader, cfg.SignatureAlgo,
			t.id, sq.Expr("now()"),
		).
		Suffix(`ON CONFLICT (team_id, name) DO UPDATE SET
			type = EXCLUDED.type,
			token = EXCLUDED.token,
			secret = EXCLUDED.secret,
			nonce = EXCLUDED.nonce,
			matcher_rules = EXCLUDED.matcher_rules,
			signature_header = EXCLUDED.signature_header,
			signature_algo = EXCLUDED.signature_algo,
			updated_at = now()`).
		Suffix("RETURNING id, created_at, updated_at").
		RunWith(t.conn).
		QueryRow().
		Scan(&id, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}

	return &webhook{
		id:              id,
		name:            cfg.Name,
		type_:           cfg.Type,
		token:           token,
		secret:          cfg.Secret,
		rules:           cfg.Rules,
		signatureHeader: cfg.SignatureHeader,
		signatureAlgo:   cfg.SignatureAlgo,
		teamID:          t.id,
		createdAt:       createdAt,
		updatedAt:       updatedAt,
	}, nil
}

// DestroyWebhook deletes a webhook by name within the team.
func (t *team) DestroyWebhook(name string) error {
	_, err := psql.Delete("webhooks").
		Where(sq.Eq{"team_id": t.id, "name": name}).
		RunWith(t.conn).
		Exec()
	return err
}

// FindWebhook looks up a webhook by name within the team.
func (t *team) FindWebhook(name string) (Webhook, bool, error) {
	row := psql.Select(webhookColumns...).
		From("webhooks").
		Where(sq.Eq{"team_id": t.id, "name": name}).
		RunWith(t.conn).
		QueryRow()

	w, err := scanWebhook(row, t.conn.EncryptionStrategy())
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}
	return w, true, nil
}

// Webhooks returns all webhooks for the team.
func (t *team) Webhooks() ([]Webhook, error) {
	rows, err := psql.Select(webhookColumns...).
		From("webhooks").
		Where(sq.Eq{"team_id": t.id}).
		OrderBy("name ASC").
		RunWith(t.conn).
		Query()
	if err != nil {
		return nil, err
	}
	defer Close(rows)

	es := t.conn.EncryptionStrategy()
	var webhooks []Webhook
	for rows.Next() {
		w, err := scanWebhook(rows, es)
		if err != nil {
			return nil, err
		}
		webhooks = append(webhooks, w)
	}
	return webhooks, nil
}

// FindResourcesByWebhookPayload finds all resources subscribed to the given webhook
// type whose explicit JSONB filter is contained within the payload.
func (t *team) FindResourcesByWebhookPayload(webhookType string, payload json.RawMessage) ([]Resource, error) {
	rows, err := resourcesQuery.
		Join("resource_webhook_subscriptions rws ON rws.resource_id = r.id").
		Where(sq.Eq{"rws.webhook_type": webhookType, "p.team_id": t.id}).
		Where("rws.filter::text != '{}'").
		Where("? @> rws.filter", string(payload)).
		OrderBy("r.id ASC").
		RunWith(t.conn).
		Query()
	if err != nil {
		return nil, err
	}
	defer Close(rows)

	var resources []Resource
	for rows.Next() {
		r := newEmptyResource(t.conn, t.lockFactory)
		if err := scanResource(r, rows); err != nil {
			return nil, err
		}
		resources = append(resources, r)
	}
	return resources, nil
}

// FindResourcesByWebhookType finds all resources subscribed to the given webhook type.
func (t *team) FindResourcesByWebhookType(webhookType string) ([]Resource, error) {
	rows, err := resourcesQuery.
		Join("resource_webhook_subscriptions rws ON rws.resource_id = r.id").
		Where(sq.Eq{"rws.webhook_type": webhookType, "p.team_id": t.id}).
		OrderBy("r.id ASC").
		RunWith(t.conn).
		Query()
	if err != nil {
		return nil, err
	}
	defer Close(rows)

	var resources []Resource
	for rows.Next() {
		r := newEmptyResource(t.conn, t.lockFactory)
		if err := scanResource(r, rows); err != nil {
			return nil, err
		}
		resources = append(resources, r)
	}
	return resources, nil
}

// saveResourceWebhookSubscriptions persists webhook subscriptions from pipeline config.
func saveResourceWebhookSubscriptions(tx Tx, resourceNameToID map[string]int, resources atc.ResourceConfigs) error {
	for _, id := range resourceNameToID {
		_, err := psql.Delete("resource_webhook_subscriptions").
			Where(sq.Eq{"resource_id": id}).
			RunWith(tx).
			Exec()
		if err != nil {
			return err
		}
	}

	for _, resource := range resources {
		if len(resource.Webhooks) == 0 {
			continue
		}
		resourceID, ok := resourceNameToID[resource.Name]
		if !ok {
			continue
		}
		for _, sub := range resource.Webhooks {
			filterJSON, err := json.Marshal(sub.Filter)
			if err != nil {
				return err
			}
			if sub.Filter == nil {
				filterJSON = []byte("{}")
			}
			_, err = psql.Insert("resource_webhook_subscriptions").
				Columns("resource_id", "webhook_type", "filter").
				Values(resourceID, sub.Type, string(filterJSON)).
				Suffix("ON CONFLICT (resource_id, webhook_type, filter) DO NOTHING").
				RunWith(tx).
				Exec()
			if err != nil {
				return err
			}
		}
	}
	return nil
}
