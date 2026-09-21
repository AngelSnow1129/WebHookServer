package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"smsserver/cache"
	"smsserver/config"
	"smsserver/handler"
	"smsserver/model"
	"smsserver/repository"
	"smsserver/service"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// version 由构建时注入（见 .github/workflows/release.yml 的 -ldflags "-X main.version=..."）。
// 默认 "dev" 使本地 go build 的产物能被明确识别为非正式发布版本。
var version = "dev"

func main() {
	// 第一行输出实际运行的版本，便于线上排查「部署的到底是哪个版本」
	log.Printf("[服务] 版本=%s", version)

	cfg := config.Load()

	// 校验必填配置
	if cfg.HMACSecret == "" {
		log.Fatal("[配置错误] HMAC_SECRET 不能为空")
	}
	if cfg.WebhookSecret == "" {
		log.Fatal("[配置错误] WEBHOOK_SECRET 不能为空")
	}

	// 连接数据库
	db, err := gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{})
	if err != nil {
		log.Fatalf("[数据库] 连接失败: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("[数据库] 获取连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(time.Hour)

	// 自动建表与建索引。
	// 索引由 model.SMSRecord 上的 gorm index 标签声明，AutoMigrate 会幂等创建，
	// 不再手写 CREATE INDEX（MySQL 不支持 CREATE INDEX IF NOT EXISTS，手写会报 1064）。
	if err := db.AutoMigrate(&model.SMSRecord{}); err != nil {
		log.Fatalf("[数据库] 迁移失败: %v", err)
	}

	// 初始化各模块
	repo := repository.NewSMSRepository(db)
	otpCache := cache.NewOTPCache(cfg.OTPCacheTTL)
	svc := service.NewOTPService(repo, otpCache, cfg.HMACSecret)
	h := handler.NewHandler(svc, cfg.WebhookSecret)

	// 启动清理协程
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitCleanup := svc.StartCleanupWorker(ctx, cfg.CleanupInterval)

	// 注册路由（路由定义集中在 handler.NewRouter，便于测试覆盖）
	mux := handler.NewRouter(h)

	// HTTP服务配置
	server := &http.Server{
		Addr:         cfg.ServerAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// 启动服务
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("[服务] 监听地址: %s", cfg.ServerAddr)
		// 记录生效配置，避免 TTL/清理间隔只能从日志倒推
		log.Printf("[服务] 验证码有效期=%v 清理间隔=%v", cfg.OTPCacheTTL, cfg.CleanupInterval)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// 优雅关闭
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		log.Fatalf("[服务] 启动失败: %v", err)
	case sig := <-quit:
		log.Printf("[服务] 正在关闭... signal=%s", sig)
	}

	// 停止接收新请求，等待在途 HTTP 请求结束
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("[服务] 关闭异常: %v", err)
	}

	// 停掉清理协程，并等待在途短信处理完成，避免丢短信
	cancel()
	svc.WaitInFlight()
	waitCleanup()

	log.Println("[服务] 已停止")
}
