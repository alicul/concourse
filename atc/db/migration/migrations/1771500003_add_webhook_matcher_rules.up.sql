-- Replace the 4 flat matcher columns with a single JSONB column for the rule list.
-- The signature_header is kept flat since it's used for auth (not per-rule).
ALTER TABLE webhooks
    DROP COLUMN IF EXISTS source_field,
    DROP COLUMN IF EXISTS source_pattern,
    DROP COLUMN IF EXISTS payload_field,
    DROP COLUMN IF EXISTS signature_header,
    ADD COLUMN matcher_rules    JSONB NOT NULL DEFAULT '[]',
    ADD COLUMN signature_header TEXT  NOT NULL DEFAULT '';
