-- Returns jobs of managed-create kinds as the database records them, newest first, with their
-- tasks, allocations, and placements. Every filter is optional. A page starts after the job
-- (after_submitted_at, after_job_id), in the order (submitted_at DESC NULLS LAST, job_id DESC).
-- Task and allocation times are stored as UTC without a time zone.
WITH submissions AS (
    SELECT
        j.job_id,
        j.job_type,
        j.owner_id,
        u.username AS owner,
        j.idempotency_key,
        j.request_digest,
        j.admission,
        j.cancel_requested_at,
        t.task_id,
        t.task_state,
        t.end_time AT TIME ZONE 'UTC' AS task_end_time,
        cs.allocation_id AS attempt_id,
        e.id AS experiment_id,
        e.state AS experiment_state,
        e.end_time AS experiment_end_time,
        -- A task's workspace is in the spec it was launched with; an experiment's follows its
        -- project, which can move.
        CASE
            WHEN e.id IS NOT NULL THEN p.workspace_id
            ELSE COALESCE(
                (cs.generic_task_spec->>'WorkspaceID')::int,
                (cs.generic_command_spec->'Metadata'->>'workspace_id')::int
            )
        END AS workspace_id,
        COALESCE(e.project_id, (cs.generic_task_spec->>'ProjectID')::int) AS project_id,
        COALESCE(e.config->>'name', cs.generic_command_spec->'Config'->>'description') AS name,
        -- A deleted experiment leaves only its job row and its trials' tasks.
        COALESCE(
            t.start_time AT TIME ZONE 'UTC',
            e.start_time,
            (SELECT min(dt.start_time) FROM tasks dt WHERE dt.job_id = j.job_id) AT TIME ZONE 'UTC'
        ) AS submitted_at
    FROM jobs j
    LEFT JOIN users u ON u.id = j.owner_id
    LEFT JOIN tasks t ON t.job_id = j.job_id AND j.job_type IN ('COMMAND', 'SHELL', 'GENERIC')
    LEFT JOIN command_state cs ON cs.task_id = t.task_id
    LEFT JOIN experiments e ON e.job_id = j.job_id AND j.job_type = 'EXPERIMENT'
    LEFT JOIN projects p ON p.id = e.project_id
    WHERE j.job_type IN ('COMMAND', 'SHELL', 'GENERIC', 'EXPERIMENT')
        AND (?job_id::text IS NULL OR j.job_id = ?job_id::text)
        AND (?owner_id::int IS NULL OR j.owner_id = ?owner_id::int)
        AND (?job_type::text IS NULL OR j.job_type::text = ?job_type::text)
)
SELECT
    s.*,
    (
        SELECT COALESCE(JSONB_AGG(JSONB_BUILD_OBJECT(
            'task_id', tk.task_id,
            'trial_id', rt.run_id,
            'allocations', (
                SELECT COALESCE(JSONB_AGG(JSONB_BUILD_OBJECT(
                    'allocation_id', a.allocation_id,
                    'state', a.state,
                    'is_ready', a.is_ready,
                    'start_time', a.start_time AT TIME ZONE 'UTC',
                    'end_time', a.end_time AT TIME ZONE 'UTC',
                    'slots', a.slots,
                    'resource_pool', a.resource_pool,
                    'exit_reason', a.exit_reason,
                    'exit_error', a.exit_error,
                    'status_code', a.status_code,
                    'exit_class', a.exit_class,
                    'exit_detail', a.exit_detail,
                    'placements', (
                        SELECT COALESCE(JSONB_AGG(JSONB_BUILD_OBJECT(
                            'node', aa.node_name,
                            'accelerator_uuids', COALESCE(aa.accelerator_uuids, '{}')
                        ) ORDER BY aa.id), '[]'::jsonb)
                        FROM allocation_accelerators aa
                        WHERE aa.allocation_id = a.allocation_id
                    )
                -- Allocation IDs end in the attempt's ordinal.
                ) ORDER BY
                    substring(a.allocation_id FROM '\.(\d+)$')::int NULLS FIRST,
                    a.allocation_id
                ), '[]'::jsonb)
                FROM allocations a
                WHERE a.task_id = tk.task_id
            )
        ) ORDER BY tk.start_time, tk.task_id), '[]'::jsonb)
        FROM tasks tk
        LEFT JOIN run_id_task_id rt ON rt.task_id = tk.task_id
        WHERE tk.job_id = s.job_id
    ) AS tasks
FROM submissions s
WHERE (?submitted_after::timestamptz IS NULL OR s.submitted_at > ?submitted_after::timestamptz)
    AND (
        ?after_job_id::text IS NULL
        OR (
            ?after_submitted_at::timestamptz IS NOT NULL
            AND (
                s.submitted_at < ?after_submitted_at::timestamptz
                OR s.submitted_at = ?after_submitted_at::timestamptz
                    AND s.job_id < ?after_job_id::text
                OR s.submitted_at IS NULL
            )
        )
        OR (
            ?after_submitted_at::timestamptz IS NULL
            AND s.submitted_at IS NULL
            AND s.job_id < ?after_job_id::text
        )
    )
ORDER BY s.submitted_at DESC NULLS LAST, s.job_id DESC
LIMIT ?limit::int
