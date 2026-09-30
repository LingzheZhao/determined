SELECT
    t.task_id,
    CONCAT('TASK_TYPE_', t.task_type) AS task_type,
    t.start_time,
    t.end_time,
    CASE WHEN t.task_state is NULL THEN NULL
    ELSE CONCAT('GENERIC_TASK_STATE_', t.task_state)
    END as task_state,
    (
        SELECT
            COALESCE(
                JSONB_AGG(
                    allo
                    ORDER BY end_time DESC NULLS FIRST
                ),
                '[]'::jsonb
            )
        FROM (
            SELECT
                allocation_id,
                task_id,
                is_ready,
                start_time,
                end_time,
                (
                    CASE
                        WHEN state IN ('PENDING', 'ASSIGNED') THEN 'STATE_QUEUED'
                        ELSE 'STATE_' || state
                    END
                ) AS state,
                slots,
                exit_reason,
                status_code,
                'EXIT_CLASS_' || NULLIF(exit_class, '') AS exit_class,
                exit_detail
            FROM allocations
            WHERE allocations.task_id = t.task_id
        ) allo
    ) AS allocations
FROM tasks t
WHERE t.task_id = $1;
