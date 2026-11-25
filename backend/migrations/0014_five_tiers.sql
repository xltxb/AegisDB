-- 0014: correct the built-in tier meanings, and add the missing 演练 tier.
--
-- The tier — not the environment's name — says which KIND of environment an
-- instance sits in, and every rule row is keyed by the tier. Two of the shipped
-- tiers were labelled with the wrong kind:
--
--   gli      灰度 (grey release)  →  法务环境   (legal)
--   staging  演练UAT              →  预发布环境 (pre-release)
--   dev      测试                 →  开发环境
--   uat      —                    →  演练环境   (new; 演练 had no tier of its own)
--
-- Only the LABELS were wrong. Every code is unchanged, so no rule row and no
-- connection moves: tbl_connection.env still holds a valid environment code and
-- tbl_role_capability.env / tbl_risk_command.env still hold valid tier codes.
-- What changes is what the console tells an operator they are about to touch.
--
-- gli keeps its existing (loose) control profile deliberately. It holds real
-- legal data, but tightening it here would change the verdict on instances that
-- are live right now; that is an operator's decision, and the tiers page can make
-- it. This migration corrects names and nothing else.
--
-- Every UPDATE below is guarded on the OLD value, so a tier an operator has
-- already renamed keeps their name. Fixing our own mistake is not a licence to
-- overwrite someone's deliberate edit.

UPDATE tbl_env_tier SET display_name = '法务环境 · GLI'    WHERE code = 'gli'     AND display_name = '灰度 · GLI';
UPDATE tbl_env_tier SET conn_layer   = 'L2 法务'           WHERE code = 'gli'     AND conn_layer   = 'L2 灰度';
UPDATE tbl_env_tier SET display_name = '预发布环境 · STAGING' WHERE code = 'staging' AND display_name = '演练UAT · STAGING';
UPDATE tbl_env_tier SET conn_layer   = 'L3 预发布'         WHERE code = 'staging' AND conn_layer   = 'L3 演练UAT';
UPDATE tbl_env_tier SET display_name = '开发环境 · DEV'    WHERE code = 'dev'     AND display_name = '测试 · DEV';

UPDATE tbl_environment SET display_name = '法务环境 · GLI'      WHERE code = 'gli'     AND display_name = '灰度 · GLI';
UPDATE tbl_environment SET display_name = '预发布环境 · STAGING' WHERE code = 'staging' AND display_name = '演练UAT · STAGING';
UPDATE tbl_environment SET display_name = '开发环境 · DEV'      WHERE code = 'dev'     AND display_name = '测试 · DEV';

-- The new tier, plus its identically-named environment.
INSERT INTO tbl_env_tier
  (code, display_name, sort_order, require_mfa, danger_banner, counts_in_pending, scan_baseline, conn_layer, default_role)
VALUES
  ('uat', '演练环境 · UAT', 3, 0, 0, 0, 0, 'L3 演练', 'dba_l2')
ON DUPLICATE KEY UPDATE code = code;

INSERT INTO tbl_environment (code, display_name, tier_code, sort_order)
VALUES ('uat', '演练环境 · UAT', 'uat', 3)
ON DUPLICATE KEY UPDATE code = code;

-- dev moves behind uat in display order (labels only — nothing reads sort_order
-- for a decision). Guarded on the old value like the renames above.
UPDATE tbl_env_tier    SET sort_order = 4 WHERE code = 'dev' AND sort_order = 3;
UPDATE tbl_environment SET sort_order = 4 WHERE code = 'dev' AND sort_order = 3;

-- uat's rules, cloned from staging — which is where the 演练 rules already lived,
-- back when staging was the tier wearing that label.
--
-- This is the whole reason a tier cannot be created empty: both rule lookups read
-- a missing row as permission granted, so a tier that exists without these rows
-- is an environment where any role may DROP TABLE unreviewed. INSERT … SELECT
-- with NOT EXISTS so re-running writes nothing and edits are never clobbered.
INSERT INTO tbl_role_capability (role_id, capability, env, level)
SELECT s.role_id, s.capability, 'uat', s.level
  FROM tbl_role_capability s
 WHERE s.env = 'staging'
   AND NOT EXISTS (
     SELECT 1 FROM tbl_role_capability d
      WHERE d.role_id = s.role_id AND d.capability = s.capability AND d.env = 'uat');

INSERT INTO tbl_risk_command (command, env, level)
SELECT s.command, 'uat', s.level
  FROM tbl_risk_command s
 WHERE s.env = 'staging'
   AND NOT EXISTS (
     SELECT 1 FROM tbl_risk_command d WHERE d.command = s.command AND d.env = 'uat');
