package bootstrap

import (
	"testing"

	"gorm.io/gorm"

	"velagateway/internal/model"
	"velagateway/internal/testsupport"
)

// 历史行可能带着非规范大小写的邮箱:Invite 从前既不折叠也不查重(那条路已经修了),
// 而 UpsertAdmin 命中旧行后走的是 Save,把原样的 email 写回去 —— 所以一旦库里有
// `Ops@Vela.io` 这样一行,它会一直保持那个形态。
//
// 功能上不影响(查询两侧都折叠、唯一索引建在 lower(email) 上),但它是一颗留着的雷:
// 哪天有人写了一段**不折叠**的新查询,它会查不到这些行 —— 那正是 UpsertAdmin 自己
// 踩过的坑(见 email_case_test.go)。回填一次,把这个形态差异从库里清掉。
func TestBackfillLowerEmail_NormalisesHistoricalRows(t *testing.T) {
	db := testsupport.NewDB(t)

	for _, e := range []string{"Ops@Vela.io", "MIXED@Case.IO", "already@lower.io"} {
		u := &model.User{Name: "x", Email: e, RoleID: 1, Status: "active"}
		if err := db.Create(u).Error; err != nil {
			t.Fatalf("seed %q: %v", e, err)
		}
	}

	if err := backfillLowerEmail(db); err != nil {
		t.Fatalf("backfillLowerEmail: %v", err)
	}

	var got []string
	if err := db.Model(&model.User{}).Order("id asc").Pluck("email", &got).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	want := []string{"ops@vela.io", "mixed@case.io", "already@lower.io"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 行 email = %q, 想要 %q", i+1, got[i], want[i])
		}
	}
}

// 跑第二次一行都不该动。这条回填挂在 Migrate 上,而 serve / migrate / init 三个
// 入口每次启动都经过它 —— 不幂等的话,每次启动都在写库。
func TestBackfillLowerEmail_SecondRunTouchesNothing(t *testing.T) {
	db := testsupport.NewDB(t)

	u := &model.User{Name: "x", Email: "Ops@Vela.io", RoleID: 1, Status: "active"}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := backfillLowerEmail(db); err != nil {
		t.Fatalf("第一次: %v", err)
	}

	// 第二次:用 RowsAffected 直接问「改了几行」,而不是比对内容 —— 内容相同也
	// 可能是「又写了一遍同样的值」,那仍然是每次启动一次无谓的写。
	res := db.Model(&model.User{}).Where("email <> lower(email)").Update("email", gorm.Expr("lower(email)"))
	if res.Error != nil {
		t.Fatalf("第二次: %v", res.Error)
	}
	if res.RowsAffected != 0 {
		t.Errorf("第二次跑改了 %d 行,应当一行都不动", res.RowsAffected)
	}
}

// 这条回填必须真的挂在 Migrate 上 —— 它是一次性数据修正,不进 baseline(那里只有
// schema),所以唯一的执行时机就是 Migrate。漏挂的话上面两条用例照样全绿,而真实
// 的库一行都不会被修。
func TestMigrate_RunsTheLowerEmailBackfill(t *testing.T) {
	db := testsupport.NewDB(t)

	u := &model.User{Name: "x", Email: "Ops@Vela.io", RoleID: 1, Status: "active"}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg := &Config{}
	cfg.Gateway.StrictMode = true
	if err := Migrate(cfg, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var got string
	if err := db.Model(&model.User{}).Where("id = ?", u.ID).Pluck("email", &got).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != "ops@vela.io" {
		t.Errorf("Migrate 之后 email = %q, 想要 %q —— 回填没有挂在 Migrate 上", got, "ops@vela.io")
	}
}
