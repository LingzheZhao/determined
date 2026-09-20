CREATE TABLE dynamic_resource_pools (
    cluster_name TEXT NOT NULL,
    pool_name TEXT NOT NULL UNIQUE,
    config_version INTEGER NOT NULL,
    idempotency_key TEXT NOT NULL,
    config JSONB NOT NULL,
    config_hash TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('Pending', 'Ready', 'Failed')),
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (cluster_name, idempotency_key)
);

