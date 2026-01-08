-- Default stub for list_accessible_objects - replaced by generated dispatcher.
-- This stub denies all requests until migration runs with an authorization schema.
-- The generated dispatcher (from schema migration) will replace this function
-- to route to specialized list functions.
CREATE OR REPLACE FUNCTION list_accessible_objects(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT
) RETURNS TABLE (object_id TEXT) AS $$
BEGIN
    -- This stub is replaced by the generated dispatcher during migration.
    -- If you see this error, run the migration with your authorization schema.
    RAISE EXCEPTION 'list_accessible_objects: no authorization schema loaded - run migration first'
        USING ERRCODE = 'M0001';
END;
$$ LANGUAGE plpgsql STABLE;


-- Default stub for list_accessible_subjects - replaced by generated dispatcher.
-- This stub denies all requests until migration runs with an authorization schema.
-- The generated dispatcher (from schema migration) will replace this function
-- to route to specialized list functions.
CREATE OR REPLACE FUNCTION list_accessible_subjects(
    p_object_type TEXT,
    p_object_id TEXT,
    p_relation TEXT,
    p_subject_type TEXT
) RETURNS TABLE (subject_id TEXT) AS $$
BEGIN
    -- This stub is replaced by the generated dispatcher during migration.
    -- If you see this error, run the migration with your authorization schema.
    RAISE EXCEPTION 'list_accessible_subjects: no authorization schema loaded - run migration first'
        USING ERRCODE = 'M0001';
END;
$$ LANGUAGE plpgsql STABLE;
