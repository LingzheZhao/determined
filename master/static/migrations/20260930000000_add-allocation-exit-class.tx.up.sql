ALTER TABLE allocations
    ADD COLUMN exit_class text,
    ADD COLUMN exit_detail jsonb;
