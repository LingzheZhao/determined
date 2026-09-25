CREATE TABLE generic_task_resume (
    root_task_id text NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    operation_id text NOT NULL,
    task_id text PRIMARY KEY REFERENCES tasks(task_id) ON DELETE CASCADE,
    old_allocation_id text NOT NULL,
    new_allocation_id text NOT NULL,
    ordinal integer NOT NULL,
    completed boolean NOT NULL DEFAULT false,
    canceled boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX generic_task_resume_root_idx ON generic_task_resume(root_task_id, operation_id);
