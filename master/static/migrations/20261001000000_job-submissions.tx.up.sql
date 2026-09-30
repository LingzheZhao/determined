ALTER TABLE jobs
    ADD COLUMN idempotency_key text,
    ADD COLUMN request_digest text,
    ADD COLUMN cancel_requested_at timestamptz,
    ADD COLUMN admission text NOT NULL DEFAULT 'QUEUE';

CREATE UNIQUE INDEX jobs_owner_idempotency_key ON jobs (owner_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
