-- 0010: per-user data-access scope.
--
-- Tags were grants on roles only, where "no tags" means unrestricted and the most
-- permissive role wins. That composes well for groups but cannot express "this
-- particular person": unioned with the role grants, a user-level tag would do
-- nothing for anyone whose role is already unrestricted — exactly the people most
-- often scoped. A user-level grant is therefore the MORE SPECIFIC statement and
-- REPLACES the role scope; a user with no rows here keeps the role behaviour, so
-- existing installs are unaffected until a tag is assigned.
CREATE TABLE IF NOT EXISTS tbl_user_tag (
  user_id BIGINT      NOT NULL,
  tag     VARCHAR(64) NOT NULL,
  PRIMARY KEY (user_id, tag)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
