DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM generic_task_resume WHERE NOT completed) THEN
        RAISE EXCEPTION 'cannot remove generic task resume table while operations are unfinished';
    END IF;
END;
$$;
DROP TABLE generic_task_resume;
