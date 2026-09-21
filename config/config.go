package config

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"smsserver/model"
)

// Config 应用配置结构体
type Config struct {
	ServerAddr         string        // 服务监听地址
	MySQLDSN           string        // MySQL连接字符串
	HMACSecret         string        // 手机号HMAC密钥
	WebhookSecret      string        // Webhook鉴权密钥
	OTPCacheTTL        time.Duration // 验证码缓存过期时间
	CleanupInterval    time.Duration // 后台清理间隔
	OTPTemplates       []model.OTPTemplate
	SMSForwardChannels map[string]model.SMSForwardChannel
}

// Load 从环境变量加载配置
func Load() *Config {
	return &Config{
		ServerAddr:         getEnv("SERVER_ADDR", ":53340"),
		MySQLDSN:           getEnv("MYSQL_DSN", "user:password@tcp(127.0.0.1:3306)/smsdb?parseTime=true&loc=Local"),
		HMACSecret:         getEnv("HMAC_SECRET", ""),
		WebhookSecret:      getEnv("WEBHOOK_SECRET", ""),
		OTPCacheTTL:        getMinutesEnv("OTP_CACHE_TTL_MINUTES", 5),
		CleanupInterval:    getSecondsEnv("CLEANUP_INTERVAL_SECONDS", 30),
		OTPTemplates:       loadOTPTemplates(),
		SMSForwardChannels: loadSMSForwardChannels(),
	}
}

func loadOTPTemplates() []model.OTPTemplate {
	raw := strings.TrimSpace(os.Getenv("OTP_TEMPLATES_JSON"))
	if raw == "" {
		return defaultOTPTemplates()
	}

	var templates []model.OTPTemplate
	if err := json.Unmarshal([]byte(raw), &templates); err != nil {
		return defaultOTPTemplates()
	}
	if len(templates) == 0 {
		return defaultOTPTemplates()
	}
	return templates
}

func defaultOTPTemplates() []model.OTPTemplate {
	return []model.OTPTemplate{
		{
			ID:        "cn_numeric",
			Keywords:  []string{"验证码", "动态码", "校验码"},
			CodeType:  "numeric",
			MinLength: 4,
			MaxLength: 8,
		},
		{
			ID:        "en_numeric",
			Keywords:  []string{"verification code", "code", "otp", "passcode"},
			CodeType:  "numeric",
			MinLength: 4,
			MaxLength: 8,
		},
		{
			ID:        "en_alnum",
			Keywords:  []string{"verification code", "code", "otp", "passcode"},
			CodeType:  "alnum",
			MinLength: 4,
			MaxLength: 10,
		},
	}
}

func loadSMSForwardChannels() map[string]model.SMSForwardChannel {
	raw := strings.TrimSpace(os.Getenv("SMSFORWARD_CHANNELS_JSON"))
	if raw == "" {
		return map[string]model.SMSForwardChannel{}
	}

	var channels []model.SMSForwardChannel
	if err := json.Unmarshal([]byte(raw), &channels); err != nil {
		return map[string]model.SMSForwardChannel{}
	}

	result := make(map[string]model.SMSForwardChannel, len(channels))
	for _, ch := range channels {
		id := strings.TrimSpace(ch.ChannelID)
		if id == "" {
			continue
		}
		ch.ChannelID = id
		if ch.Provider == "" {
			ch.Provider = "smsforward"
		}
		result[id] = ch
	}
	return result
}

// getEnv 获取环境变量，不存在时返回默认值
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getMinutesEnv 按“分钟”语义读取配置，缺省或非法时使用 fallback 分钟
func getMinutesEnv(key string, fallbackMinutes int) time.Duration {
	if n, ok := getIntEnv(key); ok {
		return time.Duration(n) * time.Minute
	}
	return time.Duration(fallbackMinutes) * time.Minute
}

// getSecondsEnv 按“秒”语义读取配置，缺省或非法时使用 fallback 秒
func getSecondsEnv(key string, fallbackSeconds int) time.Duration {
	if n, ok := getIntEnv(key); ok {
		return time.Duration(n) * time.Second
	}
	return time.Duration(fallbackSeconds) * time.Second
}

// getIntEnv 读取整数环境变量，未设置或非法时返回 ok=false
func getIntEnv(key string) (int, bool) {
	v := os.Getenv(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}
