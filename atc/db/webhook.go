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
	TeamID() int
	CreatedAt() time.Time
	UpdatedAt() time.Time
}

type webhook struct {
	id        int
	name      string
	type_     string
	token     string
	secret    string
	teamID    int
	createdAt time.Time
	updatedAt time.Time
}

func (w *webhook) ID() int              { return w.id }
func (w *webhook) Name() string         { return w.name }
func (w *webhook) Type() string         { return w.type_ }
func (w *webhook) Token() string        { return w.token }
func (w *webhook) Secret() string       { return w.secret }
func (w *webhook) TeamID() int          { return w.teamID }
func (w *webhook) CreatedAt() time.Time { return w.createdAt }
func (w *webhook) UpdatedAt() time.Time { return w.updatedAt }

func scanWebhook(row scannable) (Webhook, error) {
	w := &webhook{}
	var encryptedSecret sql.NullString
	var nonce sql.NullString
	err := row.Scan(&w.id, &w.name, &w.type_, &w.token, &encryptedSecret, &nonce, &w.teamID, &w.createdAt, &w.updatedAt)
	if err != nil {
		return nil, err
	}
	// Note: decryption of secret is handled in the query methods, not here.
	// The scanned values are the raw (possibly encrypted) values.
	w.secret = encryptedSecret.String
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

// SaveWebhook upserts a webhook for the team. A new token is generated on each call.
// An optional secret can be provided for HMAC signature validation.
// The secret is encrypted at rest using Concourse's encryption strategy.
func (t *team) SaveWebhook(name, webhookType, secret string) (Webhook, error) {
	token, err := generateWebhookToken()
	if err != nil {
		return nil, err
	}

	var encryptedSecret string
	var nonceStr *string
	if secret != "" {
		es := t.conn.EncryptionStrategy()
		encrypted, nonce, err := es.Encrypt([]byte(secret))
		if err != nil {
			return nil, err
		}
		encryptedSecret = encrypted
		nonceStr = nonce
	}

	var id int
	var createdAt, updatedAt time.Time
	err = psql.Insert("webhooks").
		Columns("name", "type", "token", "secret", "nonce", "team_id", "updated_at").
		Values(name, webhookType, token, encryptedSecret, nonceStr, t.id, sq.Expr("now()")).
		Suffix("ON CONFLICT (team_id, name) DO UPDATE SET type = EXCLUDED.type, token = EXCLUDED.token, secret = EXCLUDED.secret, nonce = EXCLUDED.nonce, updated_at = now()").
		Suffix("RETURNING id, created_at, updated_at").
		RunWith(t.conn).
		QueryRow().
		Scan(&id, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}

	return &webhook{
		id:        id,
		name:      name,
		type_:     webhookType,
		token:     token,
		secret:    secret, // return the plain text secret to the caller
		teamID:    t.id,
		createdAt: createdAt,
		updatedAt: updatedAt,
	}, nil
}

// DestroyWebhook deletes a webhook by name within the team.
func (t *team) DestroyWebhook(name string) error {
	_, err := psql.Delete("webhooks").
		Where(sq.Eq{
			"team_id": t.id,
			"name":    name,
		}).
		RunWith(t.conn).
		Exec()
	return err
}

// FindWebhook looks up a webhook by name within the team.
// The secret is decrypted transparently.
func (t *team) FindWebhook(name string) (Webhook, bool, error) {
	row := psql.Select("id", "name", "type", "token", "secret", "nonce", "team_id", "created_at", "updated_at").
		From("webhooks").
		Where(sq.Eq{
			"team_id": t.id,
			"name":    name,
		}).
		RunWith(t.conn).
		QueryRow()

	w := &webhook{}
	var encryptedSecret sql.NullString
	var nonce sql.NullString
	err := row.Scan(&w.id, &w.name, &w.type_, &w.token, &encryptedSecret, &nonce, &w.teamID, &w.createdAt, &w.updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}

	if encryptedSecret.Valid && encryptedSecret.String != "" {
		decrypted, err := t.conn.EncryptionStrategy().Decrypt(encryptedSecret.String, &nonce.String)
		if err != nil {
			// If decryption fails (e.g. no encryption configured), use raw value
			w.secret = encryptedSecret.String
		} else {
			w.secret = string(decrypted)
		}
	}

	return w, true, nil
}

// Webhooks returns all webhooks for the team.
func (t *team) Webhooks() ([]Webhook, error) {
	rows, err := psql.Select("id", "name", "type", "token", "secret", "nonce", "team_id", "created_at", "updated_at").
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
		w := &webhook{}
		var encryptedSecret sql.NullString
		var nonce sql.NullString
		err := rows.Scan(&w.id, &w.name, &w.type_, &w.token, &encryptedSecret, &nonce, &w.teamID, &w.createdAt, &w.updatedAt)
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
		webhooks = append(webhooks, w)
	}
	return webhooks, nil
}

// FindResourcesByWebhookPayload finds all resources in the team that are subscribed
// to a webhook with the given type and whose filter is contained within the payload
// (using Postgres JSONB containment @>).
func (t *team) FindResourcesByWebhookPayload(webhookType string, payload json.RawMessage) ([]Resource, error) {
	rows, err := resourcesQuery.
		Join("resource_webhook_subscriptions rws ON rws.resource_id = r.id").
		Where(sq.Eq{
			"rws.webhook_type": webhookType,
			"p.team_id":        t.id,
		}).
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
		err = scanResource(r, rows)
		if err != nil {
			return nil, err
		}
		resources = append(resources, r)
	}
	return resources, nil
}

// FindResourcesByWebhookType finds all resources in the team that are subscribed
// to a webhook with the given type (regardless of filter). This is used when
// matcher-based filtering is applied in the application layer instead of JSONB.
func (t *team) FindResourcesByWebhookType(webhookType string) ([]Resource, error) {
	rows, err := resourcesQuery.
		Join("resource_webhook_subscriptions rws ON rws.resource_id = r.id").
		Where(sq.Eq{
			"rws.webhook_type": webhookType,
			"p.team_id":        t.id,
		}).
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
		err = scanResource(r, rows)
		if err != nil {
			return nil, err
		}
		resources = append(resources, r)
	}
	return resources, nil
}

// saveResourceWebhookSubscriptions persists the webhook subscriptions from
// pipeline config into the resource_webhook_subscriptions table.
func saveResourceWebhookSubscriptions(tx Tx, resourceNameToID map[string]int, resources atc.ResourceConfigs) error {
	// Delete existing subscriptions for all resources in this pipeline save
	for _, id := range resourceNameToID {
		_, err := psql.Delete("resource_webhook_subscriptions").
			Where(sq.Eq{"resource_id": id}).
			RunWith(tx).
			Exec()
		if err != nil {
			return err
		}
	}

	// Insert new subscriptions
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
