ALTER TABLE webhooks
    DROP COLUMN IF EXISTS signature_header,
    DROP COLUMN IF EXISTS matcher_rules,
    ADD COLUMN source_field     TEXT NOT NULL DEFAULT '',
    ADD COLUMN source_pattern   TEXT NOT NULL DEFAULT '',
    ADD COLUMN payload_field    TEXT NOT NULL DEFAULT '',
    ADD COLUMN signature_header TEXT NOT NULL DEFAULT '';
