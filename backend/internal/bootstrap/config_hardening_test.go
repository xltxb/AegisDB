package bootstrap

import (
	"testing"

	"velagateway/internal/repository"
	"velagateway/internal/testsupport"
)

// C5: a prod JWT secret that is long enough and not blacklisted can still be
// trivially weak (e.g. a repeated character). Require a minimum character variety
// so an obviously low-entropy secret is refused.
func TestValidateForServe_RejectsLowEntropySecret(t *testing.T) {
	mk := func(sec string) *Config {
		c := &Config{}
		c.Env = "prod"
		c.JWT.Secret = sec
		// prod 还要求 DSN 非空(见下一条用例),这里给一个,好让这条用例只测密钥。
		c.Database.PostgresDSN = "host=127.0.0.1 port=5432 dbname=vela_gateway sslmode=disable"
		return c
	}

	// 34 identical chars: passes length + blacklist, but near-zero entropy.
	if err := mk("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").ValidateForServe(); err == nil {
		t.Error("expected a repeated-character secret to be rejected")
	}
	// "changeme" x4 = 32 chars, only 7 distinct — still weak.
	if err := mk("changemechangemechangemechangeme").ValidateForServe(); err == nil {
		t.Error("expected a low-variety secret to be rejected")
	}
	// a realistic openssl rand -hex 32 style secret passes.
	if err := mk("9f2c1a7e4b0d63859a1e2f7c4d8b6031aa55cc77ee99bb00").ValidateForServe(); err != nil {
		t.Errorf("expected a high-entropy secret to pass, got %v", err)
	}
}

// C6: an unrecognized environment (e.g. "staging") silently degrades to dev —
// which turns the demo seed on and drops the prod JWT-strength check.
// isKnownEnv lets LoadConfig warn instead of failing silently.
func TestIsKnownEnv(t *testing.T) {
	known := []string{"prod", "production", "dev", "development", "local", ""}
	for _, s := range known {
		if !isKnownEnv(s) {
			t.Errorf("isKnownEnv(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"staging", "stage", "qa", "uat", "bogus"} {
		if isKnownEnv(s) {
			t.Errorf("isKnownEnv(%q) = true, want false", s)
		}
	}
}

// ED2: config.yaml ships with seed: true and its header invites operators to run
// `APP_ENV=prod ./server` with it. Nothing gated the SEED, so an empty production
// database would be populated with the demo platform administrator — whose
// password is printed in the README. Production accounts come from
// `server init`, never from the demo seed.
func TestSeed_RefusesToPlantDemoDataInProduction(t *testing.T) {
	cfg := &Config{}
	cfg.Env = "prod"
	cfg.Database.Seed = true

	db := testsupport.NewDB(t)
	repo := repository.New(db)

	if err := Seed(repo, cfg); err == nil {
		t.Error("seeding a production database silently succeeded")
	}
	if _, err := repo.GetUserByEmail("linwei@vela.io"); err == nil {
		t.Error("the demo administrator was planted in a production database")
	}
}

// 生产漏填 postgres_dsn 不该是一次「静默连到别处」。
//
// 空 DSN 在 libpq 眼里不是错误 —— 它读成「连本机默认库,用 OS 用户的名字当库名」,
// 而且通常连得上。于是漏填的后果不是启动失败,是网关在一个不相干的库上建起 36 张表、
// 开始写审计、存加密后的连接口令,健康检查全绿,直到有人去真正的生产库里找这些数据。
//
// config.prod.yaml 里 postgres_dsn 是**故意留空**的(凭据不进代码库),所以这条守卫
// 拦的正是「照着模板起服务、忘了设 VELA_PG_DSN」这条最常见的路径。
func TestValidateForServe_RejectsEmptyProdDSN(t *testing.T) {
	strongSecret := "9f2c1a7e4b0d63859a1e2f7c4d8b6031aa55cc77ee99bb00"
	mk := func(env, dsn string) *Config {
		c := &Config{}
		c.Env = env
		c.JWT.Secret = strongSecret
		c.Database.PostgresDSN = dsn
		return c
	}

	if err := mk("prod", "").ValidateForServe(); err == nil {
		t.Error("生产环境空 postgres_dsn 被放行了 —— 网关会连上本机默认库并在上面建表")
	}
	// 只有空白也一样:yaml 里 `postgres_dsn: " "` 同样是没配。
	if err := mk("prod", "   ").ValidateForServe(); err == nil {
		t.Error("生产环境全空白的 postgres_dsn 被放行了")
	}
	// 配了就放行。
	if err := mk("prod", "host=db.internal port=5432 user=vela dbname=vela_gateway sslmode=require").ValidateForServe(); err != nil {
		t.Errorf("配好 DSN 的生产配置被拒:%v", err)
	}
	// dev 不受这条约束:本机随手跑一下不该被它挡住。
	if err := mk("dev", "").ValidateForServe(); err != nil {
		t.Errorf("dev 环境不该要求 postgres_dsn:%v", err)
	}
}
