-- 0027: 敏感字段维护 —— 按 表名 + 字段名 定义,查询结果在**离开网关之前**打码。
--
-- 为什么脱敏必须在服务端做:一个"前端负责打码"的设计,等于把敏感数据完整地发到了
-- 浏览器,再请浏览器不要显示 —— 抓个包、开个 DevTools 就绕过了,而且它已经躺在
-- 浏览器缓存和沿途任何代理的日志里了。回传的数据本身就是打码后的,这是这张表存在
-- 的全部意义。
--
-- table_name 为 '*' 表示所有表:口令、密钥这类字段,在哪张表上都不该被看到。
--
-- 它防的是**顺手看到**,不是防外泄。一个铁了心要拿数据的人总能构造出映射不回去的
-- 表达式(经子查询再套一层别名是最简单的一种)。真正的边界是不给这张表的访问权限
-- —— 标签与角色才是那道墙。详见 gateway/sensitive.go 的说明。
CREATE TABLE IF NOT EXISTS tbl_sensitive_column (
  id          BIGINT       NOT NULL AUTO_INCREMENT,
  table_name  VARCHAR(128) NOT NULL,             -- '*' = 所有表
  column_name VARCHAR(128) NOT NULL,
  mask_style  VARCHAR(16)  NOT NULL DEFAULT 'partial', -- partial|full|hash
  enabled     TINYINT(1)   NOT NULL DEFAULT 1,
  note        VARCHAR(255)     NULL,             -- 为什么它是敏感的,给后来人看
  created_by  BIGINT       NOT NULL DEFAULT 0,
  created_at  DATETIME(3)      NULL,
  updated_at  DATETIME(3)      NULL,
  PRIMARY KEY (id),
  -- 同一张表的同一个字段只有一条规则:再定一次是改,不是又添一条。
  UNIQUE KEY uk_sensitive_col (table_name, column_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
