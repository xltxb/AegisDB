# Oracle規範

## 一、概述

Oracle 是一套成熟的關聯式資料庫系統（RDBMS），支援 ACID 事務、複雜 SQL、分區、RAC 高可用架構與多層安全權限控制，適用於高可用、高一致性業務場景。為確保資料設計標準化、效能最優化與維運安全，特制訂本規範。

**請優先閱讀官方開發指南：**

[https://docs.oracle.com/en/database/oracle/oracle-database/]()

## 二、規範等級定義與責任制度

### 【責任制度】

- **所有資料設計需符合高危/強制規範**，未達標者由 DBA **強制退回設計**。

- **若開發方拒絕修改**，需**自行承擔資料風險或系統異常責任**。

## 三、功能特性限制

## 四、命名與建模規範【強制】

[**Keywords**](https://docs.oracle.com/en/database/oracle/oracle-database/19/sqlrf/Oracle-SQL-Reserved-Words.html)

## 五、表設計規範

### Table命名參考

## 六、資料類型與性能規範



## 七、索引設計規範



## 八、索引設計規範

## 九、中間表與備份表



## 十、變更與運維建議



### Oracle 19c — Online DDL Support Overview 

#### Oracle 19c Online DDL Features

#### Best Practice Examples  

In Oracle 12c\+ \(including 19c\), this is **instant**: no data rewrite, no locking

```SQL
- Add a NOT NULL column with default value online
ALTER TABLE employees ADD (status VARCHAR2(10) DEFAULT 'ACTIVE' NOT NULL);
-- Create index online without blocking DML
CREATE INDEX idx_emp_status ON employees(status) ONLINE;
-- Rebuild an index online
ALTER INDEX idx_emp_status REBUILD ONLINE;
```

#### `COMPUTE STATISTICS` Add index Online with updates optimizer stats immediately 

```SQL
CREATE INDEX idx_emp_dept ON employees(department_id) ONLINE COMPUTE STATISTICS;
```

## 十一、字符編碼



---

## 十二、安全與數據合規性



