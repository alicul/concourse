CREATE TABLE webhooks (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    token TEXT NOT NULL,
    team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(team_id, name)
);

CREATE TABLE resource_webhook_subscriptions (
    id SERIAL PRIMARY KEY,
    resource_id INTEGER NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    webhook_type TEXT NOT NULL,
    filter JSONB NOT NULL DEFAULT '{}',
    UNIQUE(resource_id, webhook_type, filter)
);

CREATE INDEX idx_resource_webhook_subs_type ON resource_webhook_subscriptions(webhook_type);
