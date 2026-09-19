package bootstrap

// 一张发布单从提交走到完成,中间那条索引变更**真的**由 OSC 做掉。
//
// 前面每一层都能单独绿:判定(oscroute.Decide)是纯函数,状态机(pipeline_osc.go)
// 用假执行器,回调(OnOSCJobFinished)用假任务。但"一次发布单真的靠一次真实的迁移
// 完成了"只有把它们串起来才验得到 —— 而这条链路上任何一个接头松了,症状都是"单子
// 永远停在 waiting":没有报错,没有失败,只是不动了。
//
// 要真 MySQL(VELA_OSC_MYSQL_DSN),连不上是失败不是 skip(ADR 0011)。

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"

	"velagateway/internal/model"
	"velagateway/internal/osc"
	"velagateway/pkg/crypto"
	"velagateway/pkg/resp"
)

// oscMySQLDSN 与 internal/osc/gather_test.go 的 mysqlDSN 是同一个约定,但那边的
// 常量是包内私有的,这里(bootstrap 包)拿不到,所以照抄一份而不是导出它 ——
// 导出一个只为了给一个测试文件复用的常量,不值得放大 osc 包的公共面。
func oscMySQLDSN() string {
	if v := os.Getenv("VELA_OSC_MYSQL_DSN"); v != "" {
		return v
	}
	return "vela:velapass@tcp(127.0.0.1:3306)/osc_test?parseTime=true&multiStatements=true"
}

// openOSCMySQL 打开一条到真 MySQL 的连接,连不上直接 Fatal —— 与 osc 包的规矩一致,
// 静默跳过会让一个谁也没跑过的用例看起来是绿的。
func openOSCMySQL(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", oscMySQLDSN())
	if err != nil {
		t.Fatalf("打不开 MySQL 连接: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("连不上 MySQL(DSN=%s): %v\n"+
			"这条用例必须对着真 MySQL 跑,见 ADR 0011。本机装一个或用 docker-compose 里的 mysql-target。",
			oscMySQLDSN(), err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedMySQLTarget 往 tbl_connection 插一台指向 VELA_OSC_MYSQL_DSN 的实例。
//
// engine 必须精确是 "mysql"(不是 "MySQL 8.0" 这类展示用的标签):oscroute.Decide
// 用 strings.EqualFold(engine, "mysql") 精确匹配,写成别的字符串会让路由判定在
// "非 MySQL,直发" 那一支上直接短路,整条链路测的就不是这条路了。
//
// Env 选 dev、Policy 选 audit-only:产品种子数据里 ALTER 在 dev 分层的风险字典是
// "off"(见 seed.go 的 risk command dictionary),即"允许,不必审批"。这条用例要
// 验的是"执行闸 → OSC 路由 → 回调"这条接缝,不是审批流程本身 —— 选一个不会绊在
// 审批上的分层,让发布单能一路跑到执行闸。
//
// 密码必须是**加密**存的(crypto.EncryptSecret):RealExecSupported/engineDriver
// 读 conn.Password 时会 crypto.DecryptSecret 一次,存明文的话解密直接失败,
// Executor.Run 就会判定"凭据不完整"转去模拟执行 —— 而模拟执行不会真的建索引。
func seedMySQLTarget(t *testing.T, app *testApp) (connID int64, targetDB string) {
	t.Helper()
	cfg, err := mysqldrv.ParseDSN(oscMySQLDSN())
	if err != nil {
		t.Fatalf("解析 VELA_OSC_MYSQL_DSN 失败: %v", err)
	}
	host, portStr, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		t.Fatalf("解析地址 %q 失败: %v", cfg.Addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("解析端口 %q 失败: %v", portStr, err)
	}
	encPw, err := crypto.EncryptSecret(cfg.Passwd)
	if err != nil {
		t.Fatalf("加密密码失败: %v", err)
	}

	conn := &model.Connection{
		Name:     fmt.Sprintf("osc-e2e-%d", time.Now().UnixNano()),
		Engine:   "mysql",
		Host:     host,
		Port:     port,
		Env:      model.EnvDev,
		Policy:   "audit-only",
		Username: cfg.User,
		Password: encPw,
		Database: cfg.DBName,
		Status:   model.ConnOnline,
	}
	if err := app.repo.DB().Create(conn).Error; err != nil {
		t.Fatalf("插入 tbl_connection 失败: %v", err)
	}
	return conn.ID, cfg.DBName
}

// seedBigTable 在目标实例上建一张表并灌 n 行,返回表名。
//
// t.Cleanup 里把它和它可能留下的 _gho(影子表,中途失败会留下)/_del(切换成功后
// 的原表)/_ghc(心跳表)一并 drop 掉 —— 抄的是 internal/osc 包测试文件里的写法
// (见 gather_test.go 的 makeTable、migrate_e2e_test.go 的清理)。
func seedBigTable(t *testing.T, app *testApp, targetDB string, n int) string {
	t.Helper()
	_ = app // 保留参数只是为了和其余 seed* 辅助函数的签名一致;连接信息与 app 无关
	db := openOSCMySQL(t)
	ctx := context.Background()

	name := fmt.Sprintf("e2e_big_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS `"+name+"`"); err != nil {
		t.Fatalf("清理旧表失败: %v", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE `%s` (id BIGINT PRIMARY KEY AUTO_INCREMENT, memo VARCHAR(64))", name)); err != nil {
		t.Fatalf("建表失败: %v", err)
	}

	// 一批多行插入,不逐行 Exec —— n 有几千行时逐条插入会把这条用例拖得很慢,
	// 而这里要的只是"表里有足够多的行",不是插入本身的性能。
	const batch = 500
	for i := 0; i < n; i += batch {
		end := i + batch
		if end > n {
			end = n
		}
		var b strings.Builder
		b.WriteString("INSERT INTO `" + name + "` (memo) VALUES ")
		for j := i; j < end; j++ {
			if j > i {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "('memo-%d')", j)
		}
		if _, err := db.ExecContext(ctx, b.String()); err != nil {
			t.Fatalf("灌数据失败(第 %d 行起): %v", i, err)
		}
	}

	// gatherRows(经 osc.Gather → gatherTable)读的是 information_schema.TABLES
	// 的 TABLE_ROWS 估算值,而它只在 ANALYZE(或达到 InnoDB 内部的变更比例)之后
	// 才刷新。批量插入之后不主动 ANALYZE 的话,这个估算值可能仍然是 0 或旧值,
	// 于是路由判定会把这张刚灌满的大表当成小表,直接直发 —— 而不是走 OSC,
	// 这条用例的核心断言就会落空。
	//
	// ANALYZE TABLE 是同步阻塞语句:执行完,统计值立即可见,不依赖任何后台刷新
	// 窗口或延迟——这一步让用例在不同机器上是确定性的,不是在赌时机。
	if _, err := db.ExecContext(ctx, "ANALYZE TABLE `"+name+"`"); err != nil {
		t.Fatalf("ANALYZE 失败: %v", err)
	}

	t.Cleanup(func() {
		cctx := context.Background()
		for _, x := range []string{
			name,
			osc.ShadowName(name),
			"_" + name + osc.DelSuffix,
			osc.HeartbeatName(name),
		} {
			_, _ = db.ExecContext(cctx, "DROP TABLE IF EXISTS `"+x+"`")
		}
	})
	return name
}

// indexExists 对着真实例查 information_schema.STATISTICS,判断一张表上有没有
// 给定名字的索引 —— 这是"索引真的加上了"这条断言唯一站得住脚的证据来源:发布单
// 报告成功不代表数据库里真的有这个索引,尤其是在链路某处悄悄回落到了模拟执行的
// 时候。
func indexExists(t *testing.T, schema, table, index string) bool {
	t.Helper()
	db := openOSCMySQL(t)
	var n int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM information_schema.STATISTICS "+
			"WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND INDEX_NAME = ?",
		schema, table, index,
	).Scan(&n)
	if err != nil {
		t.Fatalf("查询 information_schema.STATISTICS 失败: %v", err)
	}
	return n > 0
}

// waitForRelease 轮询发布单直到它落到终态(success/failed/aborted)或者超时。
//
// 直接读 repo 而不是走 HTTP:GetRelease 需要带 token 的鉴权,而这里只是在等一个
// 后台 goroutine(driveRelease)把状态写进库 —— 断言的是数据库里的事实,不是接口
// 的可达性,没有必要为此多绕一层 HTTP。
func waitForRelease(t *testing.T, app *testApp, id int64, timeout time.Duration) *model.Release {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var rel model.Release
		if err := app.repo.DB().First(&rel, id).Error; err != nil {
			t.Fatalf("读取发布单 %d 失败: %v", id, err)
		}
		switch rel.Status {
		case model.RunSuccess, model.RunFailed, model.RunAborted:
			return &rel
		}
		if time.Now().After(deadline) {
			t.Fatalf("发布单 %d 在 %s 内没有到达终态,停在 %q(err=%q)", id, timeout, rel.Status, rel.Error)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// executeStageOf 等到发布单停在执行闸(execute 阶段,状态 waiting)并返回那个阶段。
//
// 执行闸是无条件的(ADR 0010):审批回答"可不可以做",这里回答"现在做"，
// driveRelease 到这一步必停,由人点「继续」才会真的往下发。
func (a *testApp) executeStageOf(t *testing.T, releaseID int64) *model.ReleaseStage {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		stages, err := a.repo.StagesOfRelease(releaseID)
		if err != nil {
			t.Fatalf("读取发布单 %d 的阶段失败: %v", releaseID, err)
		}
		for i := range stages {
			if stages[i].Type == model.StageExecute && stages[i].Status == model.RunWaiting {
				return &stages[i]
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("发布单 %d 没有停在执行闸", releaseID)
		}
		time.Sleep(40 * time.Millisecond)
	}
}

// mustSetSetting 写一条平台设置,失败即 Fatal —— 这条用例依赖 osc.autoRoute.*
// 生效,设置写不进去的话后面的每一步都失去了意义。
func mustSetSetting(t *testing.T, app *testApp, key, value string) {
	t.Helper()
	if err := app.repo.SetSetting(key, value); err != nil {
		t.Fatalf("写入设置 %s=%s 失败: %v", key, value, err)
	}
}

func TestReleaseE2E_TheIndexChangeIsMadeByOSCAndTheReleaseCompletes(t *testing.T) {
	app := newTestApp(t)
	app.cfg.OSC.Enabled = true
	mustSetSetting(t, app, "osc.autoRoute.enabled", "true")
	mustSetSetting(t, app, "osc.autoRoute.minRows", "100") // 夹具表只有几千行

	connID, targetDB := seedMySQLTarget(t, app)
	table := seedBigTable(t, app, targetDB, 3000)

	token := app.login("linwei@vela.io", "vela123")

	// 自建一条只有「执行变更」一个阶段的流水线,不用产品默认的标准流程 —— 默认流程
	// 带一个无条件的人工审批阶段(seed 里的"标准发布流程"),会让这条用例还没到
	// 执行闸就先停在一张需要人去点的审批单上。这条用例要验的是"执行闸 → OSC 路由
	// → 回调"这条接缝,不是审批流程,所以直接跳过它 —— 与
	// TestExecuteStageWaitsForHumanConfirmation 的做法一致。
	pid := app.createPipeline(token, "e2e-osc-pipeline", "", []map[string]any{
		{"name": "执行变更", "type": "execute"},
	})

	r := app.submitRelease(token, map[string]any{
		"title": "e2e 加索引", "pipelineId": pid, "connectionId": connID, "database": targetDB,
		"sql": "ALTER TABLE " + table + " ADD INDEX idx_memo (memo)", "reason": "osc e2e 回归",
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
	// osc.Job 没有 ReleaseID 字段,单看 Where("table_name = ?", table) 这一条查询
	// 本身是可能命中历史任务行的——它的安全性靠的是这个文件之外的两条前提:
	//   1. newTestApp → testsupport.NewDB(t) 给每个用例分配全新的 Postgres schema,
	//      t.Cleanup 里 DROP SCHEMA ... CASCADE,tbl_osc_job 在这条用例开始时必然
	//      是空的,不存在别的用例留下的同名历史行;
	//   2. seedBigTable 的表名带纳秒时间戳,同一进程内的其他用例不会造出同名表。
	// 这两条一旦被打破(比如为了提速 CI 把测试库换成跨用例共享的),这条断言会
	// 在没有任何报错的情况下变得不可靠:一次真正的"路由没生效"可能被别的用例
	// 留下的同名任务行悄悄掩盖成通过。
	var jobs int64
	app.repo.DB().Model(&osc.Job{}).Where("table_name = ?", table).Count(&jobs)
	if jobs == 0 {
		t.Error("没有任何 OSC 任务 —— 这一条其实是直发的,路由没有生效")
	}
}
