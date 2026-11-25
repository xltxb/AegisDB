package gateway

import (
	"reflect"
	"testing"
)

func objNames(objs []InvalidObject) []string {
	out := []string{}
	for _, o := range objs {
		out = append(out, o.Name)
	}
	return out
}

// 连带编译的范围:直接或间接依赖被编对象的那些,不多不少。
//
// 这就是 UAT_G13 那次的形状:PK_AGENT 编好之后,PK_AGENT_WALLET 与 PK_CUS 直接依赖它,
// PK_REPORT 经由 PK_CUS 间接依赖它;PK_LCOG 与它无关,不该被顺手编掉。
func TestDependentsOf_DirectAndTransitiveOnly(t *testing.T) {
	set := []InvalidObject{
		{Name: "PK_AGENT", Kind: "package"},        // 0 root
		{Name: "PK_AGENT_WALLET", Kind: "package"}, // 1 → 0
		{Name: "PK_CUS", Kind: "package"},          // 2 → 0
		{Name: "PK_LCOG", Kind: "package"},         // 3 无关
		{Name: "PK_REPORT", Kind: "package"},       // 4 → 2
		{Name: "V_LCOG", Kind: "view"},             // 5 → 3,也无关
	}
	edges := []DepEdge{{1, 0}, {2, 0}, {4, 2}, {5, 3}}
	got := objNames(dependentsOf(set, edges))
	want := []string{"PK_AGENT_WALLET", "PK_CUS", "PK_REPORT"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dependents = %v, want %v", got, want)
	}
}

// 两个依赖方互相引用(包与包之间的环)不能让遍历打转,也不能把谁漏掉。
func TestDependentsOf_CycleAmongDependents(t *testing.T) {
	set := []InvalidObject{
		{Name: "ROOT", Kind: "package"},
		{Name: "A", Kind: "package"},
		{Name: "B", Kind: "package"},
	}
	edges := []DepEdge{{1, 0}, {1, 2}, {2, 1}}
	got := objNames(dependentsOf(set, edges))
	want := []string{"A", "B"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dependents = %v, want %v", got, want)
	}
}

// 清点时被编对象自己可能又出现在清单里(同名同类型),它不是自己的依赖方。
// 同名但类型不同的(过程 ROOT 与包 ROOT)是另一个对象,照常算。
func TestDependentsOf_RootListedAgainIsNotItsOwnDependent(t *testing.T) {
	set := []InvalidObject{
		{Name: "ROOT", Kind: "package"},
		{Name: "root", Kind: "package"},
		{Name: "ROOT", Kind: "procedure"},
	}
	edges := []DepEdge{{1, 0}, {2, 0}}
	got := objNames(dependentsOf(set, edges))
	want := []string{"ROOT"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dependents = %v, want %v", got, want)
	}
	if k := dependentsOf(set, edges)[0].Kind; k != "procedure" {
		t.Fatalf("留下的应是过程 ROOT,得到 %s", k)
	}
}

func TestDependentsOf_NoEdgesNoDependents(t *testing.T) {
	set := []InvalidObject{{Name: "ROOT", Kind: "package"}, {Name: "X", Kind: "view"}}
	if got := dependentsOf(set, nil); len(got) != 0 {
		t.Fatalf("没有依赖边时不该连带任何对象,得到 %v", objNames(got))
	}
}
