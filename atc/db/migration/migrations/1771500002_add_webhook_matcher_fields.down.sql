ALTER TABLE webhooks
    DROP COLUMN IF EXISTS signature_header,
    DROP COLUMN IF EXISTS payload_field,
    DROP COLUMN IF EXISTS source_pattern,
    DROP COLUMN IF EXISTS source_field;
