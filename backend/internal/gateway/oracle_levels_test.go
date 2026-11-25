package gateway

// 批量重编译的**依赖分层**。
//
// 这是 utlrp 的做法:被依赖的先编,依赖别人的后编,一轮就能把一条依赖链编干净。没有
// 分层时同一条链要靠"多编几轮"硬碰,链有多长就要多少轮,而轮数封顶是 3 —— 一条四层的
// 链在没有分层的世界里**永远**编不干净,报告却只会说"仍失败 1"。
//
// 没有真实 Oracle,能钉住的是不碰数据库的三段:把 all_dependencies 的行折成这批对象内部
// 的边、把边分成层、以及分层之后的编译顺序确实是逐层往上。

import (
	"strings"
	"testing"
)

func names(targets []InvalidObject, idx []int) string {
	parts := make([]string, 0, len(idx))
	for _, i := range idx {
		parts = append(parts, targets[i].Name)
	}
	return strings.Join(parts, ",")
}

// 四层链 D→C→B→A(D 依赖 C……),清单顺序故意倒着给。
func chain4() ([]InvalidObject, []DepEdge) {
	t := []InvalidObject{obj("D", "package"), obj("C", "package"), obj("B", "package"), obj("A", "package")}
	return t, []DepEdge{{From: 0, To: 1}, {From: 1, To: 2}, {From: 2, To: 3}}
}

func TestLevelize_ChainBecomesOneObjectPerLevel(t *testing.T) {
	targets, edges := chain4()
	levels, cyclic := levelize(len(targets), edges)
	if cyclic != 0 {
		t.Errorf("一条链没有环,实际 cyclic=%d", cyclic)
	}
	got := make([]string, 0, len(levels))
	for _, lv := range levels {
		got = append(got, names(targets, lv))
	}
	want := "A|B|C|D"
	if strings.Join(got, "|") != want {
		t.Errorf("分层应为 %s,实际 %s", want, strings.Join(got, "|"))
	}
}

// 环:A 与 B 互相依赖,C 依赖 A。A、B 剥不下来,C 跟着剥不下来 —— 三个都进最后一层,
// 而且要报出来:人要知道这些是靠多轮硬碰的,不是分层能解决的。
func TestLevelize_CycleGoesToTheLastLevelAndIsReported(t *testing.T) {
	targets := []InvalidObject{obj("A", "package"), obj("B", "package"), obj("C", "package"), obj("Z", "view")}
	edges := []DepEdge{{From: 0, To: 1}, {From: 1, To: 0}, {From: 2, To: 0}}
	levels, cyclic := levelize(len(targets), edges)
	if len(levels) != 2 {
		t.Fatalf("应分两层(Z 一层、环一层),实际 %d 层", len(levels))
	}
	if names(targets, levels[0]) != "Z" {
		t.Errorf("第一层应只有不依赖任何人的 Z,实际 %s", names(targets, levels[0]))
	}
	if names(targets, levels[1]) != "A,B,C" {
		t.Errorf("环及其依赖者应在最后一层、按清单顺序,实际 %s", names(targets, levels[1]))
	}
	if cyclic != 3 {
		t.Errorf("应报 3 个分不出层的对象,实际 %d", cyclic)
	}
}

// 没有边就是一层,顺序照清单 —— 依赖图读不到时退回的就是这条路。
func TestLevelize_NoEdgesIsOneLevelInListOrder(t *testing.T) {
	levels, cyclic := levelize(3, nil)
	if len(levels) != 1 || cyclic != 0 || len(levels[0]) != 3 {
		t.Fatalf("应为一层三个对象,实际 %v cyclic=%d", levels, cyclic)
	}
	for i, v := range levels[0] {
		if v != i {
			t.Errorf("同一层内应保持清单顺序,实际 %v", levels[0])
		}
	}
}

// 分层之后一条四层链**一轮**编完;没有分层要四轮,而封顶是三轮 —— 最上面那个会被留下。
// 这就是分层存在的全部理由,所以两种情形并排钉住。
func TestRecompile_LevelsFinishAChainInOnePass(t *testing.T) {
	compiler := func() func(InvalidObject) (*CompileReport, error) {
		valid := map[string]bool{}
		dep := map[string]string{"D": "C", "C": "B", "B": "A"}
		return func(o InvalidObject) (*CompileReport, error) {
			if d, ok := dep[o.Name]; ok && !valid[d] {
				return badReport(), nil
			}
			valid[o.Name] = true
			return okReport(), nil
		}
	}

	targets, edges := chain4()
	with := recompileWith(targets, edges, compiler(), nil)
	if with.Passes != 1 || with.Fixed != 4 {
		t.Errorf("分层后应一轮修好 4 个,实际 passes=%d fixed=%d failed=%d", with.Passes, with.Fixed, with.Failed)
	}
	if with.Levels != 4 || with.Cyclic != 0 {
		t.Errorf("报告应说明分了 4 层、无环,实际 levels=%d cyclic=%d", with.Levels, with.Cyclic)
	}
	for _, it := range with.Items {
		wantLevel := map[string]int{"A": 1, "B": 2, "C": 3, "D": 4}[it.Name]
		if it.Level != wantLevel {
			t.Errorf("%s 应在第 %d 层,实际 %d", it.Name, wantLevel, it.Level)
		}
	}

	without := recompileWith(targets, nil, compiler(), nil)
	if without.Passes != maxRecompilePasses || without.Failed != 1 {
		t.Errorf("不分层时四层链应跑满 %d 轮且剩 1 个,实际 passes=%d failed=%d —— 这正是分层要解决的",
			maxRecompilePasses, without.Passes, without.Failed)
	}
}

// 环里的对象靠多轮:A、B 互相依赖时,先编谁都会失败一次,第二轮才过。分层管不了它,
// 但多轮兜底必须还在 —— 分层不能把多轮这条路挤掉。
func TestRecompile_CycleStillFallsBackToPasses(t *testing.T) {
	targets := []InvalidObject{obj("A", "package"), obj("B", "package")}
	edges := []DepEdge{{From: 0, To: 1}, {From: 1, To: 0}}
	tried := map[string]bool{}
	rep := recompileWith(targets, edges, func(o InvalidObject) (*CompileReport, error) {
		// 第一次编谁都不过(对方还是 INVALID);对方试过一次之后就过了 —— 模拟 Oracle
		// 里"规范已经能解析、只差包体"的常态。
		other := "B"
		if o.Name == "B" {
			other = "A"
		}
		tried[o.Name] = true
		if tried[other] {
			return okReport(), nil
		}
		return badReport(), nil
	}, nil)
	if rep.Fixed != 2 {
		t.Errorf("环里的两个应靠第二轮修好,实际 fixed=%d failed=%d passes=%d", rep.Fixed, rep.Failed, rep.Passes)
	}
	if rep.Cyclic != 2 {
		t.Errorf("报告应指出 2 个对象处于环里,实际 %d", rep.Cyclic)
	}
	for _, it := range rep.Items {
		if !it.Cyclic {
			t.Errorf("%s 应标为环内对象", it.Name)
		}
	}
}

// all_dependencies 的行 → 这批对象内部的边。三条要钉住:
//   - 包体依赖包规范,在这里是同一个对象,折掉;
//   - 依赖了不在这批里的对象(别的 schema、本来就 VALID),忽略;
//   - 同一对对象之间的多条行(包体和包规范各引用一次)只记一条边。
func TestEdgesAmong_CollapsesUnitsAndIgnoresOutsiders(t *testing.T) {
	targets := []InvalidObject{obj("PKG_A", "package"), obj("V_ORDERS", "view"), obj("TRG_X", "trigger")}
	raw := []depRow{
		{"PKG_A", "PACKAGE BODY", "PKG_A", "PACKAGE"},        // 自己的两个单元 → 折掉
		{"PKG_A", "PACKAGE BODY", "V_ORDERS", "VIEW"},        // 包体依赖视图
		{"PKG_A", "PACKAGE", "V_ORDERS", "VIEW"},             // 包规范也依赖同一个视图 → 同一条边
		{"V_ORDERS", "VIEW", "ORDERS", "TABLE"},              // 表不在清单里(TABLE 也不是可编译类型)
		{"TRG_X", "TRIGGER", "PKG_ELSEWHERE", "PACKAGE"},     // 不在这批里
		{"trg_x", "TRIGGER", "pkg_a", "PACKAGE"},             // 大小写不同也要认出来
		{"PKG_A", "PACKAGE BODY", "SOME_SYNONYM", "SYNONYM"}, // 认不出的类型
	}
	edges := edgesAmong(targets, raw)
	want := map[DepEdge]bool{{From: 0, To: 1}: true, {From: 2, To: 0}: true}
	if len(edges) != len(want) {
		t.Fatalf("应折成 %d 条边,实际 %d: %v", len(want), len(edges), edges)
	}
	for _, e := range edges {
		if !want[e] {
			t.Errorf("多出一条边 %v", e)
		}
	}
	levels, _ := levelize(len(targets), edges)
	if got := names(targets, levels[0]) + "|" + names(targets, levels[1]) + "|" + names(targets, levels[2]); got != "V_ORDERS|PKG_A|TRG_X" {
		t.Errorf("视图 → 包 → 触发器 应各占一层,实际 %s", got)
	}
}
