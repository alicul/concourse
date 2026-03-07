ALTER TABLE webhooks
    ADD COLUMN source_field     TEXT NOT NULL DEFAULT '',
    ADD COLUMN source_pattern   TEXT NOT NULL DEFAULT '',
    ADD COLUMN payload_field    TEXT NOT NULL DEFAULT '',
    ADD COLUMN signature_header TEXT NOT NULL DEFAULT '';
