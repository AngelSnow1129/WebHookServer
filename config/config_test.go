package config

import (
	"testing"
	"time"
)

// TestLoadDefaults 验证在环境变量缺失时，默认值与 .env.example 声明的语义一致。
// 这是此前的缺陷点：TTL 变量名是“分钟”，实现却按秒解析，导致默认值为 5 秒。
func TestLoadDefaults(t *testing.T) {
	t.Setenv("SERVER_ADDR", "")
	t.Setenv("MYSQL_DSN", "")
	t.Setenv("HMAC_SECRET", "")
	t.Setenv("WEBHOOK_SECRET", "")
	t.Setenv("OTP_CACHE_TTL_MINUTES", "")
	t.Setenv("CLEANUP_INTERVAL_SECONDS", "")
	t.Setenv("OTP_TEMPLATES_JSON", "")
	t.Setenv("SMSFORWARD_CHANNELS_JSON", "")

	cfg := Load()

	if cfg.ServerAddr != ":53340" {
		t.Errorf("ServerAddr = %q，期望 \":53340\"", cfg.ServerAddr)
	}
	if cfg.OTPCacheTTL != 5*time.Minute {
		t.Errorf("OTPCacheTTL = %v，期望 5m0s", cfg.OTPCacheTTL)
	}
	if cfg.CleanupInterval != 30*time.Second {
		t.Errorf("CleanupInterval = %v，期望 30s", cfg.CleanupInterval)
	}
	if len(cfg.OTPTemplates) == 0 {
		t.Fatal("默认模板不能为空")
	}
	if len(cfg.SMSForwardChannels) != 0 {
		t.Fatalf("默认 smsforward 通道数量=%d，期望 0", len(cfg.SMSForwardChannels))
	}
}

func TestLoadValuesFromEnv(t *testing.T) {
	t.Setenv("SERVER_ADDR", ":8080")
	t.Setenv("HMAC_SECRET", "hmac-secret")
	t.Setenv("WEBHOOK_SECRET", "webhook-secret")
	t.Setenv("OTP_CACHE_TTL_MINUTES", "5")
	t.Setenv("CLEANUP_INTERVAL_SECONDS", "30")
	t.Setenv("OTP_TEMPLATES_JSON", `[{"id":"custom","keywords":["code"],"code_type":"alnum","min_length":6,"max_length":6}]`)
	t.Setenv("SMSFORWARD_CHANNELS_JSON", `[{"channel_id":"android-main","webhook_secret":"abc123","enabled":true,"provider":"smsforward","template_ids":["custom"],"source_whitelist":["pixel-8"]}]`)

	cfg := Load()

	if cfg.ServerAddr != ":8080" {
		t.Errorf("ServerAddr = %q，期望 \":8080\"", cfg.ServerAddr)
	}
	if cfg.HMACSecret != "hmac-secret" {
		t.Errorf("HMACSecret = %q", cfg.HMACSecret)
	}
	if cfg.WebhookSecret != "webhook-secret" {
		t.Errorf("WebhookSecret = %q", cfg.WebhookSecret)
	}
	// 显式设置为 .env.example 中的值，结果必须与默认值一致
	if cfg.OTPCacheTTL != 5*time.Minute {
		t.Errorf("OTPCacheTTL = %v，期望 5m0s", cfg.OTPCacheTTL)
	}
	if cfg.CleanupInterval != 30*time.Second {
		t.Errorf("CleanupInterval = %v，期望 30s", cfg.CleanupInterval)
	}
	if len(cfg.OTPTemplates) != 1 || cfg.OTPTemplates[0].ID != "custom" {
		t.Fatalf("模板加载失败: %+v", cfg.OTPTemplates)
	}
	ch, ok := cfg.SMSForwardChannels["android-main"]
	if !ok {
		t.Fatal("smsforward 通道 android-main 未加载")
	}
	if ch.WebhookSecret != "abc123" || !ch.Enabled || ch.Provider != "smsforward" {
		t.Fatalf("smsforward 通道配置异常: %+v", ch)
	}
}

func TestLoadOTPTemplatesFallbackToDefaultOnInvalidJSON(t *testing.T) {
	t.Setenv("OTP_TEMPLATES_JSON", `{bad json`)

	cfg := Load()
	if len(cfg.OTPTemplates) == 0 {
		t.Fatal("非法 JSON 时应回退默认模板")
	}
}

func TestLoadSMSForwardChannelsFallbackOnInvalidJSON(t *testing.T) {
	t.Setenv("SMSFORWARD_CHANNELS_JSON", `{bad json`)

	cfg := Load()
	if len(cfg.SMSForwardChannels) != 0 {
		t.Fatalf("非法 JSON 时应回退为空 map，实际=%d", len(cfg.SMSForwardChannels))
	}
}

func TestGetMinutesEnv(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		fallback int
		want     time.Duration
	}{
		{"正常分钟值", "5", 1, 5 * time.Minute},
		{"单分钟", "1", 99, time.Minute},
		{"未设置使用默认", "", 5, 5 * time.Minute},
		{"非法值回退默认", "abc", 5, 5 * time.Minute},
		{"浮点回退默认", "1.5", 5, 5 * time.Minute},
		{"零值", "0", 5, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TEST_MINUTES", tt.value)
			if got := getMinutesEnv("TEST_MINUTES", tt.fallback); got != tt.want {
				t.Errorf("getMinutesEnv(%q, %d) = %v，期望 %v", tt.value, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestGetSecondsEnv(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		fallback int
		want     time.Duration
	}{
		{"正常秒值", "30", 99, 30 * time.Second},
		{"单秒", "1", 99, time.Second},
		{"未设置使用默认", "", 30, 30 * time.Second},
		{"非法值回退默认", "xyz", 30, 30 * time.Second},
		{"负值", "-5", 30, -5 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TEST_SECONDS", tt.value)
			if got := getSecondsEnv("TEST_SECONDS", tt.fallback); got != tt.want {
				t.Errorf("getSecondsEnv(%q, %d) = %v，期望 %v", tt.value, tt.fallback, got, tt.want)
			}
		})
	}
}

// TestGetMinutesEnvUnitIsMinutes 是防止单位缺陷回归的关键用例：
// 配置值 5 必须得到 5 分钟，而不是 5 秒。
func TestGetMinutesEnvUnitIsMinutes(t *testing.T) {
	t.Setenv("TEST_MINUTES", "5")

	got := getMinutesEnv("TEST_MINUTES", 5)
	if got == 5*time.Second {
		t.Fatal("回归：TTL 配置被当作秒解析（5 得到 5s），应为 5m")
	}
	if got != 5*time.Minute {
		t.Fatalf("getMinutesEnv = %v，期望 5m0s", got)
	}
}

func TestGetEnvFallback(t *testing.T) {
	t.Setenv("TEST_STR", "")
	if got := getEnv("TEST_STR", "fallback"); got != "fallback" {
		t.Errorf("空值应回退，实际 %q", got)
	}

	t.Setenv("TEST_STR", "value")
	if got := getEnv("TEST_STR", "fallback"); got != "value" {
		t.Errorf("getEnv = %q，期望 \"value\"", got)
	}
}
