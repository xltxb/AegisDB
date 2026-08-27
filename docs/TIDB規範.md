# TIDB規範

## 一、概述

TiDB 是一款分布式 HTAP（Hybrid Transactional/Analytical Processing）資料庫，兼容 MySQL 協議，但底層採用自研分布式儲存與計算架構，具備良好的橫向擴展能力與強一致性。

為確保應用開發、資料建模及運營維護的穩定性與可擴展性，特制訂本規範。

🔗 **請優先閱讀官方開發指南：**

[https://docs.pingcap.com/zh/tidb/stable/dev-guide-overview]()

---

## 二、分級

- 

- 此導致專案延期責任

**注意：所有設計必須明確符合【高危】及【強制】規範要求，任何模糊或忽視規範的行為均不被允許。**

### 【責任制度】

- **所有資料設計需符合高危/強制規範**，未達標者由 DBA **強制退回設計**。

- **若開發方拒絕修改**，需**自行承擔資料風險或系統異常責任**。

## 三、TiDB 特性與限制\(高危,不支持\)

---





## 四、命名與建模規範  

### 禁用TiDB Keywords\(第一次提醒\):

[https://docs.pingcap.com/zh/tidb/stable/keywords/]()

### 開發人員常誤用 SQL 關鍵字（Keywords）

### 命名規則



**建庫範例：  **

```SQL
CREATE DATABASE c66_dc DEFAULT CHARACTER SET utf8mb4;
```



---

## 五、表設計規範

### 禁用TiDB Keywords:\(第二次提醒\)

[https://docs.pingcap.com/zh/tidb/stable/keywords/]()

### Table命名參考



### 基本要求**\[強制\]**

- **\[強制\]**默认情况下所有的表都是clustered 表，如果表数据量很少，可以创建nonclustered 表，nonclusterd表主键有程序自己控制写入

- **\[強制\]**所有表名以 t\_ 為前綴，表名與欄位均使用小寫英文字母與底線

- **\[強制\]**表名稱不超過 32 個字元

- **\[強制\]**所有欄位必須 NOT NULL，除非業務明確需要允許 NULL（如 datetime 類欄位）

### 主鍵規範

- **\[強制\] 使用**高頻UK做為主鍵,若無高頻UK,則PK 為選擇 id BIGINT PRIMARY KEY，並使用 AUTO\_RANDOM

- **\[強制\] id BIGINT PRIMARY KEY **不得使用 AUTO\_INCREMENT

- **\[強制\] **自增值不應由業務系統指定，由 TiDB 自動產生

### 時間欄位與 2038 問題

- **\[強制\]**明確使用 DATETIME 類型（避免使用TIMESTAMP，Year 2038 problem溢出）

    - \[建議\]標準時間欄位：

        

        ```SQL
        create_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
        update_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
        ```

### clustered表创建表示例

```SQL
CREATE TABLE user_profile (
  id             BIGINT PRIMARY KEY /*T![auto_rand] AUTO_RANDOM(5) */ comment 'pk',
  username       VARCHAR(50) NOT NULL comment '用户名',
  email          VARCHAR(100) NOT NULL default '' comment '邮件地址',
  phone          VARCHAR(20) not null default '' comment '手机号码',
  gender         TINYINT UNSIGNED NOT NULL DEFAULT 0 comment '性别',  -- 0未知,1男,2女
  create_time    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP comment '创建时间',
  update_time    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP comment '更新时间',
  is_deleted     TINYINT(1) NOT NULL DEFAULT 0 comment '是否删除'，
PRIMARY KEY (`ID`) /*T![clustered_index] CLUSTERED */
)DEFAULT CHARSET = utf8mb4 comment='用户属性表';
```

### nonclustered表创建表示例

```SQL
CREATE TABLE user_profile (
  id             BIGINT PRIMARY KEY  comment 'pk',
  username       VARCHAR(50) NOT NULL comment '用户名',
  email          VARCHAR(100) NOT NULL default '' comment '邮件地址',
  phone          VARCHAR(20) not null default '' comment '手机号码',
  gender         TINYINT UNSIGNED NOT NULL DEFAULT 0 comment '性别',  -- 0未知,1男,2女
  create_time    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP comment '创建时间',
  update_time    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP comment '更新时间',
  is_deleted     TINYINT(1) NOT NULL DEFAULT 0 comment '是否删除'，
PRIMARY KEY (`ID`) /*T![clustered_index] NONCLUSTERED */,
)DEFAULT CHARSET = utf8mb4 comment='用户属性表';
```

---

## 六、資料類型與性能規範 

---

## 七、索引設計規範

- **\[強制\]**主鍵索引名稱以 pk\_ 開頭

- **\[強制\]**唯一索引名稱以 uk\_ 開頭

- **\[強制\]**普通索引名稱以 idx\_ 開頭

- \[**強制**\]優先使用覆蓋索引,減少回表查詢

- **索引範例：**

```sql
ALTER TABLE t_seamless_launch ADD INDEX idx_tsl_ln_t_ct(login_name, token, created_time ) ;
```



---

## 八、中間表與備份表

---

## 九、變更與運維建議

- **\[強制\] **DDL操作必須審批 

- **\[強制\] **超過 100 萬行**表**需要明確定義下線歸檔週期

- **\[強制\] **所有建表、索引操作均應先經過測試驗證

- **\[建議\] **大型業務表開啟 TiFlash 副本以加速查詢

---

## 十、安全與數據合規性

- **\[強制\]**禁止使用 DROP DATABASE /Drop Column在生產環境

- **\[強制\]**定期備份資料並驗證可用性

- **\[強制\]**所有表設計須經 DBA 審核與版本控管



## 十一、開發SQL編寫規範,權責  

### 編寫建議與規範

#### 效能管理

#### SQL 編寫與開發

#### 測試與效能驗證



### SQL設計編寫檢查表

#### 撰寫前規劃階段

#### SQL 編寫與開發階段

#### 效能測試與 Review 階段

#### 上線與維運階段

### 責任歸屬總覽表（可導入專案流程）

### 分库分表，分区表

TIDB属于分布式数据库，可以存储海量数据，不需要考虑分库分表,當前分區表功能在TIDB上并不完善。\(8\.5版本\)



