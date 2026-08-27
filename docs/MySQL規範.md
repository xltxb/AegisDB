# MySQL規範

## 一、概述

MySQL 作為開源關聯型資料庫管理系統，相較於 Oracle、SQL Server 及 TiDB，有其特定優勢與劣勢

🔗 **請優先閱讀官方開發指南：**

[https://dev.mysql.com/doc/]()

## 二、分級

- 對於不滿足【高危】和【強制】兩個級別的設計，DBA **強制退回，要求修改**。

- 違反規範且拒絕修改者，開發方需承擔因此導致的系統故障或資料問題的責任。

**注意：所有設計必須明確符合【高危】及【強制】規範要求，任何模糊或忽視規範的行為均不被允許。**

### 【責任制度】

- **所有資料設計需符合高危/強制規範**，未達標者由 DBA **強制退回設計**。

- **若開發方拒絕修改**，需**自行承擔資料風險或系統異常責任**。

## 三、Mysql 特性\(禁用\)

## 四、命名與建模規範  

[https://dev.mysql.com/doc/refman/8.4/en/keywords.html]()



```SQL
###prod
create database c66_gi default character set utf8mb4;
###uat
create database uat_c66_gi default character set utf8mb4;
###fat
create database fat_c66_gi default character set utf8mb4;
```

## 五、表設計規範

### Table命名參考



```SQL
CREATE TABLE t_tests(
  `id` bigint(11) NOT NULL AUTO_INCREMENT,
  `customer_id` bigint(11) NOT NULL COMMENT '用户id',
  `username` varchar(45) NOT NULL COMMENT '真实姓名',
  `email` varchar(30) NOT NULL COMMENT '用户邮箱',
  `nickname` varchar(45) NOT NULL COMMENT '昵称',
  `create_time` datetime NOT NULL COMMENT '用户记录创建的时间',
  `update_time` datetime NOT NULL COMMENT '用户资料修改的时间',
  `user_review_status` tinyint NOT NULL COMMENT '用户资料审核状态，1为通过，2为审核中，3为未通过，4为还未提交审核',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_us_ci` (`customer_id`),
  KEY `idx_us_un`(`username`),
  KEY `idx_us_ct_urs`(`create_time`,`user_review_status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='用户基本信息';
```

- **\[強制\] Column Online DDL參照Online DDL Support for Column Operations**

- **\[強制\] Column Online DDL Modify**

```SQL
ALTER TABLE t_tests ADD COLUMN c1 VARCHAR(20), ALGORITHM=INSTANT;
ALTER TABLE t_tests ADD COLUMN c2 INT, ADD COLUMN c3 INT, ALGORITHM=INSTANT;
ALTER TABLE t_tests Modify COLUMN c1 VARCHAR(100) ALGORITHM=copy;  ###GHOST
```

### 補充規範建議 ALTER TABLE 大表操作/ Large Table ALTER TABLE Guidelines

#### **大表 \(\>10GB\)**

- 優先使用 **Instant/Inplace \&GHOST **，避免 rebuild table 或 full copy

- 如需 rebuild table → 建議**GHOST 並在**低峰期執行

#### **VARCHAR 增長**

- 0\~255 bytes → 可 Inplace

- ≥256 bytes → 可 Inplace 增長

- 跨 255→256 bytes → 必須 COPY，會鎖表 → 使用**GHOST **規劃維護窗口

#### **Column NOT NULL**

- 禁止直接大表修改not null   , 因為null convert to not null 需 full scan

- **自增 ID / auto\-increment**

    - INPLACE 調整初始值

    - Instant 不支持

- **一般建議**

    - 對大表操作前，**先備份表/欄位資料**

    - 修改前檢查現有資料長度或約束

    - 小表可直接操作，大表透過ghost 規劃維護窗口執行

- 



## 六、資料類型與性能規範 

## 七、索引設計規範

- **\[強制\]**主鍵索引名稱以 pk\_ 開頭

- **\[強制\]**唯一索引名稱以 uk\_ 開頭

- **\[強制\]**普通索引名稱以 idx\_ 開頭

- **\[強制\]**索引名稱不超過 32 個字元

- \[建議\]優先使用Composite Index, 減少回表查詢 ,區分度高置放索引左側

**命名範例：**

```SQL
CREATE TABLE t_users(
  `id` bigint(11) NOT NULL AUTO_INCREMENT,
  `customer_id` bigint(11) NOT NULL COMMENT '用户id',
  `username` varchar(45) NOT NULL COMMENT '真实姓名',
  `email` varchar(30) NOT NULL COMMENT '用户邮箱',
  `nickname` varchar(45) NOT NULL COMMENT '昵称',
  `create_time` timestamp NOT NULL COMMENT '用户记录创建的时间',
  `update_time` timestamp NOT NULL COMMENT '用户资料修改的时间',
  `user_review_status` tinyint NOT NULL COMMENT '用户资料审核状态，1为通过，2为审核中，3为未通过，4为还未提交审核',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_us_ci` (`customer_id`),
  KEY `idx_us_un`(`username`),
  KEY `idx_us_ct_urs`(`create_time`,`user_review_status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='用户基本信息';
```



```JavaScript
UNIQUE KEY `uk_us_ci` (`customer_id`),
 KEY `idx_us_un`(`username`),
 KEY `idx_us_ct_urs`(`create_time`,`user_review_status`)
```



- **\[強制\] 索引變更參照Online DDL Support for Index Operations**

```SQL
ALTER TABLE t_tests add INDEX idx_us_un(column_name) using btree;
```



- **\[強制\] pk參照Online DDL Support for Primary Key Operations**

```SQL
ALTER TABLE TEST DROP PRIMARY KEY, ADD PRIMARY KEY (*column*), ALGORITHM=INPLACE, LOCK=NONE;
ALTER TABLE TEST DROP PRIMARY KEY, ALGORITHM=COPY;
```



## 八、中間表與備份表

## 九、變更與運維建議

## 十、字符編碼

## 十一、安全與數據合規性

- **\[強制\]**禁止使用 DROP DATABASE /Drop Column/Drop Table在生產環境

- **\[強制\]**定期備份資料並驗證可用性

- **\[強制\]**所有表設計須經 DBA 審核與版本控管



