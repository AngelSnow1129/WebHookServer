package cache

import (
	"testing"
	"time"
)

func TestSetAndGet(t *testing.T) {
	c := NewOTPCache(time.Minute)
	c.Set("k", "123456")

	code, ok := c.Get("k")
	if !ok || code != "123456" {
		t.Fatalf("Get = (%q, %v), 期望 (\"123456\", true)", code, ok)
	}
}

func TestGetMissingKey(t *testing.T) {
	c := NewOTPCache(time.Minute)

	if code, ok := c.Get("absent"); ok {
		t.Fatalf("不存在的 key 应返回 false，实际 (%q, %v)", code, ok)
	}
}

func TestGetAfterTTLExpires(t *testing.T) {
	c := NewOTPCache(30 * time.Millisecond)
	c.Set("k", "123456")

	time.Sleep(60 * time.Millisecond)

	if code, ok := c.Get("k"); ok {
		t.Fatalf("TTL 过期后仍可读到 %q", code)
	}
}

// TestGetAndDeleteIsOneShot 验证阅后即焚语义：
// 验证码被成功取走后必须立即失效，否则同一验证码可被重复使用。
func TestGetAndDeleteIsOneShot(t *testing.T) {
	c := NewOTPCache(time.Minute)
	c.Set("k", "123456")

	code, ok := c.GetAndDelete("k")
	if !ok || code != "123456" {
		t.Fatalf("首次 GetAndDelete = (%q, %v), 期望 (\"123456\", true)", code, ok)
	}

	if code, ok := c.GetAndDelete("k"); ok {
		t.Fatalf("第二次 GetAndDelete 仍返回 %q，阅后即焚失效", code)
	}
	if code, ok := c.Get("k"); ok {
		t.Fatalf("GetAndDelete 之后 Get 仍返回 %q", code)
	}
}

func TestGetAndDeleteExpired(t *testing.T) {
	c := NewOTPCache(30 * time.Millisecond)
	c.Set("k", "123456")

	time.Sleep(60 * time.Millisecond)

	if code, ok := c.GetAndDelete("k"); ok {
		t.Fatalf("过期后 GetAndDelete 仍返回 %q", code)
	}
}

// TestSetOverwritesExisting 验证同一号码收到新短信时旧验证码被覆盖。
func TestSetOverwritesExisting(t *testing.T) {
	c := NewOTPCache(time.Minute)
	c.Set("k", "111111")
	c.Set("k", "222222")

	code, ok := c.Get("k")
	if !ok || code != "222222" {
		t.Fatalf("Get = (%q, %v), 期望 (\"222222\", true)", code, ok)
	}
}

func TestDelete(t *testing.T) {
	c := NewOTPCache(time.Minute)
	c.Set("k", "123456")
	c.Delete("k")

	if _, ok := c.Get("k"); ok {
		t.Fatal("Delete 之后仍可读到")
	}
}

func TestCleanupRemovesOnlyExpired(t *testing.T) {
	c := NewOTPCache(40 * time.Millisecond)
	c.Set("expired-1", "111111")
	c.Set("expired-2", "222222")
	time.Sleep(80 * time.Millisecond)

	// 过期后再写入一个仍有效的条目
	c.Set("alive", "333333")
	c.Set("alive", "333333")

	removed := c.Cleanup()
	if removed != 2 {
		t.Fatalf("Cleanup 移除 %d 个，期望 2 个", removed)
	}
	if n := c.Len(); n != 1 {
		t.Fatalf("Cleanup 后 Len = %d，期望 1", n)
	}
	if code, ok := c.Get("alive"); !ok || code != "333333" {
		t.Fatalf("未过期条目被误删：(%q, %v)", code, ok)
	}
}

func TestCleanupOnEmptyCache(t *testing.T) {
	c := NewOTPCache(time.Minute)

	if removed := c.Cleanup(); removed != 0 {
		t.Fatalf("空缓存 Cleanup = %d，期望 0", removed)
	}
}

func TestLen(t *testing.T) {
	c := NewOTPCache(time.Minute)
	if n := c.Len(); n != 0 {
		t.Fatalf("初始 Len = %d，期望 0", n)
	}

	c.Set("a", "111111")
	c.Set("b", "222222")
	if n := c.Len(); n != 2 {
		t.Fatalf("Len = %d，期望 2（覆盖写入不应增加计数）", n)
	}

	c.Set("a", "333333")
	if n := c.Len(); n != 2 {
		t.Fatalf("覆盖写入后 Len = %d，期望 2", n)
	}
}

// TestConcurrentAccess 在 -race 下验证并发安全（webhook 与查询会并发访问缓存）。
func TestConcurrentAccess(t *testing.T) {
	c := NewOTPCache(time.Minute)
	done := make(chan struct{})

	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				c.Set("k", "123456")
				c.Get("k")
				c.GetAndDelete("k")
				c.Len()
			}
		}()
	}
	go func() {
		defer func() { done <- struct{}{} }()
		for j := 0; j < 100; j++ {
			c.Cleanup()
		}
	}()

	for i := 0; i < 9; i++ {
		<-done
	}
}
