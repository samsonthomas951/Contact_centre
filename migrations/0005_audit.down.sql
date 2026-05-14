DROP TABLE IF EXISTS audit_anchors;
DROP TABLE IF EXISTS audit_events_default;
DROP TABLE IF EXISTS audit_events;

-- Roles are cluster-wide; drop their privileges in *this* database first.
-- DROP ROLE will still error if the role owns objects in another database,
-- which is the desired safety: a tenant DB shouldn't silently drop a role
-- another tenant relies on.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_writer') THEN
    EXECUTE 'DROP OWNED BY audit_writer';
    DROP ROLE audit_writer;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_reader') THEN
    EXECUTE 'DROP OWNED BY audit_reader';
    DROP ROLE audit_reader;
  END IF;
EXCEPTION
  WHEN dependent_objects_still_exist THEN
    RAISE NOTICE 'audit roles retained: still referenced by another database';
END$$;
