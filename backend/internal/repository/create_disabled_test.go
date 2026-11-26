package repository

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"aegisdb/internal/model"
	"aegisdb/internal/sqlmigrate"
	"aegisdb/migrations"
)

// 2026-11:带 GORM 标签 default:true 的布尔列,建行时传 false 会被 GORM 换成 true ——
// "新建一条停用的 X",存下来却是启用的。元数据缓存的 Nullable 因此每轮被删了重插;
// 发布流程、审查规则、Webhook 首次保存都能从界面 / 接口带着 enabled=false 建行。
//
// 每条建行入口都建一条停用的,读回来必须是停用的;再建一条启用的,读回来必须是启用的。
// 存储库覆盖 PostgreSQL(生产迁移建表),以及设了 AEGISDB_TEST_STORE_MYSQL 时的 MySQL。

func disabledStores(t *testing.T) map[string]*Repo {
	t.Helper()
	repos := map[string]*Repo{"pg": New(openStore(t))}
	if base := os.Getenv("AEGISDB_TEST_STORE_MYSQL"); base != "" {
		name := fmt.Sprintf("vt_disabled_%d", time.Now().UnixNano()%1_000_000)
		admin, err := sql.Open("mysql", base)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { admin.Exec("DROP DATABASE IF EXISTS " + name); admin.Close() })
		db, err := gorm.Open(mysql.Open(base+name+"?charset=utf8mb4&parseTime=true&loc=Local"),
			&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		if s, e := db.DB(); e == nil {
			t.Cleanup(func() { s.Close() })
		}
		if err := sqlmigrate.Run(db, migrations.FS); err != nil {
			t.Fatalf("mysql: %v", err)
		}
		repos["mysql"] = New(db)
	}
	return repos
}

func TestCreate_DisabledStaysDisabled(t *testing.T) {
	for store, r := range disabledStores(t) {
		t.Run(store, func(t *testing.T) {
			for _, want := range []bool{false, true} {
				tag := fmt.Sprintf("%v", want)
				check := func(what string, got bool) {
					t.Helper()
					if got != want {
						t.Errorf("%s 建成 enabled=%v,读回来是 %v", what, want, got)
					}
				}

				w := &model.ExecWindow{Name: "w" + tag, Enabled: want, ConnectionID: 1, Database: "db1",
					Kind: "once", Timezone: "Asia/Shanghai", Status: model.WindowPending}
				if err := r.CreateExecWindow(w); err != nil {
					t.Fatal(err)
				}
				check("班车窗口(返回值)", w.Enabled)
				gw, err := r.GetExecWindow(w.ID)
				if err != nil {
					t.Fatal(err)
				}
				check("班车窗口", gw.Enabled)

				p := &model.Pipeline{Name: "p" + tag, Enabled: want}
				if err := r.SavePipeline(p, nil); err != nil {
					t.Fatal(err)
				}
				check("发布流程(返回值)", p.Enabled)
				gp, err := r.GetPipeline(p.ID)
				if err != nil {
					t.Fatal(err)
				}
				check("发布流程", gp.Enabled)

				rule := &model.SQLReviewRule{Code: "c_" + tag, Name: "n", Kind: model.ReviewKindRegex, Enabled: want}
				if err := r.CreateSQLReviewRule(rule); err != nil {
					t.Fatal(err)
				}
				check("审查规则(返回值)", rule.Enabled)
				var gr model.SQLReviewRule
				if err := r.db.First(&gr, rule.ID).Error; err != nil {
					t.Fatal(err)
				}
				check("审查规则", gr.Enabled)

				sc := &model.SensitiveColumn{Tbl: "t", ColumnName: "c_" + tag, MaskStyle: "partial", Enabled: want}
				if err := r.CreateSensitiveColumn(sc); err != nil {
					t.Fatal(err)
				}
				gs, err := r.GetSensitiveColumn(sc.ID)
				if err != nil {
					t.Fatal(err)
				}
				check("敏感字段", gs.Enabled)

				cl := &model.APIClient{Name: "a" + tag, Key: "k_" + tag, SecretHash: "h", Enabled: want}
				if err := r.CreateAPIClient(cl); err != nil {
					t.Fatal(err)
				}
				var gc model.APIClient
				if err := r.db.First(&gc, cl.ID).Error; err != nil {
					t.Fatal(err)
				}
				check("开放接口凭据", gc.Enabled)

				// Webhook 只有一行:先清掉,模拟"还没有配置时第一次保存"。
				if err := r.db.Where("1 = 1").Delete(&model.WebhookConfig{}).Error; err != nil {
					t.Fatal(err)
				}
				wh := &model.WebhookConfig{Endpoint: "https://example.invalid/hook", Events: "create", Enabled: want}
				if err := r.SaveWebhook(wh); err != nil {
					t.Fatal(err)
				}
				gwh, err := r.GetWebhook()
				if err != nil {
					t.Fatal(err)
				}
				check("Webhook", gwh.Enabled)
			}
		})
	}
}
