//go:build integration

// MySQL 集成测试：需要真实数据库，通过 TEST_MYSQL_DSN 注入连接串。
// 未设置该变量时全部跳过，因此 `go test ./...` 在无数据库环境下不受影响。
//
// 本地运行（注意 DSN 指向**专用测试库**，用例会先 DropTable 清理）：
//
//	TEST_MYSQL_DSN='root:root@tcp(127.0.0.1:3306)/smsdb_test?parseTime=true&loc=Local&charset=utf8mb4' \
//	  go test -tags=integration -race ./repository/...
package repository

import (
	"os"
	"testing"
	"time"

	"smsserver/model"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// openTestDB 连接测试库并把表清到已知状态。
// 表结构由各用例自行 AutoMigrate，避免用例之间互相影响。
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("未设置 TEST_MYSQL_DSN，跳过 MySQL 集成测试")
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("连接测试数据库失败: %v", err)
	}

	// 从干净状态开始：残留的表/索引会让“索引是否真的建出来”这一断言失真
	if err := db.Migrator().DropTable(&model.SMSRecord{}); err != nil {
		t.Fatalf("清理测试表失败: %v", err)
	}
	return db
}

// indexColumn 对应 SHOW INDEX 返回的一行
type indexColumn struct {
	SeqInIndex int
	ColumnName string
	Collation  string // MySQL 8.0 用 'D' 标记降序、'A' 标记升序
}

// TestIntegrationAutoMigrateCreatesDescCompositeIndex 锁定索引修复。
//
// 修复前用手写 `CREATE INDEX IF NOT EXISTS`：MySQL 不支持该语法，执行报 1064，
// 而错误被丢弃，结果是索引从未建成、查询全表扫描且运维不可见。
// 现改为在模型上声明 gorm index 标签，由 AutoMigrate 幂等创建。
// 本用例断言索引真实存在、列顺序正确、且 created_at 为降序。
func TestIntegrationAutoMigrateCreatesDescCompositeIndex(t *testing.T) {
	db := openTestDB(t)

	if err := db.AutoMigrate(&model.SMSRecord{}); err != nil {
		t.Fatalf("AutoMigrate 失败: %v", err)
	}

	var cols []indexColumn
	if err := db.Raw(`SHOW INDEX FROM sms_records WHERE Key_name = ?`, "idx_recipient_time").
		Scan(&cols).Error; err != nil {
		t.Fatalf("查询索引失败: %v", err)
	}

	if len(cols) != 2 {
		t.Fatalf("索引 idx_recipient_time 的列数 = %d，期望 2（索引未建成或列数不符）", len(cols))
	}

	bySeq := make(map[int]indexColumn, len(cols))
	for _, c := range cols {
		bySeq[c.SeqInIndex] = c
	}

	if got := bySeq[1].ColumnName; got != "recipient" {
		t.Errorf("索引首列 = %q，期望 recipient", got)
	}
	if got := bySeq[2].ColumnName; got != "created_at" {
		t.Errorf("索引次列 = %q，期望 created_at", got)
	}
	if got := bySeq[2].Collation; got != "D" {
		t.Errorf("created_at 排序 = %q，期望 D（降序）", got)
	}
}

// TestIntegrationAutoMigrateIsIdempotent 验证重复启动不会因建索引失败而中止。
// 服务每次启动都会 AutoMigrate，幂等是它可用的前提。
func TestIntegrationAutoMigrateIsIdempotent(t *testing.T) {
	db := openTestDB(t)

	for i := 1; i <= 3; i++ {
		if err := db.AutoMigrate(&model.SMSRecord{}); err != nil {
			t.Fatalf("第 %d 次 AutoMigrate 失败: %v", i, err)
		}
	}

	var count int64
	// 注意：information_schema.statistics 的列名是 INDEX_NAME；
	// SHOW INDEX 输出的列别名才是 Key_name，两者不可混用。
	if err := db.Raw(
		`SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.statistics
		 WHERE table_schema = DATABASE() AND table_name = 'sms_records'
		   AND INDEX_NAME = 'idx_recipient_time'`,
	).Scan(&count).Error; err != nil {
		t.Fatalf("查询索引数量失败: %v", err)
	}
	if count != 1 {
		t.Errorf("重复迁移后 idx_recipient_time 数量 = %d，期望 1", count)
	}
}

// TestIntegrationCreateAndFindByRecipient 验证落库与按号码查询的往返，
// 并确认查询结果按 created_at 倒序（复合索引次列的方向）。
func TestIntegrationCreateAndFindByRecipient(t *testing.T) {
	db := openTestDB(t)

	if err := db.AutoMigrate(&model.SMSRecord{}); err != nil {
		t.Fatalf("AutoMigrate 失败: %v", err)
	}

	repo := NewSMSRepository(db)
	base := time.Now().Truncate(time.Millisecond)
	code := "123456"

	records := []*model.SMSRecord{
		{Provider: "twilio", Sender: "+8613800000000", Recipient: "+8613900000001", Body: "验证码123456", ExtractedCode: &code, ReceivedAt: base, CreatedAt: base},
		{Provider: "twilio", Sender: "+8613800000000", Recipient: "+8613900000001", Body: "验证码654321", ExtractedCode: &code, ReceivedAt: base.Add(time.Second), CreatedAt: base.Add(time.Second)},
		{Provider: "twilio", Sender: "+8613800000000", Recipient: "+8613900000002", Body: "验证码000000", ExtractedCode: &code, ReceivedAt: base, CreatedAt: base},
	}
	for i, rec := range records {
		if err := repo.Create(rec); err != nil {
			t.Fatalf("插入第 %d 条记录失败: %v", i+1, err)
		}
	}

	got, err := repo.FindByRecipient("+8613900000001")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("号码 +8613900000001 记录数 = %d，期望 2（不应串到其它号码）", len(got))
	}
	if !got[0].CreatedAt.After(got[1].CreatedAt) {
		t.Errorf("结果未按 created_at 倒序：首条=%v 次条=%v", got[0].CreatedAt, got[1].CreatedAt)
	}

	// 自增主键应被回填，确认插入的确实是持久化记录而非仅内存对象
	for i, rec := range records {
		if rec.ID == 0 {
			t.Errorf("第 %d 条记录主键未回填，期望非 0", i+1)
		}
	}
}
