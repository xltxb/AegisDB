package bootstrap

import (
	"testing"
	"time"
)

// 限流阈值的默认值要落在**安全**的那一边。
//
// 一份 0002 时代写下的 config.yaml 里没有 osc 这一段。把"没写"解释成 0(不限流),
// 意味着升级上来的部署在打开开关之后跑的是没有限流的迁移 —— 而没人改过配置,
// 也没人会知道。见 ADR 0011。

func TestConfig_OSCMaxLagDefaultsToThirtySecondsWhenUnset(t *testing.T) {
	cfg, err := LoadConfig(writeTempConfig(t, `env: "dev"`))
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	if got := cfg.OSCMaxLag(); got != 30*time.Second {
		t.Errorf("没写 osc.max_lag_seconds 时阈值是 %v,期望默认的 30s", got)
	}
}

func TestConfig_OSCMaxLagCanBeTurnedOffOnPurpose(t *testing.T) {
	// 关掉限流要**说出来**才算数。负数是那句话 —— 它与"没写"区分得开,
	// 而 0 分不开:0 既可能是有人要关,也可能是这一段根本不存在。
	cfg, err := LoadConfig(writeTempConfig(t, "env: \"dev\"\nosc:\n  max_lag_seconds: -1"))
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	if got := cfg.OSCMaxLag(); got != 0 {
		t.Errorf("显式写了 -1,阈值却是 %v,期望 0(不限流)", got)
	}
}

func TestConfig_OSCMaxLagTakesTheConfiguredSeconds(t *testing.T) {
	cfg, err := LoadConfig(writeTempConfig(t, "env: \"dev\"\nosc:\n  max_lag_seconds: 5"))
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	if got := cfg.OSCMaxLag(); got != 5*time.Second {
		t.Errorf("配置写的是 5 秒,实际 %v", got)
	}
}
