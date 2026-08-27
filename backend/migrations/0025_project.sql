-- 0025: 项目(Project) —— 数据库与升级单的归属。
--
-- 它是**组织维度,不是安全边界**。系统已经有 Connection.Tags 做数据访问范围;
-- 再叠一层能限制访问的"项目",就有了两套互相重叠的范围机制,迟早有一层是错的,
-- 而错的那一层会以"本该看不见却看得见"的形式出现。判定层(能力矩阵 / 标签范围 /
-- 高危字典)完全不看项目 —— 它只回答"这个库、这张单归谁跟进"。
--
-- 名字即身份(唯一)。没有另立一个 code:项目是给人看的东西,而人是用名字指代它的。
CREATE TABLE IF NOT EXISTS tbl_project (
  id          BIGINT       NOT NULL AUTO_INCREMENT,
  name        VARCHAR(64)  NOT NULL,
  owner       VARCHAR(64)      NULL,   -- 负责人 / 团队,自由文本
  description VARCHAR(512)     NULL,
  created_by  BIGINT       NOT NULL DEFAULT 0,
  created_at  DATETIME(3)      NULL,
  updated_at  DATETIME(3)      NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_project_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 库的归属。归属的单位是**库**,不是数据源:一个实例底下的几个库分属不同团队是
-- 常态,把归属挂在实例上,等于逼着人按项目去拆实例 —— 让组织结构去改数据库拓扑,
-- 方向反了。
--
-- 库是**发现出来的**,系统里没有库的注册表(对象树是实时 introspect 的),所以这里
-- 按库名存 —— 与升级单记录目标库用的是同一个字符串。库以后被删掉,残留行无害:
-- 没有任何地方会列出它,也没有任何查询会命中它。
--
-- 没有行 = 未归属,是**合法状态**而不是待修的缺口:项目是后加的维度,存量库不该
-- 因为没人给它填归属就变得不可用。
CREATE TABLE IF NOT EXISTS tbl_database_project (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  connection_id BIGINT       NOT NULL,
  db_name       VARCHAR(128) NOT NULL,
  project_id    BIGINT       NOT NULL,
  created_by    BIGINT       NOT NULL DEFAULT 0,
  created_at    DATETIME(3)      NULL,
  updated_at    DATETIME(3)      NULL,
  PRIMARY KEY (id),
  -- 一个库只有一个归属:再定一次是覆盖,不是又添一条。
  UNIQUE KEY uk_db_project (connection_id, db_name),
  KEY idx_db_project_project (project_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 升级单的归属:提交时从目标库快照,和 env / tier_code / engine 一样。
--
-- 快照而不是每次 JOIN 现算,是因为库以后可能改挂到别的项目 —— 现算会让一次归属
-- 调整把过去所有单据的账一起改掉,而历史应当记录"当时归谁"。project_name 同样
-- 快照:项目改名或被删,历史单据仍读得出它当初挂在哪。
ALTER TABLE tbl_release ADD COLUMN project_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tbl_release ADD COLUMN project_name VARCHAR(64) NULL;
ALTER TABLE tbl_release ADD INDEX idx_release_project (project_id);
