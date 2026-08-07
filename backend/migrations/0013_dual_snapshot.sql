-- 0013: record WHERE a command ran and WHAT it was judged under, separately.
--
-- Since 0012 those are two different things. tbl_approval.env held one of four
-- strings that meant both at once; it now holds an environment code, which can
-- be any group an operator creates (prod-hk, prod-sh) — hence the widening from
-- VARCHAR(16) to VARCHAR(32), matching tbl_environment.code.
--
-- The environment alone no longer explains the decision. An environment can be
-- rebound to another tier, and an instance can be moved to another environment;
-- after either, resolving the connection today reports a control level that was
-- never the one applied. So the tier in force at the time is stored alongside it
-- and neither value is ever rewritten. A record that follows the current
-- configuration is not an audit trail.
--
-- Empty tier_code means the row predates this split. Read it as unknown — the
-- environment's tier today is a guess about the past, not the answer.
ALTER TABLE tbl_approval
  MODIFY COLUMN env VARCHAR(32) NOT NULL,
  ADD COLUMN tier_code VARCHAR(16) NOT NULL DEFAULT '';

-- Same widening on the live column, for the same reason: tbl_connection.env now
-- holds an environment code. Sized 16 when the only legal values were the four
-- built-ins, it would silently truncate (or reject) a longer code an operator is
-- entitled to create — and a truncated code resolves to no environment, hence to
-- no tier, hence to no rules.
ALTER TABLE tbl_connection MODIFY COLUMN env VARCHAR(32) NOT NULL;

-- The audit log carries the same pair. It had no env column at all: a row said a
-- command was judged `high` without recording the control level that made it so.
--
-- Both columns join the chained hash payload from this release on, so they are
-- tamper-evident like every other audited field. Rows written earlier hash a
-- payload without these keys and keep their original hashes — a verifier must
-- key on the row's own era rather than re-hashing old rows with the new shape
-- (same caveat as 0008's operator column).
ALTER TABLE tbl_audit_log
  ADD COLUMN env VARCHAR(32) NOT NULL DEFAULT '',
  ADD COLUMN tier_code VARCHAR(16) NOT NULL DEFAULT '';
