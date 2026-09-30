DROP INDEX jobs_owner_idempotency_key;

ALTER TABLE jobs
    DROP COLUMN admission,
    DROP COLUMN cancel_requested_at,
    DROP COLUMN request_digest,
    DROP COLUMN idempotency_key;
