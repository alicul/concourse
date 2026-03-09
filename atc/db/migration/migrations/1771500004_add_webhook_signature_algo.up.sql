ALTER TABLE webhooks
    ADD COLUMN signature_algo TEXT NOT NULL DEFAULT 'hmac-sha256';
