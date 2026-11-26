-- 0066: 网关内申请数据查询权限(#173)。
--
-- tbl_query_grant_request:一次申请 —— 谁、哪个业务查询源的哪个库、要多少天、要不要导出、理由。
-- 它不单独记状态:状态就是它挂着的那张审批单的状态(待审批 / 通过 / 驳回 / 撤回 / 超时),
-- 撤回、超时都走审批单原有的路,不必在每条路上再同步一遍。批准后生成的授权记在 grant_id;
-- 批准时重新校验没通过(库没了、上限改小了)的原因记在 error。
--
-- tbl_approval.query_grant_req_id:这张审批单是哪次申请的。和 window_id、export_job_id 一样,
-- 它把单子挡在"手动执行"那条路之外 —— 单子的 command 是一句描述,不是 SQL。
--
-- CREATE TABLE IF NOT EXISTS 与单条 ALTER 都可以安全重跑(ADR 0016)。只增不删。
CREATE TABLE IF NOT EXISTS tbl_query_grant_request (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  user_id       BIGINT       NOT NULL,
  connection_id BIGINT       NOT NULL,
  db_name       VARCHAR(128) NOT NULL,
  days          INT          NOT NULL,
  allow_export  TINYINT(1)   NOT NULL DEFAULT 0,
  reason        VARCHAR(512) NOT NULL DEFAULT '',
  approval_id   BIGINT       NOT NULL DEFAULT 0,
  grant_id      BIGINT       NOT NULL DEFAULT 0,
  error         VARCHAR(512) NOT NULL DEFAULT '',
  created_at    DATETIME(3)  NULL,
  PRIMARY KEY (id),
  KEY idx_qgr_user (user_id),
  KEY idx_qgr_approval (approval_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE tbl_approval
  ADD COLUMN query_grant_req_id BIGINT NOT NULL DEFAULT 0,
  ADD INDEX idx_approval_qgr (query_grant_req_id);
