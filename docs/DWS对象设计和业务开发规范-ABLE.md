**DWS对象设计和业务开发规范**

## **概述**

### **目的**

- Huawei DWS（数据仓库服务 Data Warehouse Service）为公司使用的分布式数仓 /分析型数据库服务，采用存算分离、列存/行存结合架构，主要承载报表分析、数仓计算、通查等场景。为保证建模设计与 SQL 开发的一致性与可维护性，统一设计思路、编码风格，提高输出工作产品的质量，特制定本规范。
- This is the company-wide distributed data warehouse / analytical database service, supporting reporting, warehouse computation, and ad-hoc queries.
### **适用范围**

本规范适用于公司基于 Huawei DWS（华为云分布式数据仓库服务）设计与开发的数据系统。
本规范适用于：Applies to:
- 所有基于 DWS 的新建库表、视图、索引等对象；New tables, views, indexes in Huawei DWS
- 既有系统迁移至 DWS 时的新建或重构对象；Newly created or refactored objects during system migration to DWS
- 所有 DWS ETL 调度任务 / BI 报表 / 分析 SQL；ETL jobs, scheduling tasks, BI reports, and analytical SQL accessing DWS
- 基于 DWS 数据库的自主开发新产品建设、老产品中新模块建设，包括 DWS 数据库层面的设计与开发，以及应用层面涉及 DWS 数据库的设计与开发。
在文中，DWS 会指代华为的分布式数据仓库服务，而 DWS 层 则是指数据仓库的各层结构。
In this document, DWS refers to the Huawei Cloud Distributed Data Warehouse Service, while DWS Layers refer to the different levels of data processing in the data warehouse.
- 参考链接 / Reference:
- 
### **分级**

为便于执行、风险管理与责任划分，本规范将所有条目划分为三个等级：
For the purpose of implementation, risk management, and responsibility allocation, this specification divides all items into three levels:
映射说明 / Mapping Note：本规范的分级体系与规范类型”Must”和”Should”的对应关系如下——“强制”对应”Must”（任何情况下必须强制执行），“建议”对应”Should”（通常情况下必须被遵循，例外需在评审中说明合理原因）。
#### **高危 / Critical**** ****/**** Must**

涉及 数据错误、不可恢复风险、集群不稳定或严重性能问题 的行为。此类行为 严禁出现，DBA 有权 强制退回 设计与 SQL，并中止上线流程。
Critical: Actions that may cause data errors, unrecoverable risks, cluster instability, or severe performance issues. Strictly prohibited. DBA may reject the design or block deployment.
#### **强制 / ****Mandatory**** / ****Should**

必须遵守的设计与开发要求。违反可能造成 明显风险、治理成本上升或影响可维护性，需修改后方可上线。
Mandatory: Requirements that must be followed. Violations introduce significant risk or governance overhead and must be corrected before deployment. This level corresponds to “Must” in the specification type — it must be enforced under any circumstances.
#### **建议 / Recommended**

业界最佳实践与优化方向。应尽量遵循；如需例外，需在设计评审中说明合理原因。
Recommended: Best practices. Should be followed whenever possible. Exceptions must be justified during design review. This level corresponds to “Should” in the specification type — it must generally be followed, and any deviation requires documented justification.
#### **责任声明 / Responsibility Notice**

所有 DWS 相关设计、开发与调整，均需至少符合 “高危”与”强制”两类规范。
如因业务方拒绝修改或违规实施，导致 系统故障、性能问题、数据错误、治理成本上升或项目延期，其责任由业务系统自行承担。
All DWS-related designs and changes must meet at least the Critical and Mandatory requirements. If violations result in system failures, performance degradation, data issues, or project delays, the responsible business owner must assume full accountability.
## **DWS特性与限制（高危/不支持）**

| 禁止项目 / Prohibited | 说明 / Description |
| --- | --- |
| JSON 类型 / JSON type | 函数与运算符支持有限，不利于分布式计算 / Limited support, not suitable for distributed computation |
| 自定义数据类型 / Custom data types | 包括 ENUM 等，增加迁移与计算复杂度 / Includes ENUM, increases migration and computation complexity |
| HLL 类型 / HLL type | 不支持 / Not supported |
| MONEY 类型 / MONEY type | 不支持 / Not supported |
| unlogged 表 / Unlogged table | 不支持 / Not supported |
| 触发器 / Triggers | 不支持 / Not supported |
| 自定义外部函数 / C/Java UDFs | 不支持 / Not supported |

## **DWS对象命名规范**

### **对象命名通用规则 /**** Naming Rules**

##### RULE 1 【Must】不使用关键字命名数据库对象 / Must not use reserved keywords as database object names.

- 说明：可以通过SELECT word, catcode, catdesc FROM pg_get_keywords()确认关键字范围
##### RULE 2 【Must】DWS 对象名限定使用小写字母、下划线、数字这三类字符，起始字符必须是字母。 / DWS object names must use only lowercase letters, underscores, and digits. The starting character must be a letter.

##### RULE 3 【Must】DWS 对象名禁止以 pg、gs、mlog、redis 开头。 / DWS object names must not start with pg, gs, mlog, or redis.

##### RULE 4 【Should】临时/中间计算表以 YYYYMMDD 日期（备份历史表）结尾命名，如 **_20251202**，必须明确清理策略。

##### RULE 5 【Must】对象命名长度不可超过 63 字节。 / Database object names must not exceed 63 bytes in length.

- 说明：超过63字符会被截断，可能出现预期外的现象。

| 对象类型 / Object Type | 长度约束 / Length Constraint (bytes) | 备注 / Remarks |
| --- | --- | --- |
| 数据库名 / Database name | <=63 | 命名应反映整个系统的功能，建议使用小写，关键字之间使用下划线隔开 / Should reflect system functionality; lowercase with underscores between keywords. |
| 逻辑集群名 / Logic cluster name | <=63 | 命名应反映整个逻辑集群的功能，建议使用小写，以 lc_应用名，如 "lc_lake" / Should reflect logical cluster functionality; lowercase, prefix with lc_. |
| Schema 名 / Schema name | <=63 | 命名应反映该功能模块的功能，建议使用小写，关键字之间使用下划线隔开 / Should reflect module functionality; lowercase with underscores. |
| 表 / Table | <=63 | g_分层_业务域_业务表名_后缀 的方式进行命名，建议使用小写，例如 "g_ods_big_xxxx_di" / Format: g_ods_big_xxxx_di; lowercase. |
| 视图 / View | <=63 | g_分层_业务域_视图名_v/mtv 的方式进行命名，建议使用小写，例如 "g_ods_big_xxxx_v" / Format: g_ods_big_xxxx_v; lowercase. |
| 索引 / Index | <=63 | pk/uk/idx_索引名 的方式进行命名，建议使用小写，例如 "pk/uk/idx_indexname" / Format: pk/uk/idx_indexname; lowercase.—根据业务习惯刷新 |
| 存储过程 / Stored procedure | <=63 | g_应用模块_存储过程名_proc 的方式进行命名，建议使用小写，例如 "g_voucher_proc" / Format: g_module_proc_proc; lowercase. |
| 函数 / Function | <=63 | g_应用模块_函数名_func 的方式进行命名，建议使用小写，例如 "g_voucher_func" / Format: g_module_func_func; lowercase. |
| 序列 / Sequence | <=63 | 应用模块_序列名_seq 的方式进行命名，建议使用小写，例如 "g_voucher_seq" / Format: g_module_seq_seq; lowercase. |

### **主题域代码 / Domain Codes**

| 代码 / Code | 说明 / Description (zh) | 说明 / Description (en) |
| --- | --- | --- |
| big | 大数据域 | Big Data Domain |
| basi | 基础数据域 | Basic / Foundation Domain |
| game | 游戏业务域 | Game Business Domain |
| mkit | 市场 / 营销域 | Marketing & Acquisition Domain |
| opra | 营销运营域 | Operation & Promotion Domain |
| pay | 支付域 | Payment Domain |
| risk | 风控域 | Risk Control Domain |
| usr | 用户域 | User Domain |
| ap / bp | 各产品线（A线 / B线等） | Product Line Domains |

### **频率代码**

| 代码 / Code | 说明 / Description (zh) | 说明 / Description (en) |
| --- | --- | --- |
| d | 日 | daily |
| w | 周 | weekly |
| m | 月 | monthly |
| y | 年 | yearly |
| h | 小时 | hourly |
| r | 实时 | realtime |

### **模式**

| 代码 / Code | 说明 / Description (zh) | 说明 / Description (en) |
| --- | --- | --- |
| f | 全量 | full extraction |
| i | 增量 | incremental extraction |

### **后缀**

| 后缀 / Suffix | 说明 / Description (zh) | 说明 / Description (en) | 备注 / Remark |
| --- | --- | --- | --- |
| di | T+1 更新（日增量） | daily incremental update (T+1) |  |
| ri | 实时更新 | real-time update / trial incremental |  |
| mtv | 物化视图 | Materialized View |  |
| v | 普通视图 | View |  |

### **来源形态**

| 代码 / Code | 说明 / Description (zh) | 说明 / Description (en) | 备注 / Remarks |
| --- | --- | --- | --- |
| o | 来源系统的单一业务表 | Single table extracted directly from source system |  |
| w | (弃用) 来源系统多表关联后的宽表 | 非法命名 / Illegal naming | (Deprecated) DWS is already a wide table; no need for additional joins. |

**注释**** / Note**：每个表必须有注释，明确表用途。 / Every table must have a comment specifying its purpose.

## **DWS对象设计规范**

### **数据库设计**

##### RULE 6 【Must】禁止应用使用集群默认的数据库，包括创建对象。 / Applications must not use the cluster’s default database, including creating objects in it.

##### RULE 7 【Must】一个 DWS 集群中只能有 1 个自定义数据库。 / A DWS cluster can have only one custom database.

##### RULE 8 【Must】创建数据库时必须指定字符集为 UTF8。 / When creating a database, the character set must be specified as UTF8.

##### RULE 9 【Must】创建数据库时，必须选择 dbcompatibility 属性为 MySQL。 / When creating a database, dbcompatibility must be set to MySQL.

##### RULE 10 【Must】禁止自定义表空间。 / Custom tablespaces are prohibited.

### **表设计规范**

#### **表结构****设计**

##### RULE 11 【Must】表结构创建一律采用列存hstore_opt 3.0表。

- 说明：表定义中必须包含enable_hstore_opt=**true**,colversion=**3.****0**，建表示例：
**CREATE** **TABLE** dfm.t1 (
    carno  BIGINT,
    name   TEXT,
    gender CHAR(1)
) **WITH** (
    orientation**=****column**,enable_hstore_opt=**true**,colversion=**3.0**
) DISTRIBUTE **BY** **HASH**(carno);
##### RULE 12 【Must】禁止将百万以上的大表设为复制表（distribute by replication）。只有小于 100 万行的表才能创建为复制表，且一般只建议小维表和补录表使用 replication 分布方式。禁止将事实表设为复制表。

- 说明：复制表在每个节点都要存一份数据，大表建复制表会导致更多的内存与磁盘空间开销。
#### **分布键****设计**

##### RULE 13 【Must】分布键字段数不超过3个。组合分布键在保证数据分布均匀的前提下，字段总数越少越好。

##### RULE 14 【Must】分布键字段集必须是该表唯一键、主键约束的子集，优先选择离散性高的列。

- 说明：保证数据分布均匀的基础上，分布键建议设置为一个业务强相关的字段，该字段在大多数场景下被用于join、group by等场景，不允许选择枚举类型。
##### RULE 15 【Should】主键约束不允许超过5个字段，其中每个字段长度不允许超过 128。

##### RULE 16 【Must】分布键字段的数据类型必须归属下面指定的数据类型列表：

- TINYINT、SMALLINT、INTEGER、BIGINT、NUMBER、CHAR、VARCHAR、VARCHAR2
##### RULE 17 【Must】禁止使用 UUID 系统函数作为分布列的默认赋值。

- 说明： GaussDB 的 uuid_generate_v1()、uuid()、sys_guid() 等函数会占用较高的计算资源。批量插入几千万行的数据时，会导致硬件资源消耗较高；如同一时间多张表同时插入，极易造成资源瓶颈。
#### **分区键设计**

##### RULE 18 【Should】分区主要用于数据生命周期管理（按天/月/年等）、提高剪枝能力（Partition Pruning）。分区键优先使用：账期（period_id）、交易日期（biz_date）等。

##### RULE 19 【Must】分区键只能指定一个字段，使用range分区。

- 说明：目的是避免把分区划分得太小，同时多字段分区键在实际应用中很难进行分区剪枝，无法充分发挥分区表作用。
##### RULE 20 【Must】单表分区数不超过 1000 个，防止过多小文件与锁竞争。

- 说明： 按 60 个物理节点、120 个 DN 计算，10 亿记录的表，平均每个 DN 的数据量是 10亿/120 = 833 万。500 个分区，每个分区才 1.6 万记录，每个 CU 6 万记录，才1个 CU。可见在 MPP 架构下，分区不需要太多，分得太多会产生大量的小文件。如果 SQL 执行过程中无法分区剪枝，应用高并发访问的情况下有可能会出现大量锁等待，影响应用性能。
##### RULE 21 【Must】分区表创建时指定ttl属性或增加drop partition及时淘汰历史分区，禁止无限累积。

- 说明：分区数据归档，确保数据无误后，应及时删除历史分区。
#### **字段类型设计**

##### RULE 22 【Should】字段设计应优先选用推荐类型。

| 数据类型 | 说明 | 是否推荐 |
| --- | --- | --- |
| UUID | 不同数据库可能产生相同 UUID | 推荐 |
| 整数类型 | TINYINT, SMALLINT, INTEGER, BIGINT | 推荐 |
| 任意精度类型 | NUMERIC / DECIMAL | 推荐 |
| 布尔类型 | BOOLEAN | 推荐 |
| 定长字符 | CHAR(n) | 推荐 |
| 变长字符 | VARCHAR(n), NVARCHAR2(n) | 推荐 |
| 时间类型 | DATE, TIMESTAMP | 推荐 |
| 含时区时间 | TIMESTAMPTZ | 推荐 |

##### RULE 23 【Should】以下类型禁止使用。如需使用禁用或不推荐的字段类型，建议联系 DWS 数据库专家进行评估。这些数据类型不被推荐的原因是业务使用场景较少，可能会存在性能风险。

| 数据类型 | 说明 | 是否推荐 |
| --- | --- | --- |
| BLOB（二进制大对象）, RAW（变长十六进制） | 大二进制类型 | 不推荐 |
| 浮点类型 | REAL / FLOAT4, DOUBLE PRECISION / FLOAT8, FLOAT | 不推荐 |
| 序列整型 | 即自增列，包括 SMALLSERIAL, SERIAL, BIGSERIAL | 不推荐 |
| 特殊字符类型 | NAME, “CHAR”，通常供数据库系统内部使用 | 不推荐 |
| JSON 类型 | 目前支持的操作符和函数较少 | 不推荐 |
| 自定义类型 | 可用于定义枚举 EMU 等类型 | 不推荐 |
| HLL 数据类型 | 建议直接使用 HLL 相关函数，减少性能影响 | 不推荐 |
| 货币类型 | MONEY，存储带有固定小数精度的货币金额 | 不推荐 |
| 几何类型 | POINT, LSEG, BOX, PATH, POLYGON, CIRCLE | 不推荐 |
| 网络地址类型 | 存储 IPv4 / MAC 地址数据类型 | 不推荐 |
| 文本搜索类型 | 用于支持全文检索 | 不推荐 |

##### RULE 24 【Must】对于明确不存在 NULL 值的字段必须加上 NOT NULL 约束。

##### RULE 25 【Must】NUMERIC、DECIMAL等数据类型必须指定精度。

##### RULE 26 【Should】字符类型字段不应存储数字类型的数据。说明：如果对存储在字符类型字段中的数据进行数值计算，或者与数值进行比较操作（如置于过滤条件中），会带来不必要的数据类型转换的开销，同时该字段上的索引可能失效，影响查询性能。

##### RULE 27 【Should】字符类型字段不应存储时间或日期类数据。说明：如果对存储在字符类型字段中的数据与日期类数据进行计算或比较操作（如置于过滤条件中），会带来不必要的数据类型转换的开销，同时该字段上的索引可能失效，影响查询性能。

##### RULE 28 【Must】同一含义字段在多个表中保证使用相同的数据类型及长度。

- 说明：避免不必要的类型转换而导致的性能问题。
##### RULE 29 【Should】VARCHAR类型必须指定长度，长度不超过6000。

### **索引设计规范**

##### RULE 30 【Must】必须使用Btree/CBtree索引方式，禁止使用psort等其他索引方式。

- 说明：psort索引的性能和空间开销都劣于btree索引，会产生性能问题。
##### RULE 31 【Shuold】单表上除主键外，其他索引个数不应超过3个

- 说明：多个索引会带来额外的性能开销，因此应该控制单表上的索引个数。
### **视图设计规范**

##### RULE 32 【Shuold】物化视图的刷新频率不应低于3min。

##### RULE 33 【Must】复杂视图嵌套层数不允许超过四层。

- 说明：视图嵌套过深，会导致执行计划不稳定、依赖对象视图重建过多等问题，都会导致查询性能下降、锁冲突发生概率增大等现象。按照数据库的最佳体验来看，视图嵌套应该尽量规避，原则上每个视图都应该直接基于物理表查询。出于对当前业务现状和最佳体验的综合考量，限制视图嵌套不能超过四层。存在视图各层总共关联表数据超过 20 个、大量 UNION ALL 导致跑不出数、视图嵌套超过四层的视图，均需要进行改造。
- 复杂视图定义：视图中存在三个及以上表或视图关联的场景。
##### RULE 34 【Must】视图定义中禁止排序操作（ORDER BY）。

- 说明：分布式架构下，视图排序并不能保证在使用视图时结果一定有序，因此视图内排序属于无效操作。
## **DWS业务开发规范**

### **连接管理**

##### RULE 35 【Must】应用与 ETL 必须经过 ELB（弹性负载均衡）访问 DWS。

##### RULE 36 【Must】客户端、Server 及集群的时区配置需保持一致。

##### RULE 37 【Must】作业完成后应及时关闭连接，并配置合理的 session 超时时间。

### **索引使用**

##### RULE 38 【Must】索引列做过滤条件时禁止对索引列进行表达式计算或类型转换，表达式操作和类型转换应该放在右值。

### **分区****剪枝**

##### RULE 39 【Must】过滤条件分区字段不允许设置任何表达式（非 select 输出列），过滤条件组合常量放置在右值，禁止放置在左值。

### **锁冲突**

##### RULE 40 【Must】互相冲突的操作应该错峰执行，避免锁等待引起业务堆积或出现分布式死锁报错。

**Lock Mode Conflicts**

| Lock Mode Name | Level | Lock Purpose | Conflicting Locks |
| --- | --- | --- | --- |
| Access Share Lock | 1 | SELECT statements, allows other transactions to read data. | 8 |
| Row Share Lock | 2 | SELECT … FOR UPDATE or FOR SHARE, allows other transactions to read but prevents writing. | 7, 8 |
| Row Exclusive Lock | 3 | INSERT, UPDATE, DELETE statements. | 5, 6, 7, 8 |
| Share Update Exclusive Lock | 4 | VACUUM (non-FULL), ANALYZE, CREATE INDEX CONCURRENTLY, COMMENT ON | 4, 5, 6, 7, 8 |
| Share Lock | 5 | CREATE INDEX (non-CONCURRENTLY); allows reading but prevents writing. | 3, 4, 6, 7, 8 |
| Share Row Exclusive Lock | 6 | ROW SELECT … FOR UPDATE, similar to RowExclusiveLock but allows RowShareLock. | 3, 4, 5, 6, 7, 8 |
| Exclusive Lock | 7 | Prevents RowShareLock or SELECT … FOR UPDATE operations. | 2, 3, 4, 5, 6, 7, 8 |
| Access Exclusive Lock | 8 | ALTER TABLE, DROP TABLE, VACUUM FULL; blocks other transactions from reading and writing. | 1, 2, 3, 4, 5, 6, 7, 8 |

### **其他开发规范**

##### RULE 41 【Should】insert、update、delete、merge 操作更新超过 100w 行数据或数据变动大于 25%，建议对目标表做统计信息收集。

##### RULE 42 【Must】涉及用户表的 SQL 语句，其执行计划必须能够下推，以实现分布式执行并提升性能。以下是不下推的常见写法，必须避免：

  - 子查询中不能出现 uuid_generate_v1()、sys_guid()、nextval 等不稳定函数。
  - WITH RECURSIVE 递归（Oracle CONNECT BY 改写）不支持下推。
  - WITH RECURSIVE 涉及的表不能来自多个逻辑集群。
  - WITH RECURSIVE 不能存在 UNION 去重。
  - WITH RECURSIVE 中的子查询不能涉及系统表。
  - WITH RECURSIVE 中的子查询不能存在 LIMIT 限制结果集。
  - WITH RECURSIVE 中不能存在多层嵌套。
  - WITH RECURSIVE 中 UNION ALL 子查询不能只存在 VALUE 子句。
  - 自定义函数不能包含结果不稳定的函数，常见列表包括：
- uuid_generate_v1、uuid、sys_guid、nextval、gen_random_uuid、random、clock_timestamp，可通过以下 SQL 查询所有结果不稳定的函数：
- SELECT proname FROM pg_proc WHERE provolatile = 'v';
  - 自定义函数里面不能包含查询表的 SQL 语句，只允许出现如字符处理、运算符等操作。
  - 不支持下推（重分布）的数据类型：float、double、real、自定义 type。
  - 关联列如果不支持重分布，则不支持下推。
  - 如果 COUNT(DISTINCT expr) 中的字段不支持重分布，则不支持下推。
  - COUNT(DISTINCT) + GROUP BY 如果 GROUP BY 的字段不支持重分布，则不支持下推。
  - 不支持下推 RETURNING 语句。
  - 不支持下推 DISTINCT ON 用法。
  - 不支持下推聚集函数中使用 ORDER BY 语句。
  - 不支持下推数组表达式。
##### RULE 43 【Must】禁止使用 NOT IN（子查询）语法，全部改写为 NOT EXISTS。

- 说明：DWS 中 NOT IN 子查询的执行计划走的是 Nest Loop（嵌套循环），在数据量大的情况下非常容易出现性能问题。改为 NOT EXISTS 后，执行计划可以使用 Hash Join（哈希关联），性能能够得到极大提升。
##### RULE 44 【Must】禁止使用标量子查询，必须改为外关联。

- 说明：标量子查询出现在 SELECT 列表以及 WHERE 中的等号两侧，会使执行计划走 SubPlan 算子。SubPlan 算子针对父查询中的每一行都要执行一次子查询，导致子查询执行很多次，效率非常低，从而造成性能问题。一般使用 LEFT JOIN 来改写。
##### RULE 45 【Must】子查询里面禁止使用 ORDER BY。

##### RULE 46 【Must】禁止使用无条件DELETE。全表清空请使用truncate操作。

##### RULE 47 【Should】优先使用 JOIN 替代 EXISTS 或者 IN。

- 说明：JOIN 相比 EXISTS 和 IN 具有更好的代码可读性，SQL 优化器相对更容易找到更准确的执行计划。同时在优化过程中也有更多的手段，比如 Hint 的使用。
##### RULE 48 【Must】JOIN 字段的数据类型要保持一致。

- 说明：避免优化器估算错误产生错误的执行计划，也避免数据类型自动转换导致数据发生重分布。Join 要保证 join 字段属性一致。
##### RULE 49 【Should】WHERE子句中尽量避免对字段做函数运算或套函数，保持 column op value 形式。

- 说明：对关联字段或者过滤字段进行运算或使用函数，会导致优化器无法获取到准确的字段统计信息，估算出现偏差，最终导致生成错误的执行计划，引起性能问题。
##### RULE 50 【Must】严格禁止使用 SELECT *，必须显式列出所有需要查询的字段。

- 说明：可以避免表结构变化带来的兼容性问题。
##### RULE 51 【Should】访问对象（表、函数等）时，所有对象引用必须带上 schema 名称：schema.table。

- 说明：如果不追加 schema 名称前缀，会根据当前 search_path 中表空间列表依次搜索所有表空间，直到找到匹配的表作为目标表，有可能因 schema 切换导致访问到非预期的表。
##### RULE 52 【Should】B端业务单条SQL 语句中关联的表数量不超过8个，C端业务SQL中表关联个数不超过3个。

- 说明：DWS 的优化器是基于代价的优化器（Cost-Based Optimizer），表数据量越多，估算的偏差就越大，产生性能差的计划的风险越大。当查询语句中包含视图时，需要把视图全部展开后统计查询涉及的所有基础表。
##### RULE 53 【Should】在同一个查询语句中，针对不同表应使用不同的别名。

##### RULE 54 【Must】应用程序中禁止显式开启并行参数。

- 说明：DWS 是 MPP（Massively Parallel Processing）架构，本身已经将数据分散多片到各 DN（Data Node）上并行执行，无需通过开启并行的方式加速。不允许在程序中使用 query_dop 设置并行度，它会使得 SQL 在短时间内消耗过多的资源而引起系统的不稳定。
##### RULE 55 【Recommended】在查询中，对常量建议显式指定数据类型。

##### RULE 56 【Recommended】每个 SQL 开头标记注释，唯一识别 SQL 的归属。

- 说明：方便问题定位及应用性能分析，命名建议 /* 模块名_工具名_作业名_步骤 */，例如：
- /* mca_python_xxxxxx_step1 */ INSERT INTO xxx SELECT ... FROM xxxx;
- 简写形式为：/* sys_mod_job_step */。
## **DWS运维规范**

### **变更与运维**

##### RULE 57 【Must】所有 DDL 操作（建表、改表、加索引、分区调整等）必须走变更审批流程。

##### RULE 58 【Must】禁止在生产环境执行 DROP DATABASE、无备份的DROP TABLE 及危险的ALTER COLUMN。

##### RULE 59 【Must】新建或变更的表/索引，必须经测试环境验证后才能在生产执行。

##### RULE 60 【Should】大型表提前预估影响时间，选择业务低谷执行，必要时分批变更。

##### RULE 61 【Should】大表定期检查碎片、脏页率与统计信息，纳入巡检脚本。

### **表大小****检查**

##### RULE 62 【Should】10GB 以上的大表，节点间数据量偏斜应控制在 10% 以内
