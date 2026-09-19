# 大表索引变更自动走 OSC —— 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 发布流水线执行 DDL 时，索引变更落在大表上自动改走 OSC；同时给 OSC 一个后台急停开关。

**Architecture:** 三层。①`internal/oscroute` 是纯函数判定层（这条语句是不是索引 DDL、该不该走 OSC），不碰数据库；②`service/pipeline.go` 的执行阶段按游标逐条推进，命中的那条发起 OSC 任务后进 `waiting`；③`osc.Runner` 在任务终态回调，驱动发布单继续。`osc` 包不认识 pipeline，只暴露一个 `OnFinish` 注入点。

**Tech Stack:** Go 1.2x · Gin · GORM · PostgreSQL（网关自身存储）· MySQL（被管理的目标库）· React 19 + TS + Vite（前端）· Playwright（前端单测与 e2e）

**Spec:** `docs/superpowers/specs/2026-09-19-osc-auto-route-design.md`

## Global Constraints

这些值从 spec 逐字抄来，所有任务隐含包含本节。

- **范围只有加索引 / 删索引。** 混合子句（`ADD COLUMN c INT, ADD INDEX i (c)`）一律不认。
- **该走却走不了时：直发，并把原因写进阶段日志**——不失败、不卡住。
- **判定顺序**（先到先决）：`skip` 覆盖 → `force` 覆盖 → autoRoute 关 → 非 MySQL → 不是索引 DDL → 行数 < 阈值 → 走。
- **设置项与默认值**：`osc.enabled` = `true`（急停）、`osc.autoRoute.enabled` = `true`、`osc.autoRoute.minRows` = `2000000`。
- **急停方向不对称**：配置文件里的 `osc.enabled` 关着时，后台设置怎么拨都打不开。
- **行数用 `osc.Gather` 的 `EstimatedRows`**（`information_schema`），不做 `COUNT(*)`。
- **迁移必须幂等**（ADR 0016）：`ADD COLUMN IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS`。
- **保留字不能做列名**（ADR 0016 §二）：新列名 `exec_cursor` / `osc_job_id` / `osc_mode` 均已避开。
- **后端测试连不上库是失败，不是 skip**；`internal/osc` 的测试对着真 MySQL 跑。
- **每条承重断言做变异验证**：改坏实现，确认那条用例真的会红，把结果写进提交信息。
- 提交信息 subject 用英文（`feat(osc): ...`），正文中文，结尾带
  `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`。

## 文件结构

| 文件 | 职责 |
|---|---|
| `backend/internal/oscroute/stmt.go` | 认出索引 DDL，把 `CREATE/DROP INDEX` 翻译成 alter 子句 |
| `backend/internal/oscroute/stmt_test.go` | 上面那层的全部用例 |
| `backend/internal/oscroute/decide.go` | 判定顺序：给定语句、引擎、行数、策略、覆盖 → 走不走 |
| `backend/internal/oscroute/decide_test.go` | 判定的全部用例 |
| `backend/migrations/0004_release_osc.sql` | 三列一个索引 |
| `backend/internal/model/model.go` | `ReleaseStage` 加两字段、`Release` 加一字段 |
| `backend/internal/osc/runner.go` | `OnFinish` 注入点 |
| `backend/internal/service/pipeline_osc.go` | 执行阶段的 OSC 分支、回调驱动、中止连带（新文件，不塞进已经很大的 `pipeline.go`） |
| `backend/internal/service/pipeline.go` | `stageExecute` 改为按游标推进 |
| `backend/internal/bootstrap/router.go` | 急停开关接线、`Services` 拿到 Runner |
| `frontend/src/pages/settings/index.tsx` | 三个设置项 |
| `frontend/src/pages/changes/index.tsx` | 两种 `waiting` 的区分、链到 OSC 任务 |

---

### Task 1: 急停开关（后端）

**Files:**
- Modify: `backend/internal/bootstrap/router.go:56`
- Test: `backend/internal/bootstrap/osc_api_test.go`

**Interfaces:**
- Consumes: `repo.SettingBool(key string, def bool) bool`（已存在）
- Produces: 无新符号，行为变化：`POST /api/v1/osc/jobs` 在 `osc.enabled` 设置为 `false` 时回 `resp.CodeOscDisabled`

- [ ] **Step 1: 写失败的测试**

追加到 `backend/internal/bootstrap/osc_api_test.go`：

```go
// 急停开关:改配置文件加重启关不掉一个正在出事的特性。
//
// 方向是**不对称**的,这是有意的:打开它的前提是一次对着有从库的实例的演练
// (ADR 0011),那是人做的事,界面上点一下不构成那个前提;而关要快 —— 一次迁移正在
// 把从库拖垮时,人要挡住后续发起,而不是先去重启网关。
func TestOscAPI_TheKillSwitchClosesItWithoutTouchingTheConfigFile(t *testing.T) {
	app := newTestApp(t)
	app.cfg.OSC.Enabled = true // 配置里开着
	if err := app.repo.SetSetting("osc.enabled", "false"); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/osc/jobs", token, map[string]any{
		"connectionId": 1, "schema": "app", "table": "t_order",
		"alter": "ADD INDEX idx_memo (memo)",
	})

	if r.Code != resp.CodeOscDisabled {
		t.Fatalf("后台急停之后应当返回 %d,实际 code=%d msg=%q", resp.CodeOscDisabled, r.Code, r.Msg)
	}
}

// 反方向不成立:配置里关着时,后台这个开关怎么拨都打不开。
//
// 少了这条,"急停开关"会悄悄变成"启用开关" —— 任何平台管理员在界面上点一下就能
// 打开一个会在生产库上改表的功能,而 ADR 0011 要求的那次演练没有发生。
func TestOscAPI_TheKillSwitchCannotTurnItOn(t *testing.T) {
	app := newTestApp(t)
	app.cfg.OSC.Enabled = false // 配置里关着 —— 这是前提,不是偏好
	if err := app.repo.SetSetting("osc.enabled", "true"); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/osc/jobs", token, map[string]any{
		"connectionId": 1, "schema": "app", "table": "t_order",
		"alter": "ADD INDEX idx_memo (memo)",
	})

	if r.Code != resp.CodeOscDisabled {
		t.Fatalf("配置关着时后台开关不该能打开它,实际 code=%d msg=%q", r.Code, r.Msg)
	}
}

// 没写过这个设置的部署(绝大多数)行为不变:配置说了算。
func TestOscAPI_UnsetKillSwitchMeansTheConfigDecides(t *testing.T) {
	app := newTestApp(t)
	app.cfg.OSC.Enabled = true // 没有写过 osc.enabled 这个设置
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/osc/jobs", token, map[string]any{
		"connectionId": 1, "schema": "app", "table": "t_order",
		"alter": "ADD INDEX idx_memo (memo)",
	})

	if r.Code == resp.CodeOscDisabled {
		t.Fatal("没写过急停设置时不该被拦 —— 默认值把一个开着的部署关掉了")
	}
}
```

- [ ] **Step 2: 跑测试确认它红**

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestOscAPI_TheKillSwitch|TestOscAPI_UnsetKillSwitch' -v`
Expected: 前两条 FAIL（`osc.enabled` 设置被无视，第一条会拿到非 42700 的码），第三条 PASS。

- [ ] **Step 3: 接上设置**

`backend/internal/bootstrap/router.go`，把 `AttachOSC` 那一行改成：

```go
	// 开关是**两道闸相与**,方向不对称:
	//
	//   配置文件里的 osc.enabled —— **前提**。打开它的条件是一次对着有从库的实例的
	//     演练(ADR 0011),那是人做的事,界面上点一下不构成那个前提。
	//   tbl_setting 里的 osc.enabled —— **急停**。一次迁移正在把从库拖垮时,人要
	//     立刻挡住后续发起,而不是先去重启网关。
	//
	// 所以后台那个开关只关得掉、打不开。默认 true = 不额外拦,没写过它的部署行为不变。
	h.AttachOSC(runner, func() bool {
		return cfg.OSC.Enabled && repo.SettingBool("osc.enabled", true)
	})
```

- [ ] **Step 4: 跑测试确认转绿**

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestOscAPI' -v`
Expected: 全部 PASS（含原有的四条）。

- [ ] **Step 5: 变异验证**

把接线临时改成 `return repo.SettingBool("osc.enabled", true)`（丢掉配置那一道），跑
`TestOscAPI_TheKillSwitchCannotTurnItOn`，确认它报错；改回来。

- [ ] **Step 6: 提交**

```bash
git add backend/internal/bootstrap/router.go backend/internal/bootstrap/osc_api_test.go
git commit -m "$(cat <<'EOF'
feat(osc): a kill switch that can close it but never open it

改配置文件加重启关不掉一个正在出事的特性。后台设置 osc.enabled 与配置里的
那个相与,方向刻意不对称:配置是前提(打开它要先演练过),后台是急停(要快)。

默认 true —— 没写过这个设置的部署行为一点不变。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: 急停开关（前端设置页）

**Files:**
- Modify: `frontend/src/pages/settings/index.tsx`
- Modify: `frontend/src/locales/zh.ts`, `frontend/src/locales/en.ts`

**Interfaces:**
- Consumes: 设置 key `osc.enabled`（Task 1 已消费它）
- Produces: 设置页出现「在线表结构变更」分区

- [ ] **Step 1: 加表单字段**

在 `src/pages/settings/index.tsx` 读设置的那段（`metaEnabled` 附近）加一行：

```tsx
    // ---- 在线表结构变更(OSC) ----
    // 默认 true:这是**急停**开关,不是启用开关。没写过它时不该额外拦。
    oscEnabled: parseSetting(g['osc.enabled'], true),
```

保存那段（`'meta.sync.enabled': f.metaEnabled,` 附近）加一行：

```tsx
        'osc.enabled': f.oscEnabled,
```

- [ ] **Step 2: 加界面分区**

在元数据同步那个 `<section>` 之后插入：

```tsx
        {/* ---------------- 在线表结构变更 ---------------- */}
        <section id="set-osc" className="set-sec"><Card>
          <CardHead icon={<DatabaseZap size={17} />} title={t('setOsc')} sub={t('setOscSub')} />
          <CardRow title={t('setOscEnabled')} hint={t('setOscEnabledD')}>
            <Switch checked={f.oscEnabled} onChange={(v) => set({ oscEnabled: v })} />
          </CardRow>
        </Card></section>
```

- [ ] **Step 3: 加文案**

`src/locales/zh.ts`：

```ts
  setOsc: '在线表结构变更',
  setOscSub: '大表加索引时用影子表 + 分块拷贝代替原生 DDL(ADR 0011)',
  setOscEnabled: '允许发起在线变更',
  setOscEnabledD: '这是急停开关:关掉它立刻挡住后续发起。**它打不开这个功能** —— 启用的前提是后端配置里的 osc.enabled 为 true,而那要求先在一套有从库的实例上演练过一次。',
```

`src/locales/en.ts`：

```ts
  setOsc: 'Online schema change',
  setOscSub: 'Shadow table + chunked copy instead of native DDL on big tables (ADR 0011)',
  setOscEnabled: 'Allow starting online changes',
  setOscEnabledD: 'A kill switch: turning it off blocks new migrations immediately. It cannot turn the feature ON — that requires osc.enabled in the backend config, which in turn requires one rehearsal against an instance that has a replica.',
```

- [ ] **Step 4: 类型检查与构建**

Run: `cd frontend && npm run type-check && npm run build`
Expected: 两者都通过。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/pages/settings/index.tsx frontend/src/locales/zh.ts frontend/src/locales/en.ts
git commit -m "$(cat <<'EOF'
feat(osc): the kill switch, on the settings page

开关旁边那句说明不是客套:它只关得掉、打不开。少了这句,人会拨一下然后等着
一个永远不会启用的功能。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `oscroute` —— 认出索引 DDL 并翻译

**Files:**
- Create: `backend/internal/oscroute/stmt.go`
- Test: `backend/internal/oscroute/stmt_test.go`

**Interfaces:**
- Produces:
  ```go
  type IndexDDL struct {
      Table string // 表名(不带 schema)
      Alter string // 交给 osc.StartRequest 的 alter 子句
  }
  func ParseIndexDDL(sql string) (IndexDDL, bool)
  ```

- [ ] **Step 1: 写失败的测试**

`backend/internal/oscroute/stmt_test.go`：

```go
package oscroute

import "testing"

// 这一层回答一个问题:这条语句是不是"一次纯粹的索引变更",如果是,交给 OSC 的
// alter 子句长什么样。
//
// 认错的代价有限 —— 顶多是走了或没走 OSC,而 OSC 自己的 Preflight 会对着真实例
// 再拒一次。所以这里用受限的模式匹配,不引入 SQL parser。但**认得太宽**是有代价的:
// 把一条改列语句交给 OSC,那次变更会在 Preflight 那里失败,而人看到的是一张失败的
// 发布单,不是"这条语句不该走这条路"。

func TestParseIndexDDL_AlterAddIndex(t *testing.T) {
	got, ok := ParseIndexDDL("ALTER TABLE t_order ADD INDEX idx_memo (memo)")
	if !ok {
		t.Fatal("没认出最常见的那一种写法")
	}
	if got.Table != "t_order" {
		t.Errorf("表名认成了 %q,期望 t_order", got.Table)
	}
	if got.Alter != "ADD INDEX idx_memo (memo)" {
		t.Errorf("alter 子句是 %q,期望 ADD INDEX idx_memo (memo)", got.Alter)
	}
}

func TestParseIndexDDL_AlterDropIndex(t *testing.T) {
	got, ok := ParseIndexDDL("ALTER TABLE `t_order` DROP KEY idx_memo")
	if !ok {
		t.Fatal("没认出 DROP KEY")
	}
	if got.Table != "t_order" {
		t.Errorf("反引号没有被去掉:表名是 %q", got.Table)
	}
	if got.Alter != "DROP KEY idx_memo" {
		t.Errorf("alter 子句是 %q", got.Alter)
	}
}

func TestParseIndexDDL_TranslatesCreateIndex(t *testing.T) {
	// CREATE INDEX 是很常见的写法。不翻译的话它永远走不到 OSC —— 而"静默地
	// 走不到"比"明确不支持"更糟:没有人会发现。
	got, ok := ParseIndexDDL("CREATE INDEX idx_memo ON t_order (memo)")
	if !ok {
		t.Fatal("没认出 CREATE INDEX")
	}
	if got.Table != "t_order" {
		t.Errorf("表名认成了 %q", got.Table)
	}
	if got.Alter != "ADD INDEX idx_memo (memo)" {
		t.Errorf("翻译成了 %q,期望 ADD INDEX idx_memo (memo)", got.Alter)
	}
}

func TestParseIndexDDL_TranslatesCreateUniqueIndex(t *testing.T) {
	got, ok := ParseIndexDDL("CREATE UNIQUE INDEX uk_no ON t_order (no)")
	if !ok {
		t.Fatal("没认出 CREATE UNIQUE INDEX")
	}
	if got.Alter != "ADD UNIQUE INDEX uk_no (no)" {
		t.Errorf("翻译成了 %q,期望 ADD UNIQUE INDEX uk_no (no)", got.Alter)
	}
}

func TestParseIndexDDL_TranslatesDropIndexOn(t *testing.T) {
	got, ok := ParseIndexDDL("DROP INDEX idx_memo ON t_order")
	if !ok {
		t.Fatal("没认出 DROP INDEX ... ON")
	}
	if got.Table != "t_order" || got.Alter != "DROP INDEX idx_memo" {
		t.Errorf("认成了 %+v", got)
	}
}

func TestParseIndexDDL_StripsSchemaPrefix(t *testing.T) {
	// OSC 的 StartRequest 分开收 schema 和 table,schema 来自发布单的目标库。
	// 表名里带着库名前缀会拼出 `app`.`app.t_order` 这样的东西。
	got, ok := ParseIndexDDL("ALTER TABLE app.t_order ADD INDEX idx_memo (memo)")
	if !ok {
		t.Fatal("没认出带库名前缀的写法")
	}
	if got.Table != "t_order" {
		t.Errorf("表名是 %q,库名前缀没有被剥掉", got.Table)
	}
}

func TestParseIndexDDL_RejectsMixedClauses(t *testing.T) {
	// 混合子句超出 OSC 的能力范围(ADR 0011 刻意收窄到只做索引)。认下来的话,
	// 这次变更会在 Preflight 那里失败,而人看到的是一张失败的发布单。
	if _, ok := ParseIndexDDL("ALTER TABLE t_order ADD COLUMN c INT, ADD INDEX i (c)"); ok {
		t.Error("认下了一条混合子句的 ALTER —— 它不是纯粹的索引变更")
	}
}

func TestParseIndexDDL_RejectsNonIndexDDL(t *testing.T) {
	for _, q := range []string{
		"ALTER TABLE t_order MODIFY COLUMN memo VARCHAR(128)",
		"ALTER TABLE t_order ADD COLUMN memo VARCHAR(64)",
		"ALTER TABLE t_order ENGINE=InnoDB",
		"UPDATE t_order SET memo = 'x'",
		"CREATE TABLE t (id INT)",
		"DROP TABLE t_order",
		"",
	} {
		if _, ok := ParseIndexDDL(q); ok {
			t.Errorf("认下了一条不是索引变更的语句: %q", q)
		}
	}
}

func TestParseIndexDDL_RejectsAddPrimaryKey(t *testing.T) {
	// 加主键不是加二级索引:它会重建整张表的聚簇索引,而 OSC 的影子表方案对它
	// 有另一套语义要考虑(空值、去重)。不在这次的范围里。
	if _, ok := ParseIndexDDL("ALTER TABLE t_order ADD PRIMARY KEY (id)"); ok {
		t.Error("认下了 ADD PRIMARY KEY")
	}
}
```

- [ ] **Step 2: 跑测试确认它红**

Run: `cd backend && go test ./internal/oscroute/ -v`
Expected: 编译失败（`undefined: ParseIndexDDL`）。先加一个返回零值的骨架，再跑一次，确认每条都以**断言失败**的方式红——编译错误不算"看着它失败"。

- [ ] **Step 3: 实现**

`backend/internal/oscroute/stmt.go`：

```go
// Package oscroute 回答一个问题:这条 DDL 该不该改走 OSC(ADR 0011)。
//
// 它是纯函数层,不碰数据库 —— 与 osc 包把"采集事实"和"判定"分开是同一个理由:
// 这里的每条规则都能让一次变更走上另一条路,它必须能被单独测透,而不必搭一套流水线。
package oscroute

import (
	"regexp"
	"strings"
)

// IndexDDL 是一条被认下来的纯索引变更。
//
// Alter 是**交给 osc.StartRequest 的子句**,不是原句:CREATE INDEX / DROP INDEX
// 会被翻译成等价的 ALTER 形式,因为 OSC 收的是子句。
type IndexDDL struct {
	Table string
	Alter string
}

var (
	// ALTER TABLE t ADD [UNIQUE|FULLTEXT|SPATIAL] INDEX|KEY ... / DROP INDEX|KEY ...
	reAlterIndex = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+(` + identPat + `)\s+` +
		`((?:ADD\s+(?:UNIQUE\s+|FULLTEXT\s+|SPATIAL\s+)?(?:INDEX|KEY)\s+.+)|(?:DROP\s+(?:INDEX|KEY)\s+\S+))\s*$`)
	// CREATE [UNIQUE|FULLTEXT|SPATIAL] INDEX name ON t (cols)
	reCreateIndex = regexp.MustCompile(`(?is)^\s*CREATE\s+(UNIQUE\s+|FULLTEXT\s+|SPATIAL\s+)?INDEX\s+(\S+)\s+ON\s+(` +
		identPat + `)\s*(\(.+\))\s*$`)
	// DROP INDEX name ON t
	reDropIndex = regexp.MustCompile(`(?is)^\s*DROP\s+INDEX\s+(\S+)\s+ON\s+(` + identPat + `)\s*$`)
)

// identPat 匹配 `db`.`t` / db.t / t 三种形态。
const identPat = "[` \"\\w.$-]+?"

// ParseIndexDDL 认出一条纯粹的索引变更,并给出交给 OSC 的 alter 子句。
//
// **宁可少认,不可错认。** 认不出来的后果是这条语句照常直发(原生 DDL 加索引本来
// 就是在线的);而错认的后果是把一条 OSC 做不了的变更交给它,那次发布会在 Preflight
// 那里失败 —— 人看到的是一张失败的发布单,而不是"这条语句不该走这条路"。
func ParseIndexDDL(sql string) (IndexDDL, bool) {
	s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sql), ";"))
	if s == "" {
		return IndexDDL{}, false
	}
	// 逗号分隔的多子句 ALTER 一律不认。ADD PRIMARY KEY 同理:它重建聚簇索引,
	// 不是加一个二级索引。两者都超出 ADR 0011 给这套东西划的范围。
	if m := reAlterIndex.FindStringSubmatch(s); m != nil {
		clause := strings.TrimSpace(m[2])
		if strings.Contains(clause, ",") && !insideParens(clause) {
			return IndexDDL{}, false
		}
		return IndexDDL{Table: cleanIdent(m[1]), Alter: normalizeSpace(clause)}, true
	}
	if m := reCreateIndex.FindStringSubmatch(s); m != nil {
		kind := strings.ToUpper(strings.TrimSpace(m[1]))
		if kind != "" {
			kind += " "
		}
		return IndexDDL{
			Table: cleanIdent(m[3]),
			Alter: normalizeSpace("ADD " + kind + "INDEX " + cleanIdent(m[2]) + " " + m[4]),
		}, true
	}
	if m := reDropIndex.FindStringSubmatch(s); m != nil {
		return IndexDDL{Table: cleanIdent(m[2]), Alter: "DROP INDEX " + cleanIdent(m[1])}, true
	}
	return IndexDDL{}, false
}

// insideParens 报告这个子句里的逗号是不是全都在括号内 —— `ADD INDEX i (a, b)` 是
// 一个子句,`ADD INDEX i (a), ADD INDEX j (b)` 是两个。
func insideParens(clause string) bool {
	depth := 0
	for _, r := range clause {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				return false
			}
		}
	}
	return true
}

// cleanIdent 去掉反引号/双引号与库名前缀。
//
// 库名前缀必须剥掉:OSC 的 StartRequest 分开收 schema 与 table,schema 来自发布单
// 的目标库。带着前缀会拼出 `app`.`app.t_order` 这样的名字。
func cleanIdent(s string) string {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer("`", "", `"`, "").Replace(s)
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
```

- [ ] **Step 4: 跑测试确认转绿**

Run: `cd backend && go test ./internal/oscroute/ -v`
Expected: 全部 PASS。

- [ ] **Step 5: 变异验证**

把 `insideParens` 的调用去掉（即不再拒绝多子句 ALTER），跑
`TestParseIndexDDL_RejectsMixedClauses`，确认它红；改回来。

- [ ] **Step 6: 提交**

```bash
git add backend/internal/oscroute/
git commit -m "$(cat <<'EOF'
feat(oscroute): recognise a pure index change, and translate CREATE INDEX

CREATE INDEX 要翻译成 ALTER 的子句,因为 OSC 收的是子句。不翻译的话这种写法
永远走不到 OSC —— 而"静默地走不到"比"明确不支持"更糟,没有人会发现。

宁可少认不可错认:认不出来就照常直发(原生加索引本来就是在线的),而错认会把一条
OSC 做不了的变更交给它,那次发布在 Preflight 那里失败,人看到的是一张失败的单。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `oscroute` —— 判定顺序

**Files:**
- Create: `backend/internal/oscroute/decide.go`
- Test: `backend/internal/oscroute/decide_test.go`

**Interfaces:**
- Consumes: `ParseIndexDDL(sql string) (IndexDDL, bool)`（Task 3）
- Produces:
  ```go
  type Policy struct {
      AutoRoute bool
      MinRows   int64
  }
  type Override string // "" | "force" | "skip"
  const (OverrideNone Override = ""; OverrideForce Override = "force"; OverrideSkip Override = "skip")
  type Decision struct {
      UseOSC bool
      Reason string
      Table  string
      Alter  string
  }
  // rowsOf 只在确实需要行数时才被调用 —— 它背后是一次 information_schema 查询。
  func Decide(sql, engine string, p Policy, ov Override, rowsOf func(table string) int64) Decision
  ```

**行数是懒查的。** 一张单里可能有十条 UPDATE，为每一条都去查一次
`information_schema` 是白费的往返；而且只有认出这是索引 DDL 之后才知道该查哪张表。

- [ ] **Step 1: 写失败的测试**

`backend/internal/oscroute/decide_test.go`：

```go
package oscroute

import "strings"
import "testing"

// 判定顺序是这一层的全部内容,而顺序本身就是决定:覆盖排在策略前面,因为它是人对
// 这一次的明确指令;引擎排在语句识别前面,因为对着一个 PostgreSQL 实例讨论"这是不是
// 索引 DDL"没有意义。
//
// 每条判定都要给出 Reason —— 它直接进阶段日志。一次"本该走 OSC 却没走"必须看得见,
// 否则这个功能失效时没有任何迹象。

const bigTable = 8_000_000

func policy() Policy { return Policy{AutoRoute: true, MinRows: 2_000_000} }

// rowsFn 造一个固定的行数来源。真实现背后是一次 information_schema 查询。
func rowsFn(n int64) func(string) int64 { return func(string) int64 { return n } }

func TestDecide_RoutesABigTableIndexChange(t *testing.T) {
	d := Decide("ALTER TABLE t_order ADD INDEX idx_memo (memo)", "mysql", policy(), OverrideNone, rowsFn(bigTable))

	if !d.UseOSC {
		t.Fatalf("八百万行的表上加索引没有走 OSC:%s", d.Reason)
	}
	if d.Table != "t_order" || d.Alter != "ADD INDEX idx_memo (memo)" {
		t.Errorf("交给 OSC 的是 %+v", d)
	}
	if d.Reason == "" {
		t.Error("走了 OSC 却没有说为什么 —— 阶段日志里会是一片空白")
	}
}

func TestDecide_SkipsASmallTable(t *testing.T) {
	d := Decide("ALTER TABLE t_order ADD INDEX idx_memo (memo)", "mysql", policy(), OverrideNone, rowsFn(1_000))

	if d.UseOSC {
		t.Error("一千行的表也走了 OSC —— 影子表和 binlog 订阅的开销远大于收益")
	}
	// 理由里要有那个数,否则人只知道"没走",不知道"差多少"。
	if !strings.Contains(d.Reason, "1000") && !strings.Contains(d.Reason, "1,000") {
		t.Errorf("理由 %q 里没有实际行数", d.Reason)
	}
}

func TestDecide_ThresholdItselfIsNotOver(t *testing.T) {
	// 正好等于阈值不算超。与 osc.shouldPause 对限流阈值的立场一致:一个恰好卡在
	// 线上的值反复触发,会让行为看起来随机。
	d := Decide("ALTER TABLE t_order ADD INDEX i (c)", "mysql", policy(), OverrideNone, rowsFn(2_000_000))

	if d.UseOSC {
		t.Error("行数正好等于阈值时走了 OSC")
	}
}

func TestDecide_SkipOverrideWinsOverEverything(t *testing.T) {
	d := Decide("ALTER TABLE t_order ADD INDEX i (c)", "mysql", policy(), OverrideSkip, rowsFn(bigTable))

	if d.UseOSC {
		t.Error("发起人明确选了直发,却仍然走了 OSC")
	}
	if !strings.Contains(d.Reason, "发起人") {
		t.Errorf("理由 %q 没有说明这是人的选择 —— 事后查起来会被当成判定出错", d.Reason)
	}
}

func TestDecide_ForceOverrideSkipsTheRowCheck(t *testing.T) {
	// 估算行数可能偏得很离谱(InnoDB 的 TABLE_ROWS)。force 是人对这件事的纠正。
	d := Decide("ALTER TABLE t_order ADD INDEX i (c)", "mysql", policy(), OverrideForce, rowsFn(10))

	if !d.UseOSC {
		t.Errorf("发起人强制走 OSC,却没有走:%s", d.Reason)
	}
}

func TestDecide_ForceStillRefusesWhatOSCCannotDo(t *testing.T) {
	// force 是"跳过行数判断",不是"把任何语句都塞给 OSC"。一条改列语句交过去,
	// 会在 Preflight 那里失败,而人看到的是一张失败的发布单。
	d := Decide("ALTER TABLE t_order MODIFY COLUMN memo VARCHAR(128)", "mysql", policy(), OverrideForce, rowsFn(bigTable))

	if d.UseOSC {
		t.Error("强制模式把一条改列语句交给了 OSC")
	}
}

func TestDecide_AutoRouteOffMeansNever(t *testing.T) {
	p := policy()
	p.AutoRoute = false

	d := Decide("ALTER TABLE t_order ADD INDEX i (c)", "mysql", p, OverrideNone, rowsFn(bigTable))

	if d.UseOSC {
		t.Error("自动路由关着却仍然走了 OSC")
	}
}

func TestDecide_NonMySQLNeverRoutes(t *testing.T) {
	// OSC 是 MySQL 专属的(binlog + 影子表)。对着 PostgreSQL 讨论这件事没有意义,
	// 而理由要说得出是引擎的原因 —— 否则人会去查自己的阈值配置。
	d := Decide("ALTER TABLE t_order ADD INDEX i (c)", "postgres", policy(), OverrideNone, rowsFn(bigTable))

	if d.UseOSC {
		t.Error("在 PostgreSQL 上走了 OSC")
	}
	if !strings.Contains(d.Reason, "MySQL") {
		t.Errorf("理由 %q 没有点出引擎", d.Reason)
	}
}

func TestDecide_DoesNotCountRowsForStatementsItWillNotRoute(t *testing.T) {
	// 行数背后是一次 information_schema 查询。一张单里十条 UPDATE,为每一条都查
	// 一次是白费的往返 —— 而且只有认出这是索引 DDL 之后才知道该查哪张表。
	called := 0
	rows := func(string) int64 { called++; return bigTable }

	Decide("UPDATE t_order SET memo = 'x'", "mysql", policy(), OverrideNone, rows)
	Decide("ALTER TABLE t_order ADD INDEX i (c)", "postgres", policy(), OverrideNone, rows)

	if called != 0 {
		t.Errorf("为不会路由的语句查了 %d 次行数", called)
	}
}

func TestDecide_NonIndexDDLNeverRoutes(t *testing.T) {
	d := Decide("UPDATE t_order SET memo = 'x'", "mysql", policy(), OverrideNone, rowsFn(bigTable))

	if d.UseOSC {
		t.Error("把一条 UPDATE 交给了 OSC")
	}
}
```

- [ ] **Step 2: 跑测试确认它红**

Run: `cd backend && go test ./internal/oscroute/ -run TestDecide -v`
Expected: 编译失败 → 加返回零值的骨架 → 每条以断言失败的方式红。

- [ ] **Step 3: 实现**

`backend/internal/oscroute/decide.go`：

```go
package oscroute

import (
	"fmt"
	"strings"
)

// Policy 是平台对"什么样的变更该走 OSC"的默认立场,来自设置项。
type Policy struct {
	AutoRoute bool  // osc.autoRoute.enabled
	MinRows   int64 // osc.autoRoute.minRows
}

// Override 是发起人对**这一单**的明确指令。
type Override string

const (
	OverrideNone  Override = ""
	OverrideForce Override = "force"
	OverrideSkip  Override = "skip"
)

// Decision 是一条语句的去向。Reason 直接进阶段日志 —— 每一条都要说得出为什么,
// 因为"本该走 OSC 却没走"必须看得见:看不见的话,这个功能哪天失效了不会有任何迹象。
type Decision struct {
	UseOSC bool
	Reason string
	Table  string
	Alter  string
}

// Decide 判断一条语句该不该改走 OSC。
//
// 顺序本身就是决定:
//
//	skip 覆盖   → 人对这一次说了不,不再问别的
//	force 覆盖  → 跳过行数判断,但**仍要通过语句识别**
//	autoRoute 关 → 平台没开这个功能
//	非 MySQL    → OSC 是 MySQL 专属的,再往下问没有意义
//	不是索引 DDL → 超出 OSC 的能力范围(ADR 0011 刻意收窄)
//	行数不够    → 小表上影子表的开销远大于收益
func Decide(sql, engine string, p Policy, ov Override, rowsOf func(table string) int64) Decision {
	if ov == OverrideSkip {
		return Decision{Reason: "直发:发起人对本单选择了不走 OSC"}
	}
	if ov != OverrideForce && !p.AutoRoute {
		return Decision{Reason: "直发:自动路由未启用(osc.autoRoute.enabled)"}
	}
	if !strings.EqualFold(engine, "mysql") {
		return Decision{Reason: fmt.Sprintf("直发:目标引擎是 %s,OSC 只做 MySQL", engine)}
	}
	ddl, ok := ParseIndexDDL(sql)
	if !ok {
		// force 也拦在这里 —— "跳过行数判断"不等于"把任何语句都塞给 OSC"。
		return Decision{Reason: "直发:不是一条纯粹的索引变更,超出 OSC 的能力范围"}
	}
	if ov == OverrideForce {
		return Decision{UseOSC: true, Table: ddl.Table, Alter: ddl.Alter,
			Reason: "走 OSC:发起人对本单强制指定"}
	}
	// 行数到这里才查:它背后是一次 information_schema 查询,而上面每一条分支都
	// 已经足以决定去向 —— 一张单里十条 UPDATE 不该换来十次往返。
	rows := rowsOf(ddl.Table)
	// 正好等于阈值不算超:一个恰好卡在线上的值反复触发,会让行为看起来随机。
	if rows <= p.MinRows {
		return Decision{Reason: fmt.Sprintf("直发:约 %d 行,未超过阈值 %d", rows, p.MinRows)}
	}
	return Decision{UseOSC: true, Table: ddl.Table, Alter: ddl.Alter,
		Reason: fmt.Sprintf("走 OSC:约 %d 行,超过阈值 %d", rows, p.MinRows)}
}
```

- [ ] **Step 4: 跑测试确认转绿**

Run: `cd backend && go test ./internal/oscroute/ -v`
Expected: 全部 PASS。

- [ ] **Step 5: 变异验证**

把 `rows <= p.MinRows` 改成 `rows < p.MinRows`，跑 `TestDecide_ThresholdItselfIsNotOver`，
确认它红；改回来。再把 `force` 分支提到 `ParseIndexDDL` 之前，跑
`TestDecide_ForceStillRefusesWhatOSCCannotDo`，确认它红；改回来。

- [ ] **Step 6: 提交**

```bash
git add backend/internal/oscroute/decide.go backend/internal/oscroute/decide_test.go
git commit -m "$(cat <<'EOF'
feat(oscroute): decide whether one statement should go through OSC

顺序本身就是决定:覆盖排在策略前面(它是人对这一次的明确指令),引擎排在语句识别
前面(对着 PostgreSQL 讨论"这是不是索引 DDL"没有意义)。

force 是"跳过行数判断",不是"把任何语句都塞给 OSC" —— 一条改列语句交过去会在
Preflight 那里失败,而人看到的是一张失败的发布单。

每条判定都给 Reason,它直接进阶段日志:"本该走 OSC 却没走"必须看得见,否则这个
功能哪天失效了不会有任何迹象。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: 迁移与模型字段

**Files:**
- Create: `backend/migrations/0004_release_osc.sql`
- Modify: `backend/internal/model/model.go:1213-1236`（`ReleaseStage`）、`:1149-1204`（`Release`）
- Test: `backend/internal/bootstrap/migrate_test.go`（表数断言所在处，确认不受影响）

**Interfaces:**
- Produces: `model.ReleaseStage.ExecCursor int`、`model.ReleaseStage.OSCJobID int64`、`model.Release.OSCMode string`

- [ ] **Step 1: 写迁移**

`backend/migrations/0004_release_osc.sql`：

```sql
-- 发布单执行阶段挂在 OSC 长任务上所需的三列。见 ADR 0011 与
-- docs/superpowers/specs/2026-09-19-osc-auto-route-design.md。
--
-- 为什么执行阶段要有游标:一条走 OSC 的语句要跑几小时,阶段在那期间停在 waiting。
-- 恢复时必须知道**前面几条已经执行过了** —— 不记的话,恢复会把已经执行过的语句
-- 再执行一遍,而那是一次重复的生产变更。

ALTER TABLE tbl_release_stage ADD COLUMN IF NOT EXISTS exec_cursor INT    NOT NULL DEFAULT 0;
ALTER TABLE tbl_release_stage ADD COLUMN IF NOT EXISTS osc_job_id  BIGINT NOT NULL DEFAULT 0;

-- 发起人对这一单的单次覆盖:'' = 按策略,force = 强制走,skip = 强制直发。
ALTER TABLE tbl_release ADD COLUMN IF NOT EXISTS osc_mode VARCHAR(8) NOT NULL DEFAULT '';

-- 任务结束时要反查"是哪个阶段在等它"。没有索引的话,每个任务结束都要全表扫一遍。
CREATE INDEX IF NOT EXISTS idx_release_stage_osc_job ON tbl_release_stage (osc_job_id);
```

- [ ] **Step 2: 加模型字段**

`ReleaseStage` 里 `ConfirmedBy` 之后：

```go
	// ExecCursor 是这个执行阶段**已经执行完的语句条数**。
	//
	// 一条走 OSC 的语句要跑几小时,阶段在那期间停在 waiting。恢复时必须知道前面
	// 几条已经做过了 —— 不记的话,恢复会把已经执行过的语句再执行一遍,而那是一次
	// 重复的生产变更。
	ExecCursor int `gorm:"not null;default:0" json:"execCursor"`
	// OSCJobID 是此刻挂着的那个 OSC 任务(0 = 没挂)。
	//
	// 它同时是界面的判据:waiting 现在有两种意思 —— "等人点确认执行"和"等一个迁移
	// 跑完"。后者给出「确认执行」按钮毫无意义,按下去只会让人以为自己推进了什么。
	OSCJobID int64 `gorm:"column:osc_job_id;index:idx_release_stage_osc_job;not null;default:0" json:"oscJobId"`
```

`Release` 里 `ChangeType` 之后：

```go
	// OSCMode 是发起人对这一单的单次覆盖:"" 按策略,force 强制走 OSC,skip 强制直发。
	// 它进审计 —— 一次例外要说得出是谁定的。
	OSCMode string `gorm:"column:osc_mode;size:8;not null;default:''" json:"oscMode"`
```

- [ ] **Step 3: 跑迁移相关测试**

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestMigrate' -v`
Expected: PASS。表的**张数没变**（只加列），所以那条数 37 张表的断言不受影响。若它红了，说明有人把列数也算进去了——那时改断言，不要改迁移。

- [ ] **Step 4: 提交**

```bash
git add backend/migrations/0004_release_osc.sql backend/internal/model/model.go
git commit -m "$(cat <<'EOF'
feat(pipeline): columns for an execute stage that hangs on a long task

exec_cursor 记的是"已经执行完几条"。一条走 OSC 的语句要跑几小时,阶段在那期间
停在 waiting —— 恢复时不知道前面做到哪,就会把已经执行过的语句再执行一遍,
而那是一次重复的生产变更。

osc_job_id 还兼作界面的判据:waiting 从此有两种意思,给"等任务"的那一种画上
「确认执行」按钮,按下去只会让人以为自己推进了什么。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: `Runner.OnFinish` 注入点

**Files:**
- Modify: `backend/internal/osc/runner.go:24-46`（结构体与构造）、`:332`（`finish`）
- Test: `backend/internal/osc/runner_test.go`

**Interfaces:**
- Produces: `Runner.OnFinish func(*Job)`（可为 nil）

- [ ] **Step 1: 写失败的测试**

追加到 `backend/internal/osc/runner_test.go`：

```go
// 任务走到终态时要能通知外面。
//
// 做成注入的回调而不是让 osc 包去调 pipeline:这个包的职责是把一次迁移做完,
// 它不该知道有人在等它。谁关心谁自己接 —— 与 CopyOptions.ReplicaLag 把"读延迟"
// 做成注入点是同一个理由。
func TestRunner_NotifiesWhenAJobReachesATerminalState(t *testing.T) {
	r, d := newRunner(t)
	ctx := context.Background()
	name := makeTable(t, d.target, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")
	mustExec(t, d.target, fmt.Sprintf("INSERT INTO `%s` (id, memo) VALUES (1,'x')", name))
	t.Cleanup(func() {
		for _, x := range []string{ShadowName(name), "_" + name + DelSuffix, HeartbeatName(name)} {
			_, _ = d.target.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+x+"`")
		}
	})

	done := make(chan *Job, 1)
	r.OnFinish = func(j *Job) { done <- j }

	job, err := r.Start(ctx, StartRequest{
		ConnectionID: 1, Schema: "osc_test", Table: name,
		Alter: "ADD INDEX idx_memo (memo)", CreatedBy: "linwei@vela.io",
	})
	if err != nil {
		t.Fatalf("发起失败: %v", err)
	}

	select {
	case got := <-done:
		if got.ID != job.ID {
			t.Errorf("回调拿到的是任务 %d,期望 %d", got.ID, job.ID)
		}
		// **终态要带在回调里。** 只通知"结束了"而不说结果,调用方还得自己再查一次,
		// 而它此刻最需要知道的正是成功还是失败。
		if got.Status != JobDone {
			t.Errorf("回调里的状态是 %s,期望 done", got.Status)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("任务已经结束,回调却没有被调用 —— 挂在它上面的发布单会永远等下去")
	}
}

// 没有注册回调时什么都不该发生(绝大多数任务是从 OSC 控制台手工发起的)。
func TestRunner_WorksWithoutAnyListener(t *testing.T) {
	r, d := newRunner(t)
	ctx := context.Background()
	name := makeTable(t, d.target, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")
	t.Cleanup(func() {
		for _, x := range []string{ShadowName(name), "_" + name + DelSuffix, HeartbeatName(name)} {
			_, _ = d.target.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+x+"`")
		}
	})

	job, err := r.Start(ctx, StartRequest{
		ConnectionID: 1, Schema: "osc_test", Table: name,
		Alter: "ADD INDEX idx_memo (memo)", CreatedBy: "linwei@vela.io",
	})
	if err != nil {
		t.Fatalf("发起失败: %v", err)
	}
	if final := waitForFinish(t, r, job.ID, 60*time.Second); final.Status != JobDone {
		t.Fatalf("最终状态 = %s(err=%q)", final.Status, final.Err)
	}
}
```

- [ ] **Step 2: 跑测试确认它红**

Run: `cd backend && go test ./internal/osc/ -run 'TestRunner_Notifies|TestRunner_WorksWithout' -v`
Expected: 编译失败（`r.OnFinish` 未定义）→ 加字段 → 第一条以「回调没有被调用」超时失败。

- [ ] **Step 3: 实现**

`Runner` 结构体里 `MaxLag` 之后加：

```go
	// OnFinish 在一个任务走到终态(done/failed/aborted)时被调用,可为 nil。
	//
	// 做成注入点而不是让这个包去调用谁:osc 的职责是把一次迁移做完,它不该知道
	// 有人在等它。发布流水线要靠它接着往下走,而那是 service 层的事。
	//
	// 回调在 finish 的调用者那条 goroutine 上同步执行,所以它必须**快**:
	// 里面做的事越多,越可能把一次迁移的收尾拖住。
	OnFinish func(*Job)
```

`finish` 改成：

```go
func (r *Runner) finish(id int64, status JobStatus, errMsg string) {
	now := time.Now()
	r.store.Model(&Job{}).Where("id = ?", id).Updates(map[string]any{
		"status": status, "err": errMsg, "updated_at": now, "finished_at": now,
	})
	// 先落库再通知:回调多半要去读这一行(比如发布单要按状态决定继续还是失败),
	// 顺序反了它读到的是上一个状态。
	if r.OnFinish == nil {
		return
	}
	if j, err := r.Get(context.Background(), id); err == nil {
		r.OnFinish(j)
	}
}
```

- [ ] **Step 4: 跑测试确认转绿**

Run: `cd backend && go test ./internal/osc/ -run 'TestRunner' -v`
Expected: 全部 PASS。

- [ ] **Step 5: 变异验证**

把 `finish` 里的通知挪到落库**之前**，把测试里的状态断言改成读库（或临时加一条断言
读 `r.Get` 的状态），确认顺序错了会被抓到；改回来。

- [ ] **Step 6: 提交**

```bash
git add backend/internal/osc/runner.go backend/internal/osc/runner_test.go
git commit -m "$(cat <<'EOF'
feat(osc): tell whoever is waiting that a job reached its end

做成注入的回调,不是让 osc 去调 pipeline:这个包的职责是把一次迁移做完,
它不该知道有人在等它。

先落库再通知 —— 回调多半要去读那一行,顺序反了它读到的是上一个状态。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: 执行阶段按游标推进，日志接续

**Files:**
- Modify: `backend/internal/service/pipeline.go:746-812`（`stageExecute`）
- Test: `backend/internal/service/pipeline_execcursor_test.go`（新）

**Interfaces:**
- Consumes: `model.ReleaseStage.ExecCursor`（Task 5）
- Produces: `stageExecute` 从 `st.ExecCursor` 开始执行，并以 `st.Log` 为日志前缀

**这一步不引入 OSC。** 先把"能从半截继续、且不丢日志"做出来并测住——它是下一个任务的地基，而它自己就能被独立验证。

- [ ] **Step 1: 写失败的测试**

`backend/internal/service/pipeline_execcursor_test.go`：

```go
package service

import (
	"strings"
	"testing"

	"velagateway/internal/model"
)

// 执行阶段要能从半截继续。
//
// 一条走 OSC 的语句要跑几小时,阶段在那期间停在 waiting。恢复时如果从头再来,
// 前面几条已经落库的变更会被**再执行一遍** —— 那是一次重复的生产变更,而它不会
// 报任何错(一条 ALTER 重跑会报 1061,但一条 UPDATE 不会)。

func TestStageExecute_ResumesFromTheCursorInsteadOfRerunningEverything(t *testing.T) {
	// 夹具:三条语句的发布单,游标停在 2 —— 前两条"已经执行过了"。
	fx := newExecFixture(t, "UPDATE t SET a=1; UPDATE t SET b=2; UPDATE t SET c=3")
	fx.stage.ExecCursor = 2
	fx.saveStage()

	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	if out.status != model.RunSuccess {
		t.Fatalf("状态 = %s,日志:%s", out.status, out.log)
	}
	// 只有第三条该被下发。
	if n := fx.exec.count(); n != 1 {
		t.Errorf("下发了 %d 条语句,期望 1 条 —— 前两条被重复执行了", n)
	}
	if got := fx.exec.last(); !strings.Contains(got, "c=3") {
		t.Errorf("下发的是 %q,期望第三条", got)
	}
}

func TestStageExecute_KeepsTheLogWrittenBeforeItPaused(t *testing.T) {
	// driveRelease 每次都用 out.log **覆盖**阶段的 log 字段。恢复时如果从空开始,
	// 前面几条的执行记录会消失 —— 而那正是一次跨了几小时的执行最需要留下的东西。
	fx := newExecFixture(t, "UPDATE t SET a=1; UPDATE t SET b=2")
	fx.stage.ExecCursor = 1
	fx.stage.Log = "· [1/2] 执行成功 · 1 行受影响 (3ms)\n"
	fx.saveStage()

	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	if !strings.Contains(out.log, "[1/2]") {
		t.Errorf("恢复之后的日志里没有第一条的记录:\n%s", out.log)
	}
	if !strings.Contains(out.log, "[2/2]") {
		t.Errorf("恢复之后的日志里没有第二条的记录:\n%s", out.log)
	}
}

func TestStageExecute_AdvancesTheCursorAsItGoes(t *testing.T) {
	// 游标要**边走边记**,不是跑完一起记:进程在第二条之后挂掉时,库里得写着 2。
	fx := newExecFixture(t, "UPDATE t SET a=1; UPDATE t SET b=2")

	fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	got := fx.reloadStage()
	if got.ExecCursor != 2 {
		t.Errorf("执行完两条之后游标是 %d,期望 2", got.ExecCursor)
	}
}
```

夹具 `newExecFixture` 放在 `backend/internal/service/pipeline_fixture_test.go`，
后面四个任务都用它：

```go
package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
	"velagateway/internal/osc"
	"velagateway/internal/repository"
	"velagateway/internal/testsupport"
)

// 假执行器**只替换"把 SQL 发给数据库"这一步**。判定、审计、日志、游标都走真代码 ——
// 换掉更多的话,测的是夹具不是实现。
type fakeExecutor struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeExecutor) Run(_ context.Context, _ *model.Connection, sql string, _ time.Duration) gateway.ExecResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sql)
	return gateway.ExecResult{Output: "执行成功 · 1 行受影响", Rows: 1, Ms: 3}
}

func (f *fakeExecutor) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) }
func (f *fakeExecutor) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return ""
	}
	return f.sent[len(f.sent)-1]
}

// 假的在线变更执行器。真 Runner 要连 MySQL 才能发起,而这一层要测的是**接缝**:
// 交出去了没有、挂起了没有、结束之后接着往下走没有。
var (
	errOSCDisabled    = errors.New("在线表结构变更尚未启用")
	errNotRunningHere = errors.New("任务不在运行中")
)

type fakeOSC struct {
	mu        sync.Mutex
	startID   int64
	startErr  error
	abortErr  error
	abortedID map[int64]bool
}

func (f *fakeOSC) Start(context.Context, osc.StartRequest) (*osc.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return nil, f.startErr
	}
	return &osc.Job{ID: f.startID, Status: osc.JobPending}, nil
}

func (f *fakeOSC) Abort(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.abortErr != nil {
		return f.abortErr
	}
	if f.abortedID == nil {
		f.abortedID = map[int64]bool{}
	}
	f.abortedID[id] = true
	return nil
}

func (f *fakeOSC) aborted(id int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.abortedID[id]
}

type execFixture struct {
	t     *testing.T
	svc   *Services
	repo  *repository.Repo
	rel   *model.Release
	stage *model.ReleaseStage
	conn  *model.Connection
	user  *model.User
	exec  *fakeExecutor
	osc   *fakeOSC
	// rowsOfTable 是注入的行数来源,替掉真的 information_schema 查询。
	rowsOfTable int64
}

// newExecFixture 造一张停在执行阶段、人工闸已经点过的发布单。
//
// ConfirmedBy 预先填好是有意的:人工闸不是这几条用例要测的东西,而把它留在那里
// 会让每条用例都先走一遍"等待确认",测的是别人的逻辑。
func newExecFixture(t *testing.T, sql string) *execFixture {
	t.Helper()
	db := testsupport.NewDB(t)
	repo := repository.New(db)
	svc := New(repo, gateway.NewRiskEngine(repo), nil)

	fx := &execFixture{t: t, svc: svc, repo: repo, exec: &fakeExecutor{}, osc: &fakeOSC{}}
	svc.Executor = fx.exec
	svc.osc = fx.osc
	// 行数来源注入:真实现走 osc.Gather,那要一个真 MySQL,而这几条用例测的不是采集。
	svc.tableRowsFn = func(*model.Connection, string, string) int64 { return fx.rowsOfTable }

	fx.conn = seedFixtureConnection(t, repo)
	fx.user = seedFixtureUser(t, repo)
	fx.rel, fx.stage = seedFixtureRelease(t, repo, fx.conn, fx.user, sql)
	return fx
}

func (f *execFixture) saveStage() {
	f.t.Helper()
	if err := f.repo.UpdateReleaseStage(f.stage.ID, map[string]any{
		"exec_cursor": f.stage.ExecCursor, "log": f.stage.Log,
	}); err != nil {
		f.t.Fatalf("写阶段失败: %v", err)
	}
}

func (f *execFixture) reloadStage() *model.ReleaseStage {
	f.t.Helper()
	st, err := f.repo.GetReleaseStage(f.stage.ID)
	if err != nil {
		f.t.Fatalf("读阶段失败: %v", err)
	}
	return st
}

func (f *execFixture) reloadRelease() *model.Release {
	f.t.Helper()
	rel, err := f.repo.GetRelease(f.rel.ID)
	if err != nil {
		f.t.Fatalf("读发布单失败: %v", err)
	}
	return rel
}

// markReleaseWaiting 把单子推到 waiting —— AbortRelease 只接受 pending/waiting。
func (f *execFixture) markReleaseWaiting() {
	f.t.Helper()
	if err := f.repo.UpdateRelease(f.rel.ID, map[string]any{"status": model.RunWaiting}); err != nil {
		f.t.Fatalf("改单状态失败: %v", err)
	}
	f.rel.Status = model.RunWaiting
}
```

`seedFixtureConnection` / `seedFixtureUser` / `seedFixtureRelease` 三个种子函数写在同一
文件里：建一台 `engine=mysql` 的连接、一个平台管理员、一张带 `execute` 阶段
（`Type: model.StageExecute`、`Status: model.RunRunning`、`ConfirmedBy: "Lin Wei"`）的
发布单，并把 `sql` 放进 `Release.SQL`。三者都走 `repo` 的常规写入方法，不直接拼 SQL。

**`Services.Executor` 与 `Services.osc` 都要是接口**，否则假实现塞不进去——见 Task 8。

- [ ] **Step 2: 跑测试确认它红**

Run: `cd backend && go test ./internal/service/ -run TestStageExecute -v`
Expected: 三条都 FAIL（游标被无视、日志从空开始、游标不落库）。

- [ ] **Step 3: 改 `stageExecute`**

把逐条执行那段（`for i, one := range stmts`）改成：

```go
	timeout := s.asyncExecTimeout()
	var b strings.Builder
	// **接上已经写下的日志。** driveRelease 用 out.log 覆盖这一列,从空开始的话,
	// 这个阶段在暂停之前记下的每一条都会消失 —— 而那正是一次跨了几小时的执行最
	// 需要留下的东西。
	b.WriteString(st.Log)
	total := st.Rows
	for i := st.ExecCursor; i < len(stmts); i++ {
		one := stmts[i]
		res := s.Executor.Run(context.Background(), conn, one, timeout)
		if res.Err != nil {
			fmt.Fprintf(&b, "· 第 %d/%d 条失败: %s\n", i+1, len(stmts), clip(res.Output, 300))
			s.recordAuditBy(creator, releaseOperator(rel), conn, one, v.Risk, model.ResultWarn, rel.RelNo, "exec")
			return stageOutcome{status: model.RunFailed, rows: total,
				log: fmt.Sprintf("%s· 已执行 %d/%d 条后中止\n", b.String(), i, len(stmts))}
		}
		total += res.Rows
		fmt.Fprintf(&b, "· [%d/%d] %s (%dms)\n", i+1, len(stmts), clip(res.Output, 200), res.Ms)
		b.WriteString(resultPreview(res))
		s.recordAuditBy(creator, releaseOperator(rel), conn, one, v.Risk, model.ResultExecuted, rel.RelNo, "exec")
		// 游标边走边记:进程在下一条之前挂掉时,库里写着的必须是"已经做完 i+1 条"。
		// 跑完一起记的话,一次中途的崩溃会让恢复从头再来。
		_ = s.Repo.UpdateReleaseStage(st.ID, map[string]any{"exec_cursor": i + 1})
	}
	return stageOutcome{status: model.RunSuccess, rows: total,
		log: fmt.Sprintf("%s· 执行完成 · %d 条语句 · 影响 %d 行\n", b.String(), len(stmts), total)}
```

- [ ] **Step 4: 跑测试确认转绿**

Run: `cd backend && go test ./internal/service/ -run TestStageExecute -v`
Expected: 全部 PASS。再跑 `go test ./internal/service/` 确认没有回归。

- [ ] **Step 5: 变异验证**

去掉 `b.WriteString(st.Log)`，跑 `TestStageExecute_KeepsTheLog...`，确认它红；
把游标落库挪到循环外，跑 `TestStageExecute_AdvancesTheCursor...`——**它可能仍然绿**
（循环结束后写一次结果相同），所以再加一条：让第二条语句失败，断言游标是 1。
把那条断言补进测试文件，确认变异被抓住后改回来。

- [ ] **Step 6: 提交**

```bash
git add backend/internal/service/pipeline.go backend/internal/service/pipeline_execcursor_test.go
git commit -m "$(cat <<'EOF'
feat(pipeline): an execute stage that can resume from where it paused

游标边走边记,日志接着上一次写。两件事都是为同一个场景准备的:一条语句要跑
几小时,阶段在那期间停下,恢复时既不能重跑已经做过的,也不能把之前的记录抹掉。

重跑的后果不会报错 —— 一条 ALTER 重跑会报 1061,但一条 UPDATE 不会。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: 执行阶段接上 OSC 分支

**Files:**
- Create: `backend/internal/service/pipeline_osc.go`
- Modify: `backend/internal/service/pipeline.go`（循环里插入判定）、`backend/internal/service/service.go`（`Services` 持有 Runner）
- Modify: `backend/internal/bootstrap/router.go`（把 Runner 交给 Services）
- Test: `backend/internal/service/pipeline_osc_test.go`

**Interfaces:**
- Consumes: `oscroute.Decide(...)`（Task 4）、`osc.Runner.Start(...)`、`model.ReleaseStage.OSCJobID`
- Produces:
  ```go
  // 两个小接口 —— 它们存在只有一个理由:这一层的用例要能在**没有真 MySQL**的情况下
  // 跑。断言"交出去了没有、挂起了没有"不该要求先搭一套目标库。
  type sqlExecutor interface {
      Run(ctx context.Context, conn *model.Connection, sql string, timeout time.Duration) gateway.ExecResult
      Test(conn *model.Connection) (bool, string)
  }
  type oscExecutor interface {
      Start(ctx context.Context, req osc.StartRequest) (*osc.Job, error)
      Abort(ctx context.Context, id int64) error
  }

  func (s *Services) AttachOSC(r *osc.Runner, connect osc.ConnectFunc)
  func (s *Services) oscPolicy() oscroute.Policy
  func (s *Services) routeStatement(rel *model.Release, conn *model.Connection, sql string) (jobID int64, note string)
  ```

**`Services.Executor` 的类型要从 `*gateway.Executor` 改成 `sqlExecutor`。** 它只被调用
`Run` 和 `Test` 两个方法（`admin.go:88`、`gateway.go:340/345`、`async_exec.go:165`、
`export.go:459`、`pipeline.go:728/794/940`、`script_ref.go:181`），而 `*gateway.Executor`
天然满足这个接口，`bootstrap` 那边的赋值一个字都不用改。

- [ ] **Step 1: 写失败的测试**

`backend/internal/service/pipeline_osc_test.go`：

```go
package service

import (
	"strings"
	"testing"

	"velagateway/internal/model"
)

// 命中阈值的索引变更要改走 OSC,而这件事必须**在阶段日志里说出来** ——
// 一次"本该走 OSC 却没走"看不见的话,这个功能哪天失效了不会有任何迹象。

func TestStageExecute_HandsABigTableIndexChangeToOSC(t *testing.T) {
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX idx_memo (memo)")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000 // 假的行数来源,替掉真的 information_schema 查询
	fx.osc.startID = 17

	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	if out.status != model.RunWaiting {
		t.Fatalf("状态 = %s,期望 waiting(在等那个迁移跑完)。日志:%s", out.status, out.log)
	}
	if fx.exec.count() != 0 {
		t.Error("语句被直接下发了 —— 它本该交给 OSC")
	}
	if got := fx.reloadStage(); got.OSCJobID != 17 {
		t.Errorf("阶段挂着的任务是 %d,期望 17", got.OSCJobID)
	}
	if !strings.Contains(out.log, "OSC") {
		t.Errorf("日志里没说这一条走了 OSC:\n%s", out.log)
	}
}

func TestStageExecute_SmallTableGoesStraightThrough(t *testing.T) {
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX idx_memo (memo)")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 1_000

	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	if out.status != model.RunSuccess {
		t.Fatalf("状态 = %s,日志:%s", out.status, out.log)
	}
	if fx.exec.count() != 1 {
		t.Errorf("下发了 %d 条,期望 1 条(小表直发)", fx.exec.count())
	}
}

func TestStageExecute_FallsBackToDirectExecutionWhenOSCIsOff(t *testing.T) {
	// 决定 2:该走却走不了时直发,并把原因说出来。原生加索引本来就是在线的 ——
	// "没走 OSC"是失去了限流/从库友好/MDL 可重试这三件事,不是干了一件危险的事。
	// 让一个本来能跑的发布单卡死,理由却是"我们本想用个更温和的办法",不成立。
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX idx_memo (memo)")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startErr = errOSCDisabled // 发起被拒

	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	if out.status != model.RunSuccess {
		t.Fatalf("状态 = %s —— OSC 用不了不该让整张单失败。日志:%s", out.status, out.log)
	}
	if fx.exec.count() != 1 {
		t.Error("没有回落到直发")
	}
	if !strings.Contains(out.log, "直发") {
		t.Errorf("日志里没说清这一条为什么没走 OSC:\n%s", out.log)
	}
}

func TestStageExecute_StopsAtTheFirstOSCStatementAndLeavesTheRestAlone(t *testing.T) {
	// 逐条串行:命中的那条把阶段挂起,**后面的语句一条都不能先跑** ——
	// 顺序是发起人写下的,乱序执行的后果由数据承担。
	fx := newExecFixture(t, "UPDATE t SET a=1; ALTER TABLE t_order ADD INDEX i (c); UPDATE t SET b=2")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startID = 21

	out := fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	if out.status != model.RunWaiting {
		t.Fatalf("状态 = %s,期望 waiting", out.status)
	}
	if n := fx.exec.count(); n != 1 {
		t.Errorf("下发了 %d 条,期望只有第一条 —— 第三条抢在迁移前面跑了", n)
	}
	if got := fx.reloadStage(); got.ExecCursor != 1 {
		t.Errorf("游标是 %d,期望 1(第一条已完成,第二条正在 OSC 里跑)", got.ExecCursor)
	}
}
```

- [ ] **Step 2: 跑测试确认它红**

Run: `cd backend && go test ./internal/service/ -run TestStageExecute -v`
Expected: 四条新用例 FAIL（语句被直接下发、`OSCJobID` 仍是 0）。

- [ ] **Step 3: 实现**

`backend/internal/service/pipeline_osc.go`（新文件）：

```go
package service

// 发布流水线与 OSC 的接缝。
//
// 单独一个文件,因为 pipeline.go 已经很大,而这里的每一段都只为一件事服务:
// 让执行阶段能把一条语句交给一个要跑几小时的外部任务,然后在它结束时接着往下走。

import (
	"context"
	"fmt"

	"velagateway/internal/model"
	"velagateway/internal/osc"
	"velagateway/internal/oscroute"
)

// AttachOSC 把在线变更接进来。不接的话自动路由整个不存在,执行阶段照旧直发。
func (s *Services) AttachOSC(r *osc.Runner, connect osc.ConnectFunc) {
	s.osc = r
	s.tableRowsFn = func(conn *model.Connection, schema, table string) int64 {
		return gatherRows(connect, conn.ID, schema, table)
	}
}

// oscPolicy 读平台对自动路由的立场。
//
// 默认开着是安全的:OSC 的总开关默认关着,所以在没打开 OSC 的部署上它只会走到
// "直发并说明"那条路 —— 不改变任何现有行为,只多一行日志。
func (s *Services) oscPolicy() oscroute.Policy {
	return oscroute.Policy{
		AutoRoute: s.Repo.SettingBool("osc.autoRoute.enabled", true),
		MinRows:   int64(s.Repo.SettingInt("osc.autoRoute.minRows", 2_000_000)),
	}
}

// tableRows 是这张表的**估算**行数。
//
// 走 information_schema(osc.Gather 读的就是它),不做 COUNT(*):八百万行上要跑
// 几十秒,而它换来的精度在这里没有价值 —— 边界上误判的后果是"走了/没走 OSC",
// 两边都不危险。
//
// 拿不到就返回 0,也就是"当它是小表" —— 与 Preflight 对"拿不到磁盘余量不拦"
// 相反的方向,但同一个立场:未知的时候选那个不会把事情搞砸的答案。这里直发是
// 安全的那一边。
func gatherRows(connect osc.ConnectFunc, connID int64, schema, table string) int64 {
	db, _, err := connect(connID)
	if err != nil {
		return 0
	}
	facts, err := osc.Gather(context.Background(), db, schema, table)
	if err != nil {
		return 0
	}
	return facts.EstimatedRows
}

// routeStatement 决定一条语句的去向,并在决定走 OSC 时把任务发起出来。
//
// 返回的 jobID 为 0 表示"这条语句直发" —— **包括决定走 OSC 但发起失败的情况**。
// 那是决定 2:该走却走不了时直发,并把原因说出来。原生加索引本来就是在线的,
// "没走 OSC"是失去了限流/从库友好/MDL 可重试这三件事,不是干了一件危险的事;
// 让一个本来能跑的发布单卡死,理由却是"我们本想用个更温和的办法",不成立。
func (s *Services) routeStatement(rel *model.Release, conn *model.Connection, sql string) (jobID int64, note string) {
	if s.osc == nil {
		return 0, "直发:本部署没有接入 OSC"
	}
	// 行数是懒查的:Decide 只在认出这是索引 DDL、且没被覆盖或策略拦下时才回调它。
	d := oscroute.Decide(sql, conn.Engine, s.oscPolicy(), oscroute.Override(rel.OSCMode),
		func(table string) int64 {
			if s.tableRowsFn == nil {
				return 0
			}
			return s.tableRowsFn(conn, rel.Database, table)
		})
	if !d.UseOSC {
		return 0, d.Reason
	}
	job, err := s.osc.Start(context.Background(), osc.StartRequest{
		ConnectionID: conn.ID, Schema: rel.Database, Table: d.Table,
		Alter: d.Alter, CreatedBy: rel.Creator,
	})
	if err != nil {
		return 0, fmt.Sprintf("直发:本该走 OSC(%s),但发起失败 —— %v", d.Reason, err)
	}
	return job.ID, fmt.Sprintf("%s · 任务 #%d", d.Reason, job.ID)
}
```

`Services` 结构体（`backend/internal/service/service.go`）：`Executor` 改成 `sqlExecutor`，
并加两个字段：

```go
	// osc 是在线变更的执行器,可能为 nil(没接的部署照旧直发)。
	//
	// 类型是接口不是 *osc.Runner:真 Runner 要连 MySQL 才发起得了,而这一层的用例
	// 测的是**接缝** —— 交出去了没有、挂起了没有、结束之后接着往下走没有。
	osc oscExecutor
	// tableRowsFn 是表的估算行数从哪来。AttachOSC 把它设成真实现(osc.Gather),
	// 用例覆盖它 —— 采集本身在 osc 包里已经对着真 MySQL 测透了,不必在这里再测一遍。
	tableRowsFn func(conn *model.Connection, schema, table string) int64
```

`stageExecute` 的循环里，在 `s.Executor.Run` 之前插入：

```go
		// 这一条该不该改走 OSC?
		jobID, note := s.routeStatement(rel, conn, one)
		fmt.Fprintf(&b, "· [%d/%d] %s\n", i+1, len(stmts), note)
		if jobID > 0 {
			// 挂起:后面的语句一条都不能先跑 —— 顺序是发起人写下的,
			// 乱序执行的后果由数据承担。
			_ = s.Repo.UpdateReleaseStage(st.ID, map[string]any{
				"osc_job_id": jobID, "exec_cursor": i,
			})
			return stageOutcome{status: model.RunWaiting, rows: total,
				log: fmt.Sprintf("%s· 等待迁移任务 #%d 完成\n", b.String(), jobID)}
		}
```

`bootstrap/router.go` 在创建 Runner 之后加一行 `svc.AttachOSC(runner, oscConnect(repo))`。

- [ ] **Step 4: 跑测试确认转绿**

Run: `cd backend && go test ./internal/service/ -run TestStageExecute -v`
Expected: 全部 PASS。

- [ ] **Step 5: 变异验证**

把「挂起时 return」改成 `continue`，跑
`TestStageExecute_StopsAtTheFirstOSCStatementAndLeavesTheRestAlone`，确认它报出
第三条抢跑；改回来。

- [ ] **Step 6: 提交**

```bash
git add backend/internal/service/pipeline_osc.go backend/internal/service/pipeline.go \
        backend/internal/service/service.go backend/internal/service/pipeline_osc_test.go \
        backend/internal/bootstrap/router.go
git commit -m "$(cat <<'EOF'
feat(pipeline): hand a big-table index change to OSC instead of running it

命中阈值的那一条交给 OSC,阶段挂起等它;后面的语句一条都不先跑 —— 顺序是发起人
写下的,乱序执行的后果由数据承担。

走不了就直发,并说清为什么(决定 2):原生加索引本来就是在线的,"没走 OSC"是失去
了限流、从库跟得上、MDL 可重试这三件事,不是干了一件危险的事。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: 任务结束后驱动发布单继续

**Files:**
- Modify: `backend/internal/service/pipeline_osc.go`
- Modify: `backend/internal/repository/pipeline.go`（按 job 反查阶段）
- Modify: `backend/internal/bootstrap/router.go`（注册回调）
- Test: `backend/internal/service/pipeline_osc_test.go`

**Interfaces:**
- Consumes: `osc.Runner.OnFinish`（Task 6）
- Produces:
  ```go
  func (r *Repo) StageWaitingOnOSCJob(jobID int64) (*model.ReleaseStage, error)
  func (s *Services) OnOSCJobFinished(j *osc.Job)
  ```

- [ ] **Step 1: 写失败的测试**

追加到 `backend/internal/service/pipeline_osc_test.go`：

```go
func TestOnOSCJobFinished_ResumesTheReleaseAfterASuccessfulMigration(t *testing.T) {
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX i (c); UPDATE t SET b=2")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startID = 31
	fx.svc.stageExecute(fx.rel, fx.conn, fx.stage) // 第一条挂起在任务 31 上

	fx.svc.OnOSCJobFinished(&osc.Job{ID: 31, Status: osc.JobDone})

	got := fx.reloadStage()
	if got.OSCJobID != 0 {
		t.Errorf("任务结束了,阶段还挂着 %d", got.OSCJobID)
	}
	if got.ExecCursor != 1 {
		t.Errorf("游标是 %d,期望 1 —— 走 OSC 的那一条要算已完成", got.ExecCursor)
	}
	if fx.exec.count() != 1 {
		t.Errorf("第二条语句没有在迁移完成后被执行(下发了 %d 条)", fx.exec.count())
	}
}

func TestOnOSCJobFinished_FailsTheStageWhenTheMigrationFailed(t *testing.T) {
	// 迁移失败不能当作"这一条做完了"往下走:那条索引根本没加上,而后面的语句
	// 可能正依赖它。
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX i (c); UPDATE t SET b=2")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startID = 32
	fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)

	fx.svc.OnOSCJobFinished(&osc.Job{ID: 32, Status: osc.JobFailed, Err: "拷贝:连接中断"})

	got := fx.reloadStage()
	if got.Status != model.RunFailed {
		t.Errorf("迁移失败了,阶段状态却是 %s", got.Status)
	}
	if !strings.Contains(got.Log, "32") {
		t.Errorf("日志里没有点名那个任务:\n%s", got.Log)
	}
	if fx.exec.count() != 0 {
		t.Error("迁移失败之后,后面的语句仍然被执行了")
	}
}

func TestOnOSCJobFinished_IgnoresAJobNobodyIsWaitingOn(t *testing.T) {
	// 绝大多数任务是从 OSC 控制台手工发起的,不属于任何发布单。反查不到不是错误。
	fx := newExecFixture(t, "UPDATE t SET a=1")

	fx.svc.OnOSCJobFinished(&osc.Job{ID: 999, Status: osc.JobDone}) // 不该 panic

	if fx.exec.count() != 0 {
		t.Error("一个与发布单无关的任务推进了某张单")
	}
}
```

- [ ] **Step 2: 跑测试确认它红**

Run: `cd backend && go test ./internal/service/ -run TestOnOSCJobFinished -v`
Expected: 编译失败（`OnOSCJobFinished` 未定义）→ 加骨架 → 以断言失败的方式红。

- [ ] **Step 3: 实现**

`backend/internal/repository/pipeline.go`：

```go
// StageWaitingOnOSCJob 反查"哪个阶段在等这个迁移任务"。
//
// 查不到是**正常情况**:绝大多数任务是从 OSC 控制台手工发起的,不属于任何发布单。
func (r *Repo) StageWaitingOnOSCJob(jobID int64) (*model.ReleaseStage, error) {
	var s model.ReleaseStage
	if err := r.db.Where("osc_job_id = ?", jobID).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}
```

`backend/internal/service/pipeline_osc.go`：

```go
// OnOSCJobFinished 是挂给 Runner.OnFinish 的那个回调。
//
// 它只做一件事:把等着这个任务的那个阶段推下去。没有人在等就直接返回 —— 绝大多数
// 迁移是从 OSC 控制台手工发起的。
func (s *Services) OnOSCJobFinished(j *osc.Job) {
	if j == nil {
		return
	}
	st, err := s.Repo.StageWaitingOnOSCJob(j.ID)
	if err != nil || st == nil {
		return
	}
	if j.Status != osc.JobDone {
		// 迁移失败或被中止:这一条**没有做完**,不能当作完成往下走 —— 那条索引
		// 根本没加上,而后面的语句可能正依赖它。
		_ = s.Repo.UpdateReleaseStage(st.ID, map[string]any{
			"status":     model.RunFailed,
			"osc_job_id": 0,
			"log":        st.Log + fmt.Sprintf("· 迁移任务 #%d %s:%s\n", j.ID, j.Status, j.Err),
		})
		s.driveRelease(st.ReleaseID)
		return
	}
	// 走 OSC 的那一条到此算执行完毕,游标往前推一格。
	_ = s.Repo.UpdateReleaseStage(st.ID, map[string]any{
		"osc_job_id":  0,
		"exec_cursor": st.ExecCursor + 1,
		"status":      model.RunPending, // 交回给 driveRelease 重新认领
		"log":         st.Log + fmt.Sprintf("· 迁移任务 #%d 完成\n", j.ID),
	})
	s.driveRelease(st.ReleaseID)
}
```

`bootstrap/router.go` 在 `svc.AttachOSC(runner, oscConnect(repo))` 之后：

```go
	// 任务结束时把等着它的发布单推下去。osc 包不认识 pipeline —— 这条线在这里接。
	runner.OnFinish = svc.OnOSCJobFinished
```

- [ ] **Step 4: 跑测试确认转绿**

Run: `cd backend && go test ./internal/service/ -v`
Expected: 全部 PASS。

- [ ] **Step 5: 变异验证**

把失败分支改成和成功分支一样（推进游标、不置 failed），跑
`TestOnOSCJobFinished_FailsTheStageWhenTheMigrationFailed`，确认它红；改回来。

- [ ] **Step 6: 提交**

```bash
git add backend/internal/service/pipeline_osc.go backend/internal/repository/pipeline.go \
        backend/internal/bootstrap/router.go backend/internal/service/pipeline_osc_test.go
git commit -m "$(cat <<'EOF'
feat(pipeline): resume the release when its migration ends

迁移失败不能当作"这一条做完了"往下走:那条索引根本没加上,而后面的语句可能
正依赖它。

反查不到等它的阶段是正常情况 —— 绝大多数任务是从 OSC 控制台手工发起的。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: 中止发布单连带中止迁移

**Files:**
- Modify: `backend/internal/service/pipeline.go:1218-1243`（`AbortRelease`）
- Modify: `backend/internal/service/pipeline_osc.go`
- Test: `backend/internal/service/pipeline_osc_test.go`

**Interfaces:**
- Consumes: `osc.Runner.Abort(ctx, id)`、`osc.Runner.IsRunning(id)`
- Produces: `func (s *Services) abortOSCOfRelease(id int64) string`

- [ ] **Step 1: 写失败的测试**

```go
func TestAbortRelease_AlsoStopsTheMigrationItIsWaitingOn(t *testing.T) {
	// 否则单子停了、迁移还在拷全表 —— 而人以为自己已经把它按停了。
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX i (c)")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startID = 41
	fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)
	fx.markReleaseWaiting()

	if err := fx.svc.AbortRelease(fx.user, fx.rel.ID); err != nil {
		t.Fatalf("终止失败: %v", err)
	}

	if !fx.osc.aborted(41) {
		t.Error("发布单停了,它挂着的迁移任务还在跑")
	}
}

func TestAbortRelease_StillAbortsWhenTheMigrationCannotBeStopped(t *testing.T) {
	// Abort 只叫得停**本进程**手上的任务(ADR 0011)。多副本下另一台副本跑着的
	// 那个停不掉 —— 此时发布单照常中止,那个任务留成残局被列出来。
	// 谎称已经停掉它,比留着它更糟。
	fx := newExecFixture(t, "ALTER TABLE t_order ADD INDEX i (c)")
	fx.conn.Engine = "mysql"
	fx.rowsOfTable = 8_000_000
	fx.osc.startID = 42
	fx.osc.abortErr = errNotRunningHere
	fx.svc.stageExecute(fx.rel, fx.conn, fx.stage)
	fx.markReleaseWaiting()

	if err := fx.svc.AbortRelease(fx.user, fx.rel.ID); err != nil {
		t.Fatalf("迁移停不掉不该让终止本身失败: %v", err)
	}
	if got := fx.reloadRelease(); got.Status != model.RunAborted {
		t.Errorf("发布单状态 = %s,期望 aborted", got.Status)
	}
}
```

- [ ] **Step 2: 跑测试确认它红**

Run: `cd backend && go test ./internal/service/ -run TestAbortRelease -v`
Expected: 第一条 FAIL（任务没有被叫停）。

- [ ] **Step 3: 实现**

`pipeline_osc.go`：

```go
// abortOSCOfRelease 叫停这张单挂着的迁移,返回写进终止原因的一句话。
//
// **停不掉不是错误。** Abort 只叫得停本进程手上的任务(ADR 0011:IsRunning 说的是
// "这台网关没在推进它",不是"没有人在推进它")。多副本下另一台副本跑着的那个停不掉,
// 此时发布单照常中止,那个任务留成残局被列出来 —— 谎称已经停掉它比留着它更糟。
func (s *Services) abortOSCOfRelease(id int64) string {
	if s.osc == nil {
		return ""
	}
	for _, st := range s.stagesOf(id) {
		if st.OSCJobID == 0 {
			continue
		}
		if err := s.osc.Abort(context.Background(), st.OSCJobID); err != nil {
			return fmt.Sprintf(";挂着的迁移任务 #%d 未能叫停(%v),请到在线变更页确认它的残留", st.OSCJobID, err)
		}
		return fmt.Sprintf(";已连带叫停迁移任务 #%d", st.OSCJobID)
	}
	return ""
}
```

`AbortRelease` 里更新错误信息那句改成：

```go
	oscNote := s.abortOSCOfRelease(rel.ID)
	_ = s.Repo.UpdateRelease(rel.ID, map[string]any{
		"error": "已由 " + u.Name + " 终止" + oscNote, "finished_at": now,
	})
```

- [ ] **Step 4: 跑测试确认转绿**

Run: `cd backend && go test ./internal/service/ -run TestAbortRelease -v`
Expected: 全部 PASS。

- [ ] **Step 5: 变异验证**

把 `abortOSCOfRelease` 的调用去掉，跑第一条，确认它红；改回来。

- [ ] **Step 6: 提交**

```bash
git add backend/internal/service/pipeline.go backend/internal/service/pipeline_osc.go \
        backend/internal/service/pipeline_osc_test.go
git commit -m "$(cat <<'EOF'
feat(pipeline): aborting a release stops the migration it hangs on

否则单子停了、迁移还在拷全表,而人以为自己已经把它按停了。

停不掉不算错:Abort 只叫得停本进程手上的任务,多副本下另一台副本跑着的那个
停不掉。此时发布单照常中止,任务留成残局被列出来 —— 谎称已经停掉它更糟。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: 单次覆盖 `oscMode`（后端）

**Files:**
- Modify: `backend/internal/dto/pipeline.go:78-88`（`ReleaseReq`）
- Modify: `backend/internal/service/pipeline.go`（建单处，把它落进 `Release`）
- Test: `backend/internal/bootstrap/release_oscmode_test.go`（新）

**Interfaces:**
- Consumes: `model.Release.OSCMode`（Task 5）、`oscroute.Override`（Task 4）
- Produces: `dto.ReleaseReq.OSCMode string`

- [ ] **Step 1: 写失败的测试**

```go
package bootstrap

import (
	"net/http"
	"testing"

	"velagateway/pkg/resp"
)

// 单次覆盖是发起人对**这一单**的明确指令,它要落库并进审计 ——
// 一次例外要说得出是谁定的。

func TestRelease_KeepsTheOSCModeTheSubmitterChose(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/releases", token, map[string]any{
		"title": "给订单表加索引", "connectionId": 1, "database": "app",
		"sql": "ALTER TABLE t_order ADD INDEX idx_memo (memo)", "oscMode": "skip",
	})
	if r.Code != resp.CodeOK {
		t.Fatalf("建单失败: code=%d msg=%q", r.Code, r.Msg)
	}

	rel := app.lastRelease(t)
	if rel.OSCMode != "skip" {
		t.Errorf("落库的 oscMode 是 %q,期望 skip", rel.OSCMode)
	}
}

func TestRelease_RejectsAnUnknownOSCMode(t *testing.T) {
	// 拼错的值必须当场被拒,而不是被当成"按策略"静默吞掉:一个以为自己选了
	// "强制直发"的人,会看着一次走了 OSC 的执行不知所以。
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/releases", token, map[string]any{
		"title": "x", "connectionId": 1, "database": "app",
		"sql": "ALTER TABLE t_order ADD INDEX i (c)", "oscMode": "forse",
	})

	if r.Code == resp.CodeOK {
		t.Error("拼错的 oscMode 被接受了")
	}
}
```

- [ ] **Step 2: 跑测试确认它红**

Run: `cd backend && go test ./internal/bootstrap/ -run TestRelease_ -v`
Expected: FAIL（字段不存在 / 拼错的值被接受）。

- [ ] **Step 3: 实现**

`dto.ReleaseReq` 加：

```go
	// OSCMode 是对这一单的单次覆盖:"" 按策略,force 强制走 OSC,skip 强制直发。
	OSCMode string `json:"oscMode"`
```

建单处校验并落库：

```go
	// 拼错的值当场拒掉,不静默当成"按策略" —— 一个以为自己选了"强制直发"的人,
	// 会看着一次走了 OSC 的执行不知所以。
	switch req.OSCMode {
	case "", "force", "skip":
	default:
		return nil, fmt.Errorf("oscMode 只能是 force / skip 或留空,收到 %q", req.OSCMode)
	}
	rel.OSCMode = req.OSCMode
```

- [ ] **Step 4: 跑测试确认转绿**

Run: `cd backend && go test ./internal/bootstrap/ -run TestRelease_ -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/dto/pipeline.go backend/internal/service/pipeline.go \
        backend/internal/bootstrap/release_oscmode_test.go
git commit -m "$(cat <<'EOF'
feat(pipeline): let the submitter override the OSC routing for one release

拼错的值当场拒掉,不静默当成"按策略" —— 一个以为自己选了"强制直发"的人,
会看着一次走了 OSC 的执行不知所以。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: 自动路由的两个设置项（前端）

**Files:**
- Modify: `frontend/src/pages/settings/index.tsx`（Task 2 建的那个分区）
- Modify: `frontend/src/locales/zh.ts`, `frontend/src/locales/en.ts`

- [ ] **Step 1: 加字段与保存**

读设置处：

```tsx
    oscAutoRoute: parseSetting(g['osc.autoRoute.enabled'], true),
    oscMinRows: Number(parseSetting(g['osc.autoRoute.minRows'], 2000000)) || 2000000,
```

保存处：

```tsx
        'osc.autoRoute.enabled': f.oscAutoRoute,
        'osc.autoRoute.minRows': clampInt(f.oscMinRows, 10000, 1000000000, 2000000),
```

- [ ] **Step 2: 加界面**

在 Task 2 的那个 `<Card>` 里、急停开关之后：

```tsx
          <CardRow title={t('setOscAuto')} hint={t('setOscAutoD')}>
            <Switch checked={f.oscAutoRoute} onChange={(v) => set({ oscAutoRoute: v })} />
          </CardRow>
          {f.oscAutoRoute && (
            <CardRow title={t('setOscMinRows')} hint={t('setOscMinRowsD')}>
              <input className="set-in w160" type="number" min={10000} step={100000}
                     value={f.oscMinRows} onChange={(e) => set({ oscMinRows: Number(e.target.value) })} />
            </CardRow>
          )}
```

- [ ] **Step 3: 加文案**

`zh.ts`：

```ts
  setOscAuto: '大表索引变更自动走 OSC',
  setOscAutoD: '发布流水线执行到一条索引变更时,若目标表超过下面的行数,自动改用在线变更。OSC 用不了时(未启用、非 MySQL、前置检查不通过)照常直发,并把原因写进阶段日志。',
  setOscMinRows: '判定为大表的行数',
  setOscMinRowsD: '行数取自 information_schema,是**估算值**,可能与实际相差可观 —— 边界附近的表会时走时不走。正好等于这个数不算超过。',
```

`en.ts`：

```ts
  setOscAuto: 'Route big-table index changes through OSC',
  setOscAutoD: 'When a pipeline executes an index change on a table larger than the row count below, it switches to the online change. If OSC is unavailable (disabled, not MySQL, preflight refused) the statement runs directly and the reason is written into the stage log.',
  setOscMinRows: 'Rows that count as a big table',
  setOscMinRowsD: 'The row count comes from information_schema and is an ESTIMATE — it can be off by a lot, so tables near the boundary will sometimes route and sometimes not. Exactly equal does not count as over.',
```

- [ ] **Step 4: 类型检查与构建**

Run: `cd frontend && npm run type-check && npm run build`
Expected: 通过。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/pages/settings/index.tsx frontend/src/locales/zh.ts frontend/src/locales/en.ts
git commit -m "$(cat <<'EOF'
feat(osc): the auto-route policy, on the settings page

说明里写明行数是估算值 —— 边界附近的表会时走时不走,而人看到这件事时会先怀疑
自己的配置。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 13: 发布单界面（两种 waiting、单次覆盖、链到任务）

**Files:**
- Modify: `frontend/src/types/index.ts`（`ReleaseStage` 加两字段、`Release` 加一字段）
- Modify: `frontend/src/pages/changes/index.tsx`（`StageBox` 与提单表单）
- Modify: `frontend/src/api/modules/pipeline.ts`（`createRelease` 的 body）
- Modify: `frontend/src/locales/zh.ts`, `frontend/src/locales/en.ts`
- Test: `frontend/e2e/changes.spec.ts`（若无则新建 `frontend/e2e/changes-osc.spec.ts`）

- [ ] **Step 1: 写失败的 e2e**

`frontend/e2e/changes-osc.spec.ts`：

```ts
import { test, expect } from '@playwright/test'
import { envelope, seedSession, stubShell } from './fixtures'

// 挂在迁移任务上的执行阶段,不能给出「确认执行」按钮 —— 按下去只会让人以为
// 自己推进了什么。判据是 oscJobId,不是 status:两种 waiting 的 status 一模一样。

test('等迁移跑完的阶段不给「确认执行」,而是指向那个任务', async ({ page }) => {
  await seedSession(page)
  await page.route('**/api/v1/**', (r) => r.fulfill(envelope([])))
  await stubShell(page)
  await page.route('**/api/v1/releases/7', (r) =>
    r.fulfill(envelope({
      id: 7, relNo: 'REL-7', title: '给订单表加索引', status: 'waiting',
      connectionId: 1, database: 'app', sql: 'ALTER TABLE t_order ADD INDEX i (c)',
      oscMode: '', creator: 'Lin Wei',
      stages: [{
        id: 71, releaseId: 7, stepOrder: 1, name: '执行', type: 'execute',
        config: '', onFailure: 'abort', status: 'waiting',
        log: '· [1/1] 走 OSC:约 830 万行,超过阈值 200 万 · 任务 #17\n',
        findings: '', approvalId: 0, approvalNo: '', rows: 0,
        confirmedBy: 'Lin Wei', execCursor: 0, oscJobId: 17,
        startedAt: null, finishedAt: null,
      }],
    })))
  await page.goto('/changes/7')

  await expect(page.locator('.stage-osc-wait')).toContainText('#17')
  await expect(page.locator('button:has-text("确认执行")')).toHaveCount(0)
})
```

- [ ] **Step 2: 跑它确认红**

Run: `cd frontend && npx playwright test e2e/changes-osc.spec.ts`
Expected: FAIL（`.stage-osc-wait` 不存在）。

- [ ] **Step 3: 加类型**

`frontend/src/types/index.ts` 的 `ReleaseStage` 加：

```ts
  /** 这个执行阶段已经执行完的语句条数。跨 waiting 恢复时靠它不重跑。 */
  execCursor: number
  /**
   * 此刻挂着的 OSC 迁移任务(0 = 没挂)。
   *
   * 它是界面区分两种 waiting 的**唯一**判据:一种在等人点「确认执行」,一种在等
   * 一个迁移跑完,而两者的 status 一模一样。给后者画上按钮,按下去只会让人以为
   * 自己推进了什么。
   */
  oscJobId: number
```

`Release` 加：

```ts
  /** 发起人对这一单的单次覆盖:'' 按策略,force 强制走 OSC,skip 强制直发。 */
  oscMode: '' | 'force' | 'skip'
```

- [ ] **Step 4: 改 StageBox**

`frontend/src/pages/changes/index.tsx:229-233` 现在是：

```tsx
        {isGate && (
          <Button variant="primary" disabled={busy} onClick={onAdvance}>
            <Play size={13} />{stage.type === 'execute' ? t('chgConfirmExec') : t('chgAdvance')}
          </Button>
        )}
```

改成（`isGate` 的计算处也要排掉挂着任务的阶段，否则它在别处仍被当成一道人工闸）：

```tsx
        {/*
          挂着迁移任务的执行阶段**不是一道人工闸**。它的 status 和"等人点确认执行"
          一模一样,而判据是 oscJobId —— 画上按钮的话,按下去只会让人以为自己推进了
          什么。与 ADR 0011 里「中止按钮跟着 running 走而不是跟着 status 走」同一类。
        */}
        {stage.oscJobId > 0 ? (
          <Link className="stage-osc-wait" to="/osc">
            {t('chgOscWaiting', { id: stage.oscJobId })}
          </Link>
        ) : isGate && (
          <Button variant="primary" disabled={busy} onClick={onAdvance}>
            <Play size={13} />{stage.type === 'execute' ? t('chgConfirmExec') : t('chgAdvance')}
          </Button>
        )}
```

`isGate` 的定义处（同文件上方）加上 `&& stage.oscJobId === 0`。

- [ ] **Step 5: 补「任务已经没人推进」的残局判据（后端 + 前端）**

网关重启后，阶段停在 `waiting`、任务停在中间态，而**没有任何进程在推进它**。这跟
OSC 自己的残局是同一种东西，判据也用同一个：`Runner.IsRunning`。

后端 `model.ReleaseStage` 加一个**不落库**的字段，与 `osc.Job.Running` 完全同一个做法
（ADR 0011 里那个先例：库里一条 `copying` 的记录，可能正在跑，也可能是上个进程死在
半路留下的，`status` 分不开这两件事）：

```go
	// OSCRunning 不落库(`gorm:"-"`):**本进程此刻**有没有在推进它挂着的那个迁移。
	//
	// 它回答 status 回答不了的问题:一个 waiting 的执行阶段,可能正等着一个真的在跑
	// 的迁移,也可能是网关重启之后留下的空等 —— 两者的 status 一模一样,而后者永远
	// 不会自己走完。
	OSCRunning bool `gorm:"-" json:"oscRunning"`
```

读发布单的 handler 里填它（`h.osc.runner.IsRunning(st.OSCJobID)`），前端：

```tsx
      {st.oscJobId > 0 && !st.oscRunning && (
        <div className="stage-osc-orphan">{t('chgOscOrphan', { id: st.oscJobId })}</div>
      )}
```

文案（`zh.ts` / `en.ts`）：

```ts
  chgOscOrphan: '网关重启过,迁移任务 #{{id}} 已经没有进程在推进它 —— 它不会自己走完。到在线变更页收拾它留下的影子表,再决定这张单怎么办。',
  chgOscOrphan: 'The gateway restarted; no process is driving migration #{{id}} any more — it will not finish on its own. Clean up the shadow table it left on the online-change page, then decide what to do with this release.',
```

**不自动重试，也不自动失败。** 一个跑到一半的迁移留下的是影子表和一段没追平的
binlog，要由人看一眼再决定（与 ADR 0011 对残局的处理一致）。

- [ ] **Step 6: 提单表单加单次覆盖**

三选一（按策略 / 强制走 OSC / 强制直发），把值放进 `createRelease` 的 body：
`oscMode: form.oscMode`。`pipeline.ts` 的 `createRelease` 签名补上
`oscMode?: string`。

- [ ] **Step 7: 加文案**

```ts
// zh
  chgOscWaiting: '正在等在线变更任务 #{{id}} 完成 —— 点此查看进度',
  chgOscMode: '大表索引变更',
  chgOscModeAuto: '按平台策略',
  chgOscModeForce: '强制走在线变更',
  chgOscModeSkip: '强制直接执行',
// en
  chgOscWaiting: 'Waiting for online change job #{{id}} — open it to see progress',
  chgOscMode: 'Big-table index change',
  chgOscModeAuto: 'Follow platform policy',
  chgOscModeForce: 'Force the online change',
  chgOscModeSkip: 'Run it directly',
```

- [ ] **Step 8: 跑测试确认转绿**

Run: `cd frontend && npx playwright test e2e/changes-osc.spec.ts && npm run test:unit && npm run type-check`
Expected: 全部通过。

- [ ] **Step 9: 变异验证**

把判据从 `st.oscJobId > 0` 改成 `st.status === 'waiting'`，确认 e2e 红；改回来。

- [ ] **Step 10: 提交**

```bash
git add frontend/src/types/index.ts frontend/src/pages/changes/index.tsx \
        frontend/src/api/modules/pipeline.ts frontend/src/locales/*.ts frontend/e2e/changes-osc.spec.ts
git commit -m "$(cat <<'EOF'
feat(pipeline): tell the two kinds of waiting apart in the release view

一种在等人点「确认执行」,一种在等一个迁移跑完,而两者的 status 一模一样。
判据是 oscJobId —— 给后者画上按钮,按下去只会让人以为自己推进了什么。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 14: 端到端（真 MySQL）

**Files:**
- Create: `backend/internal/bootstrap/release_osc_e2e_test.go`

**Interfaces:**
- Consumes: 前面所有任务

- [ ] **Step 1: 写测试**

```go
package bootstrap

import (
	"testing"
	"time"
)

// 一张发布单从提交走到完成,中间那条索引变更**真的**由 OSC 做掉。
//
// 前面每一层都能单独绿:判定是纯函数,状态机用假执行器,回调用假任务。但"一次
// 发布单真的靠一次真实的迁移完成了"只有把它们串起来才验得到 —— 而这条链路上
// 任何一个接头松了,症状都是"单子永远停在 waiting",没有任何报错。
//
// 要真 MySQL(VELA_OSC_MYSQL_DSN),连不上是失败不是 skip。
func TestReleaseE2E_TheIndexChangeIsMadeByOSCAndTheReleaseCompletes(t *testing.T) {
	app := newTestApp(t)
	app.cfg.OSC.Enabled = true
	mustSetSetting(t, app, "osc.autoRoute.enabled", "true")
	mustSetSetting(t, app, "osc.autoRoute.minRows", "100") // 夹具表只有几千行

	// 这四个辅助写在本文件里,都走已有的 app.do / app.repo,不另起一套:
	//
	//   seedMySQLTarget  —— 往 tbl_connection 插一台指向 VELA_OSC_MYSQL_DSN 的实例
	//                       (engine=mysql、凭据齐全,否则 Executor 会走模拟执行)
	//   seedBigTable     —— 在那台实例上 CREATE TABLE 并灌 n 行,返回表名,
	//                       t.Cleanup 里 DROP 掉它和它可能留下的 _gho/_del/_ghc
	//   waitForRelease   —— 轮询 GET /api/v1/releases/{id} 直到终态或超时
	//   indexExists      —— 对着那台实例查 information_schema.STATISTICS
	connID, targetDB := seedMySQLTarget(t, app)
	table := seedBigTable(t, app, targetDB, 3000)

	token := app.login("linwei@vela.io", "vela123")
	r := app.do(http.MethodPost, "/api/v1/releases", token, map[string]any{
		"title": "e2e 加索引", "connectionId": connID, "database": "osc_test",
		"sql": "ALTER TABLE " + table + " ADD INDEX idx_memo (memo)",
	})
	if r.Code != resp.CodeOK {
		t.Fatalf("建单失败: code=%d msg=%q", r.Code, r.Msg)
	}
	rel := app.lastRelease(t)

	// 执行闸:一律先停在这里,有人点过才真的下发(ADR 0010)。
	stage := app.executeStageOf(t, rel.ID)
	if rr := app.do(http.MethodPost,
		fmt.Sprintf("/api/v1/releases/%d/stages/%d/continue", rel.ID, stage.ID), token, nil); rr.Code != resp.CodeOK {
		t.Fatalf("推进执行闸失败: code=%d msg=%q", rr.Code, rr.Msg)
	}

	final := waitForRelease(t, app, rel.ID, 180*time.Second)
	if final.Status != model.RunSuccess {
		t.Fatalf("发布单最终状态 = %s(%s)", final.Status, final.Error)
	}
	// 索引真的加上了,而且**是 OSC 加的**:后者靠任务记录证明 —— 少了这条断言,
	// 一次悄悄回落到直发的执行也会让这个用例变绿。
	if !indexExists(t, targetDB, table, "idx_memo") {
		t.Error("发布单报告成功,索引却不在")
	}
	var jobs int64
	app.repo.DB().Model(&osc.Job{}).Where("table_name = ?", table).Count(&jobs)
	if jobs == 0 {
		t.Error("没有任何 OSC 任务 —— 这一条其实是直发的,路由没有生效")
	}
}
```

- [ ] **Step 2: 跑它**

Run: `cd backend && go test ./internal/bootstrap/ -run TestReleaseE2E -v -timeout 300s`
Expected: PASS。第一次多半会红在某个接头上——那正是这条用例的价值。

- [ ] **Step 3: 变异验证**

把 `router.go` 里的 `runner.OnFinish = svc.OnOSCJobFinished` 注释掉，跑这条用例，
确认它以「发布单停在 waiting」超时失败；改回来。

- [ ] **Step 4: 提交**

```bash
git add backend/internal/bootstrap/release_osc_e2e_test.go
git commit -m "$(cat <<'EOF'
test(pipeline): one release, completed by a real OSC migration

前面每一层都能单独绿。但这条链路上任何一个接头松了,症状都是"单子永远停在
waiting" —— 没有报错,没有失败,只是不动了。

变异验证:摘掉 OnFinish 的接线,这条用例以超时失败。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 15: 文档回写

**Files:**
- Modify: `docs/adr/0011-mysql-online-schema-change.md`
- Modify: `backend/docs/openapi.yaml`
- Modify: `docs/superpowers/specs/2026-09-19-osc-auto-route-design.md`（状态改为已实现）

- [ ] **Step 1: ADR 0011 加一节**

在「界面:一个默认关着的开关」之后加「第二个入口:发布流水线」，写明：

- 自动路由的判定顺序与阈值来源
- **授权边界的放宽**：OSC 的 HTTP 发起限平台管理员，而发布单的执行闸不是；决定是
  「发布单的审批链就是授权」，并说明低风险单可能根本没有审批阶段，那时等于
  「执行闸那个人就是授权」
- 急停开关的不对称方向
- 降级直发的立场（决定 2）

- [ ] **Step 2: openapi 补字段说明**

`/releases` 的 POST 描述里补 `oscMode`；`/releases/{id}` 的描述里补执行阶段可能
挂在 OSC 任务上（`oscJobId`）。

- [ ] **Step 3: 提交**

```bash
git add docs/adr/0011-mysql-online-schema-change.md backend/docs/openapi.yaml \
        docs/superpowers/specs/2026-09-19-osc-auto-route-design.md
git commit -m "$(cat <<'EOF'
docs(osc): the pipeline is now a second entrance, and it widened a boundary

OSC 的发起此前限平台管理员。自动路由接上之后,发布单的执行闸也能发起一次迁移,
而那个闸不要求管理员 —— 决定是"发布单的审批链就是授权"。

一条授权的放宽不该只活在代码里,所以它写在 ADR 里,连同它的边界:低风险单可能
根本没有审批阶段,那时"审批链就是授权"等于"执行闸那个人就是授权"。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## 收尾检查

- [ ] `cd backend && go build ./... && VELA_OSC_EXPECT_REPLICA=1 go test ./...`
- [ ] `cd frontend && npm run type-check && npm run lint && npm run test:unit && npx playwright test`
- [ ] `gofmt -l backend/internal/oscroute backend/internal/service backend/internal/osc` 为空
- [ ] ADR 0011 与 spec 的状态行都已更新
