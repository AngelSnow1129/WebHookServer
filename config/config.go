package config

import (
	"os"
	"strconv"
	"time"
)

// Config 应用配置结构体
type Config struct {
	ServerAddr      string        // 服务监听地址
	MySQLDSN        string        // MySQL连接字符串
	HMACSecret      string        // 手机号HMAC密钥
	WebhookSecret   string        // Webhook鉴权密钥
	OTPCacheTTL     time.Duration // 验证码缓存过期时间
	CleanupInterval time.Duration // 后台清理间隔
}

// Load 从环境变量加载配置
func Load() *Config {
	return &Config{
		ServerAddr:      getEnv("SERVER_ADDR", ":53340"),
		MySQLDSN:        getEnv("MYSQL_DSN", "user:password@tcp(127.0.0.1:3306)/smsdb?parseTime=true&loc=Local"),
		HMACSecret:      getEnv("HMAC_SECRET", ""),
		WebhookSecret:   getEnv("WEBHOOK_SECRET", ""),
		OTPCacheTTL:     getMinutesEnv("OTP_CACHE_TTL_MINUTES", 5),
		CleanupInterval: getSecondsEnv("CLEANUP_INTERVAL_SECONDS", 30),
	}
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
