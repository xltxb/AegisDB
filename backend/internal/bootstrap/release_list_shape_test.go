package bootstrap

// 发布单列表 = 详情去掉阶段输出。
//
// 列表接口曾经逐单查阶段(N+1)、并给每个阶段的日志脱敏后再丢掉 —— 发布页每 2.5 秒轮询它,
// 存储库连接被排队拖死。改成批量取阶段、不碰日志之后,这里钉住对外输出不变:
//   - 每张单的列表项与详情逐字段一致,只是阶段的 log / logRefs / findings / config 为空、
//     SQL 截到 400 字;
//   - 阶段按 step_order、id 排序,且只属于本单(批量取再分组,不能串单);
//   - 没有阶段的单给 [],不是 null;
//   - SQL / error 在列表与详情里都已脱敏,口令一个字也不出现;
//   - 详情照旧带日志,且日志已脱敏。

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"aegisdb/internal/model"
)

const listShapeSecret = "s3cretpw"

func (a *testApp) seedListShapeFixture() (withStages, noStages, other model.Release) {
	a.t.Helper()
	adminID := a.searchUserID("linwei@aegisdb.io")
	longSQL := "ALTER USER 'x' IDENTIFIED BY '" + listShapeSecret + "';\n" + strings.Repeat("update t set n = n + 1 where id = 1;\n", 30)
	mk := func(no, title, sql, errText string) model.Release {
		r := model.Release{RelNo: no, Title: title, SQL: sql, Error: errText, CreatorID: adminID,
			Env: "fat", TierCode: "dev", PipelineID: 1, ConnectionID: 1, Status: "failed"}
		if err := a.repo.DB().Create(&r).Error; err != nil {
			a.t.Fatalf("seed %s: %v", no, err)
		}
		return r
	}
	withStages = mk("REL-801", "带阶段", longSQL, "执行失败:IDENTIFIED BY '"+listShapeSecret+"'")
	noStages = mk("REL-802", "无阶段", "select 1;", "")
	other = mk("REL-803", "另一张", "update t set n = 2 where id = 2;", "")

	stage := func(relID int64, order int, name, typ string) {
		st := model.ReleaseStage{ReleaseID: relID, StepOrder: order, Name: name, Type: typ, Status: "success",
			Config:   `{"failOn":"none"}`,
			Log:      "[1/1] ALTER USER 'x' IDENTIFIED BY '" + listShapeSecret + "' — 0 行受影响",
			LogRefs:  `[{"code":"x","args":{"sql":"ALTER USER 'x' IDENTIFIED BY '` + listShapeSecret + `'"}}]`,
			Findings: `[{"rule":"r1"}]`, ConfirmedBy: "linwei", ApprovalNo: "AP-" + name, Rows: order}
		if err := a.repo.DB().Create(&st).Error; err != nil {
			a.t.Fatalf("seed stage: %v", err)
		}
	}
	// 故意乱序插入,并让两个阶段 step_order 相同 —— 顺序要靠 step_order, id 排,不靠插入顺序。
	stage(other.ID, 1, "别人的审查", "review")
	stage(withStages.ID, 3, "通知", "notify")
	stage(withStages.ID, 1, "审查", "review")
	stage(other.ID, 2, "别人的执行", "execute")
	stage(withStages.ID, 2, "执行", "execute")
	stage(withStages.ID, 1, "审查二", "review")
	return
}

func (a *testApp) rawJSON(path, token string) json.RawMessage {
	a.t.Helper()
	r := a.do(http.MethodGet, path, token, nil)
	if r.Code != 0 {
		a.t.Fatalf("GET %s: %d %s", path, r.Code, r.Msg)
	}
	return r.Data
}

// listItemFromDetail 是"列表项应有的样子":详情去掉阶段输出、SQL 截 400 字。
func listItemFromDetail(t *testing.T, detail json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(detail, &m); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if s, ok := m["sql"].(string); ok {
		if r := []rune(s); len(r) > 400 {
			m["sql"] = string(r[:400]) + "…"
		}
	}
	for _, st := range m["stages"].([]any) {
		sm := st.(map[string]any)
		sm["log"], sm["findings"], sm["config"] = "", "", ""
		delete(sm, "logRefs") // omitempty
	}
	return m
}

func listItems(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()
	var p struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return p.Items
}

func stageNames(m map[string]any) []string {
	out := []string{}
	for _, st := range m["stages"].([]any) {
		out = append(out, st.(map[string]any)["name"].(string))
	}
	return out
}

func TestReleaseListIsDetailWithoutStageOutput(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	a, b, c := app.seedListShapeFixture()

	raw := app.rawJSON("/api/v1/releases?scope=all&pageSize=100", admin)
	if strings.Contains(string(raw), listShapeSecret) {
		t.Errorf("列表里出现了口令原文:%s", raw)
	}
	byID := map[int64]map[string]any{}
	for _, it := range listItems(t, raw) {
		byID[int64(it["id"].(float64))] = it
	}

	for _, rel := range []model.Release{a, b, c} {
		got, ok := byID[rel.ID]
		if !ok {
			t.Fatalf("%s 不在列表里", rel.RelNo)
		}
		detail := app.rawJSON("/api/v1/releases/"+itoa(rel.ID), admin)
		if strings.Contains(string(detail), listShapeSecret) {
			t.Errorf("%s 详情里出现了口令原文", rel.RelNo)
		}
		if want := listItemFromDetail(t, detail); !reflect.DeepEqual(got, want) {
			gj, _ := json.MarshalIndent(got, "", " ")
			wj, _ := json.MarshalIndent(want, "", " ")
			t.Errorf("%s 列表项与详情(去阶段输出)不一致\n列表:%s\n应为:%s", rel.RelNo, gj, wj)
		}
	}

	if got := stageNames(byID[a.ID]); !reflect.DeepEqual(got, []string{"审查", "审查二", "执行", "通知"}) {
		t.Errorf("REL-801 阶段顺序/归属不对:%v", got)
	}
	if got := stageNames(byID[c.ID]); !reflect.DeepEqual(got, []string{"别人的审查", "别人的执行"}) {
		t.Errorf("REL-803 阶段串单:%v", got)
	}
	if st, ok := byID[b.ID]["stages"].([]any); !ok || len(st) != 0 {
		t.Errorf("无阶段的单应给 [],实际 %#v", byID[b.ID]["stages"])
	}
	if s := byID[a.ID]["sql"].(string); !strings.HasSuffix(s, "…") || len([]rune(s)) != 401 {
		t.Errorf("列表 SQL 应截到 400 字加省略号,实际 %d 字", len([]rune(s)))
	}
	if !strings.Contains(byID[a.ID]["error"].(string), "'***'") {
		t.Errorf("列表 error 应已脱敏,实际 %q", byID[a.ID]["error"])
	}
}

// 详情照旧带完整阶段输出,日志已脱敏。
func TestReleaseDetailKeepsRedactedStageOutput(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	a, _, _ := app.seedListShapeFixture()

	d := app.getRelease(admin, a.ID)
	if len(d.Stages) != 4 {
		t.Fatalf("详情应有 4 个阶段,实际 %d", len(d.Stages))
	}
	for _, st := range d.Stages {
		if !strings.Contains(st.Log, "IDENTIFIED BY '***'") {
			t.Errorf("详情阶段 %s 日志应带脱敏后的语句,实际 %q", st.Name, st.Log)
		}
		if st.LogRefs == "" || strings.Contains(st.LogRefs, listShapeSecret) {
			t.Errorf("详情阶段 %s logRefs 应存在且已脱敏,实际 %q", st.Name, st.LogRefs)
		}
		if st.Findings == "" {
			t.Errorf("详情阶段 %s 应带 findings", st.Name)
		}
	}
	var full map[string]any
	_ = json.Unmarshal(app.rawJSON("/api/v1/releases/"+itoa(a.ID), admin), &full)
	if s := full["sql"].(string); strings.HasSuffix(s, "…") || !strings.Contains(s, "'***'") {
		t.Errorf("详情 SQL 应是脱敏后的全文,不截断")
	}
}

// 分页:第二页的单也带着自己的阶段(批量取阶段只取本页的单,不能错位)。
func TestReleaseListPagedStagesStayWithTheirRelease(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	a, b, c := app.seedListShapeFixture()
	want := map[int64][]string{
		a.ID: {"审查", "审查二", "执行", "通知"},
		b.ID: {},
		c.ID: {"别人的审查", "别人的执行"},
	}
	seen := 0
	for page := 1; page <= 3; page++ {
		items := listItems(t, app.rawJSON("/api/v1/releases?scope=all&pageSize=1&page="+itoa(int64(page)), admin))
		if len(items) != 1 {
			t.Fatalf("第 %d 页应有 1 张,实际 %d", page, len(items))
		}
		id := int64(items[0]["id"].(float64))
		if got := stageNames(items[0]); !reflect.DeepEqual(got, want[id]) {
			t.Errorf("第 %d 页 id=%d 阶段 %v,应为 %v", page, id, got, want[id])
		}
		seen++
	}
	if seen != 3 {
		t.Fatalf("应翻到 3 张,实际 %d", seen)
	}
}
