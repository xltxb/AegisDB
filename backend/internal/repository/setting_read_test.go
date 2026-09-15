package repository

// 设置项的读法,三处各抄了一份。
//
// middleware、webhook 的 Dispatcher、service 的 Services 各有一对 settingString /
// settingBool,逐字相同。它们共用的不只是"解一段 JSON"这件显然的事,还有一条**不显然**
// 的兼容规则:解不出 JSON 时,把首尾的引号裁掉当字符串用 —— 那是历史上没做 JSON 编码
// 就直接存进去的值。
//
// 这种兼容规则正是抄三遍会出事的那一类:有人在自己那一份里把它去掉(看着像多余的),
// 另外两份还留着,于是同一个 key 在两个地方读出两个值,而两边都不报错。

import (
	"testing"

	"velagateway/internal/model"
	"velagateway/internal/testsupport"
)

func newSettingDB(t *testing.T) *Repo {
	t.Helper()
	db := testsupport.NewDB(t)
	return New(db)
}

func put(t *testing.T, r *Repo, k, v string) {
	t.Helper()
	if err := r.db.Create(&model.Setting{K: k, V: v}).Error; err != nil {
		t.Fatalf("put %s: %v", k, err)
	}
}

func TestSettingString(t *testing.T) {
	r := newSettingDB(t)
	put(t, r, "json", `"你好"`)
	put(t, r, "legacy", `裸着存的`) // 历史值:没做 JSON 编码
	put(t, r, "quoted", `"半截`)  // 解不出 JSON,只能裁引号
	put(t, r, "empty", ``)

	for _, tc := range []struct{ key, def, want string }{
		{"json", "兜底", "你好"},
		{"legacy", "兜底", "裸着存的"},
		{"quoted", "兜底", "半截"},
		{"empty", "兜底", "兜底"}, // 空值等于没配
		{"缺席", "兜底", "兜底"},    // 没有这一行
	} {
		if got := r.SettingString(tc.key, tc.def); got != tc.want {
			t.Errorf("SettingString(%q) = %q,want %q", tc.key, got, tc.want)
		}
	}
}

func TestSettingBool(t *testing.T) {
	r := newSettingDB(t)
	put(t, r, "yes", `true`)
	put(t, r, "no", `false`)
	put(t, r, "junk", `是`)
	put(t, r, "empty", ``)

	for _, tc := range []struct {
		key  string
		def  bool
		want bool
	}{
		{"yes", false, true},
		{"no", true, false},
		{"junk", true, true}, // 读不懂就用兜底,不自作主张
		{"junk2", false, false},
		{"empty", true, true},
		{"缺席", true, true},
	} {
		if got := r.SettingBool(tc.key, tc.def); got != tc.want {
			t.Errorf("SettingBool(%q, %v) = %v,want %v", tc.key, tc.def, got, tc.want)
		}
	}
}

func TestSettingInt(t *testing.T) {
	r := newSettingDB(t)
	put(t, r, "n", `42`)
	put(t, r, "junk", `很多`)
	put(t, r, "empty", ``)

	for _, tc := range []struct {
		key       string
		def, want int
	}{
		{"n", 7, 42},
		{"junk", 7, 7},
		{"empty", 7, 7},
		{"缺席", 7, 7},
	} {
		if got := r.SettingInt(tc.key, tc.def); got != tc.want {
			t.Errorf("SettingInt(%q, %d) = %d,want %d", tc.key, tc.def, got, tc.want)
		}
	}
}
