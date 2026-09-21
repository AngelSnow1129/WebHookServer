# SMSServer 运行日志规范

本文档定义 SMSServer 的运行日志级别、格式、字段与脱敏要求,记录当前实现状态,以及尚未落地的改进方向。

> **文档状态**:文中标注「实测」的输出均采集自当前代码(`go build` 后真实运行,MySQL 8.0)。标注「缺口」的条目是**尚未实现**的建议,修改代码后请同步更新本文档第 4、5、7 节。

## 1. 输出流:stdout 与 stderr 分离

这是运维接入前必须了解的事实:**服务的日志分布在两个不同的输出流上**。

| 输出流 | 来源 | 内容 |
|---|---|---|
| **stdout** | GORM 的默认 logger | SQL 日志。默认 `Warn` 级别下**只输出出错的 SQL 与超过 200ms 的慢 SQL**;正常情况下为空 |
| **stderr** | Go 标准库 `log`(默认输出面) | 应用自身的全部日志 |
| **stderr** | MySQL 驱动的 `[mysql]` 日志 | 连接层异常,如 `closing bad idle connection: EOF` |

实测(全新库,`OTP_CACHE_TTL_MINUTES=2`、`CLEANUP_INTERVAL_SECONDS=2`):

```console
$ ./sms-server >/tmp/out.log 2>/tmp/err.log

$ cat /tmp/out.log          # stdout —— 实测为空:建表未出错,也未超过 200ms 慢 SQL 阈值
$ cat /tmp/err.log          # stderr —— 应用日志
2026/09/21 10:31:52 [服务] 版本=dev
2026/09/21 10:31:52 [服务] 监听地址: :53361
2026/09/21 10:31:52 [服务] 验证码有效期=2m0s 清理间隔=2s
2026/09/21 10:31:52 [清理] 已启动，间隔=2s
2026/09/21 10:31:55 [短信] 处理完成 recipient_hash=52e9f5a2 提取到验证码=true 已入库=true
2026/09/21 10:31:58 [服务] 正在关闭... signal=terminated
2026/09/21 10:31:58 [清理] 已停止
2026/09/21 10:31:58 [服务] 已停止
```

**运维要点**:正常运行时 stdout 往往是**空的**,这不代表采集配错。stdout 只会在两类情况下出现内容——SQL 报错,或SQL 超过 200ms 慢阈值。因此采集侧不能以"stdout 有内容"作为异常判据,而应**同时采集并合流两个流**:SQL 错误只在 stdout,应用状态与驱动异常只在 stderr,单看任何一个文件都无法还原完整事件序列。

### GORM 日志的额外特征

1. **非标准格式**。GORM 的日志带自己的格式前缀(空行 + `SLOW SQL >= 200ms` / `Error` 标记 + `[耗时] [rows:N]` 后缀),没有级别字段,也不受 `log.SetFlags` 影响。
2. **默认只在出错或慢查询时输出**。GORM 默认 logger 为 `logger.Default`(`LogLevel: Warn`、`SlowThreshold: 200ms`、输出到 `os.Stdout`),其 `Trace` 的实现仅命中以下分支才打印:`l.LogLevel <= Silent` 直接返回;SQL 报错且 `LogLevel >= Error`;或耗时超过 `SlowThreshold` 且 `LogLevel >= Warn`。正常执行的 SQL(包括 `AutoMigrate` 建表)因此**不会输出**——这就是 stdout 通常为空的原因。
3. **慢查询日志的真实样貌**。当某条 SQL 超过 200ms 时(首次建表偶有发生),stdout 会输出:

```console

2026/09/21 10:29:20 /workspace/SMSServer/main.go:51 SLOW SQL >= 200ms
[367.508ms] [rows:0] CREATE TABLE `sms_records` (`id` bigint unsigned AUTO_INCREMENT,`provider` varchar(64) NOT NULL DEFAULT '',...,PRIMARY KEY (`id`))
```

> 该条被标记 `SLOW SQL >= 200ms` 只是首次建表的正常开销,不是性能问题,排障时不要据此告警。该样例采集自提交 `7431fee`(索引修复后、版本日志加入前),行号指向当时的 `AutoMigrate` 调用点 `main.go:51`;当前代码同一调用点已移至 `main.go:58`(上方新增 `version` 变量与启动日志)。
4. **GORM 日志自带调用点**。`/workspace/SMSServer/main.go:58` 这种绝对路径前缀由 GORM 的 `file:line` 配置产生,会暴露部署路径;应用自身的 `log` 输出则没有调用点(未调用 `log.SetFlags`,默认只有日期时间,不含 `Lshortfile`)。

## 2. 当前日志级别:无

代码**没有引入任何日志级别机制**,统一使用标准库 `log`。默认配置下全部输出写到 stderr,不区分级别:

| 标准库 API | 实际语义 | 当前使用场景 |
|---|---|---|
| `log.Fatal` / `log.Fatalf` | 输出后 `os.Exit(1)` | 启动阶段不可恢复错误 |
| `log.Printf` | **信息、警告、错误共用** | 启动信息、鉴权失败、写库失败、HTTP 关闭异常 |
| `log.Println` | 同上 | 清理协程停止、服务停止 |

后果:

- **无法按级别过滤**。`log.Printf("[数据库] 插入失败: %v", err)` 与 `log.Printf("[服务] 监听地址: %s", ...)` 同级,采集侧无法只保留错误。
- **失败不产生告警信号**。写库失败只打印一行文字,没有 non-zero 退出码、没有 metric,进程继续运行,上游无从感知。
- **`log.Fatal` 会跳过 defer**。`main.go` 中所有 `log.Fatalf` 都直接 `os.Exit(1)`,`main` 里已注册的 `defer cancel()` 不会执行(进程随即退出,实际影响有限,但这是 `log.Fatal` 的通用陷阱)。

## 3. 目标级别规范

迁移到带级别的日志时,按下表归类。**第 3 列标「已具备」的点位当前已存在,只是没有级别字段**:

| 级别 | 使用场景 | 是否告警 | 当前对应点位 |
|---|---|---|---|
| `DEBUG` | 单条短信处理细节、缓存命中/未命中、OTP 查询结果 | 否,生产默认关闭 | **缺口**:缓存命中情况与 OTP 查询结果未记录 |
| `INFO` | 服务生命周期、配置生效值、清理周期汇总 | 否 | **已具备**:`[服务] 监听地址`、`[服务] 验证码有效期=...`、`[清理] 已启动`、`[清理] 移除=N`、`[服务] 正在关闭`、`[服务] 已停止`、`[清理] 已停止`、`[短信] 处理完成` |
| `WARN` | 可恢复的异常:webhook 鉴权失败、请求体非法、单条短信写库失败 | 视频率 | **已具备**:`[网关] webhook 鉴权失败`、`[网关]/[查询] 请求体解析失败`、`[数据库] 插入失败`。未提取到验证码的情况由 `[短信] 处理完成 ... 提取到验证码=false` 体现 |
| `ERROR` | 需要人工介入:数据库连接失败、迁移失败、关闭异常 | 是 | **已具备**:`[数据库] 连接失败`、`[数据库] 获取连接失败`、`[数据库] 迁移失败`、`[服务] 关闭异常` |
| `FATAL` | 启动期不可恢复:必填配置缺失、HTTP 启动失败 | 是 | **已具备**:`[配置错误] HMAC_SECRET 不能为空`、`[配置错误] WEBHOOK_SECRET 不能为空`、`[服务] 启动失败` |

**已补齐的历史缺口:handler 层的失败现在会写日志。** 鉴权失败(401)、请求体非法(400)均记录 `remote` 地址与路径,因此**暴力猜测 `WEBHOOK_SECRET` 的行为在日志中可见**,可用于安全审计。方法不允许(405)仍不记录,归为 `DEBUG` 级噪声。

## 4. 格式规范

### 现状

应用日志格式为:

```
<标准库默认时间戳> [<模块>] <中文消息> <可选 key=value>
```

例如:

```
2026/09/21 10:31:55 [短信] 处理完成 recipient_hash=52e9f5a2 提取到验证码=true 已入库=true
```

存在的问题:

| 问题 | 说明 |
|---|---|
| 无级别字段 | 见第 2 节 |
| 中文消息 + 中文全角标点 | `[清理] 已启动，间隔=2s` 中 `，` 是全角逗号,与 `=` 混用,解析困难 |
| 值混用中文 | `提取到验证码=true 已入库=true` 的键名是中英混排,不利于字段化解析 |
| 无结构化字段 | `移除=1 剩余=0` 是自由文本,只能正则提取 |
| 无请求关联标识 | 无法把一次 webhook 调用产生的多行日志串起来(仅有 `recipient_hash` 可做弱关联) |
| 时间戳精度不足 | 标准库默认 `2006/01/02 15:04:05`,无毫秒,高并发下无法排序 |

### 目标格式

推荐迁移到 Go 1.21 内置的 `log/slog`(无新增第三方依赖),输出 JSON:

```json
{"time":"2026-09-21T10:31:55.123+08:00","level":"INFO","msg":"sms processed","module":"sms","provider":"twilio","recipient_hash":"52e9f5a2","code_extracted":true,"db_ok":true}
```

字段命名约定:

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `time` | RFC3339 毫秒 | 是 | 由 handler 自动填充 |
| `level` | string | 是 | `DEBUG`/`INFO`/`WARN`/`ERROR` |
| `msg` | string | 是 | 简短英文标识(如 `sms processed`),**不用中文长句** |
| `module` | string | 是 | 见第 5 节枚举 |
| `err` | string | 视情况 | 错误详情 |
| `request_id` | string | 建议 | webhook 请求关联 ID,由 handler 生成并透传到 service |
| `duration_ms` | number | 建议 | 耗时 |

若沿用文本格式,则统一为**半角**、固定顺序:

```
2026-09-21T10:31:55.123+08:00 INFO  [sms] processed provider=twilio recipient_hash=52e9f5a2 code_extracted=true db_ok=true
```

要点:时间戳带毫秒与毫秒级时区;级别左对齐定宽;模块用半角方括号;键值对全部半角 `=`。

## 5. 模块枚举

当前使用 **7 个**前缀,均以半角 `[` `]` 包裹。新增日志必须复用下列之一,不得自造:

| 模块 | 含义 | 覆盖文件 |
|---|---|---|
| `[服务]` | 进程生命周期、HTTP 服务状态 | `main.go` |
| `[配置错误]` | 配置校验失败 | `main.go` |
| `[数据库]` | 数据库连接、迁移、写入 | `main.go`、`service/otp_service.go` |
| `[短信]` | 短信接收与验证码提取 | `service/otp_service.go` |
| `[清理]` | 后台清理协程 | `service/otp_service.go` |
| `[网关]` | webhook 接入层(鉴权、请求体解析) | `handler/handler.go` |
| `[查询]` | `/api/v1/otp` 接口层 | `handler/handler.go` |

命名不一致处:`[配置错误]` 描述的是**状态**而非模块(其余都是名词性模块名),建议改为 `[配置]`,级别由 `FATAL` 承载"错误"语义。该建议未落地,以保持与启动检查点的兼容。

## 6. 字段与脱敏规范

**这是本规范最重要的一节。**

### 6.1 严禁记录的字段

| 字段 | 当前行为 | 要求 |
|---|---|---|
| **验证码明文** | **已整改**:只记录是否提取到(`提取到验证码=true`) | **禁止记录** |
| **缓存 token(完整)** | **已整改**:只记录 HMAC 前 8 位(`recipient_hash=52e9f5a2`) | 仅允许前 8 位(HMAC 前 8 位足以关联,不足以反推) |
| 手机号明文 | 未打印 | 保持禁止;需要定位时使用 HMAC 前 8 位 |
| `HMAC_SECRET` / `WEBHOOK_SECRET` | 未打印,包括启动配置回显 | 永久禁止 |
| 短信正文 | 未打印(正文已落库,排查请查库) | 保持禁止 |
| 请求体原文 | 未打印 | 保持禁止 |
| 客户端地址 | `remote=127.0.0.1:59758` 会记录 | 允许;用于识别爆破来源 IP |

### 6.2 为什么验证码明文入日志是高危

验证码的有效性完全建立在"仅收件人可见"之上。历史实现曾输出 `[短信] 验证码=123456 token=52e9f5a2...e923d4`,**同时泄漏了验证码本身和定位该验证码的 key**。一旦日志具备以下任一条件,攻击者即可完整冒用验证码:

- 日志被集中采集到 Elasticsearch / Loki / 云日志服务,访问面远大于数据库;
- 日志被同步到第三方或用于离线分析;
- 日志文件被低权限运维、开发人员读取。

当前实现已改为只记录**是否**提取到验证码以及收件人的 HMAC 前 8 位:

```go
// 当前实现
log.Printf("[短信] 处理完成 recipient_hash=%s 提取到验证码=%t 已入库=%t",
    token[:tokenLogPrefixLen], code != "", dbErr == nil)
```

**升级遗留日志系统时,仍需清理历史日志中的验证码。**

### 6.3 token 的前缀截断

`recipient_hash` 是 `HMAC-SHA256(手机号, HMAC_SECRET)` 完整十六进制的前 8 位。前 8 位足以在对比同一收件人的多条日志时完成关联,同时保留了足够熵不可反推。字段名使用 `recipient_hash` 而非 `token`,避免被误读为完整值。截断长度由 `service.tokenLogPrefixLen` 常量定义。

> 注意:`token` 在代码里是"收件人号码的 HMAC"的变量名,与 `/api/v1/otp` 接口请求体中的 `token` 字段是同一个值,不要与 `WEBHOOK_SECRET` 混淆。

## 7. 日志点位清单

全项目共 **20 处**显式 `log` 调用,分布在 3 个文件。下表为逐点对照。

### `main.go`(12 处)

| 行 | 当前日志 | 建议级别 | 备注 |
|---|---|---|---|
| 29 | `[服务] 版本=%s` | INFO | 新增;由 `-ldflags "-X main.version=..."` 注入,本地构建为 `dev`,正式发布为 tag 号 |
| 35 | `[配置错误] HMAC_SECRET 不能为空` | FATAL | 保留;`log.Fatal` 已具备中止语义 |
| 38 | `[配置错误] WEBHOOK_SECRET 不能为空` | FATAL | 同上 |
| 44 | `[数据库] 连接失败: %v` | FATAL | 保留;建议附加脱敏后的 DSN(host/db,剔除口令) |
| 49 | `[数据库] 获取连接失败: %v` | FATAL | 保留 |
| 59 | `[数据库] 迁移失败: %v` | FATAL | 保留;**已不再出现 `Error 1064`**,见 8.7 |
| 88 | `[服务] 监听地址: %s` | INFO | 保留 |
| 90 | `[服务] 验证码有效期=%v 清理间隔=%v` | INFO | 已补充;解决 TTL/间隔只能从日志倒推的问题 |
| 102 | `[服务] 启动失败: %v` | ERROR | 在 goroutine 中调用 `Fatalf`;理想做法是记录后向主 goroutine 传递错误 |
| 104 | `[服务] 正在关闭... signal=%s` | INFO | 保留;已补充 `signal` 字段 |
| 111 | `[服务] 关闭异常: %v` | ERROR | 保留 |
| 119 | `[服务] 已停止` | INFO | 保留;此行为关闭完成的标志,见 8.6 |

### `service/otp_service.go`(5 处)

| 行 | 当前日志 | 建议级别 | 备注 |
|---|---|---|---|
| 82 | `[数据库] 插入失败: recipient_hash=%s err=%v` | ERROR | **注意验证码此时仍在缓存中可用**,见 8.4;建议增加失败计数与告警阈值 |
| 86 | `[短信] 处理完成 recipient_hash=%s 提取到验证码=%t 已入库=%t` | INFO | 已脱敏;建议降为 DEBUG 或按需采样 |
| 116 | `[清理] 已启动，间隔=%v` | INFO | 保留;全角逗号需改半角 |
| 120 | `[清理] 已停止` | INFO | 保留 |
| 125 | `[清理] 移除=%d 剩余=%d` | INFO(建议降为 DEBUG) | 仅在 `removed > 0` 时打印,静默期无输出 |

### `handler/handler.go`(3 处)

| 行 | 当前日志 | 建议级别 | 备注 |
|---|---|---|---|
| 82 | `[网关] webhook 鉴权失败 path=%q remote=%s` | WARN | **安全审计必需**,已补齐 |
| 98 | `[网关] webhook 请求体解析失败 remote=%s err=%v` | WARN | 排查上游格式错误 |
| 133 | `[查询] 请求体解析失败 remote=%s err=%v` | WARN | 同上 |

### 未被记录但应当记录的事件(缺口)

| 事件 | 位置 | 建议级别 | 理由 |
|---|---|---|---|
| 方法不允许 | `handler.go` webhook / otp 入口 | DEBUG | 噪声较大,仅调试期需要 |
| OTP 查询命中/未命中 | `handler.go` `GetOTP` | DEBUG | 排障主力:区分"没收到短信"与"取过了" |
| 缓存被新验证码覆盖 | `service.ProcessIncomingSMS` | DEBUG | 排查"验证码频繁失效" |
| 未知路径 404 | 路由层 | DEBUG | 排查调用方路径拼写错误 |

## 8. 实测日志样例

以下为真实运行采集的输出(MySQL 8.0,`OTP_CACHE_TTL_MINUTES=2`、`CLEANUP_INTERVAL_SECONDS=2`)。

### 8.1 正常启动

```console
2026/09/21 10:31:52 [服务] 版本=dev
2026/09/21 10:31:52 [服务] 监听地址: :53361
2026/09/21 10:31:52 [服务] 验证码有效期=2m0s 清理间隔=2s
2026/09/21 10:31:52 [清理] 已启动，间隔=2s
```

四行顺序不固定(`[清理] 已启动` 由独立协程输出)。首行 `版本=dev` 是本地 `go build` 的产物;正式发布由 CI 注入 tag 号,显示为 `版本=v1.0.0`。

### 8.2 成功处理一条短信

```console
2026/09/21 10:31:55 [短信] 处理完成 recipient_hash=52e9f5a2 提取到验证码=true 已入库=true
```

> 已脱敏:不含验证码明文、不含完整 token、不含手机号。`recipient_hash` 是收件人号码 HMAC 的前 8 位。

### 8.3 验证码过期后的清理

```console
2026/09/21 10:31:56 [清理] 移除=1 剩余=0
```

仅在 `removed > 0` 时输出,静默期无日志——**不要用"没有清理日志"判断服务异常**。

### 8.4 数据库不可用时的写库失败

实测(接收一条短信前停掉 MySQL,`recipient` 为 `+8613999999999`):

```console
[mysql] 2026/09/21 10:32:51 connection.go:49: closing bad idle connection: EOF
2026/09/21 10:32:51 [数据库] 插入失败: recipient_hash=bdcd0919 err=dial tcp 127.0.0.1:13306: connect: connection refused
2026/09/21 10:32:51 [短信] 处理完成 recipient_hash=bdcd0919 提取到验证码=true 已入库=false
```

同时 `/api/v1/otp` 仍然返回验证码:

```console
$ curl -s -X POST http://127.0.0.1:53362/api/v1/otp -d '{"token":"<HMAC(+8613999999999)>"}'
{"status":"success","code":"654321"}
```

**注意顺序**:`[数据库] 插入失败` 在 `[短信] 处理完成` **之前**。代码先尝试写库并记录失败,再输出处理完成汇总。两条日志共享同一个 `recipient_hash`,可据此配对。

**注意这里的严重不一致**:尽管写库失败(短信未持久化),验证码早已写入内存缓存,因此**调用方仍能成功取到验证码**。也就是说 `/api/v1/otp` 返回 `success` 时,该条短信在数据库中可能根本不存在——"验证码可用"与"记录已留存"是两件独立的事。排障时不能以"库里没有记录"判定验证码未下发。

`已入库=false` 是判断该情况的关键字段:出现它即表示验证码可用但记录已丢失。注意 `[mysql] closing bad idle connection` 这类驱动日志**不属于应用日志**,它来自 MySQL 驱动,且同样写入 stderr(见第 1 节)。

此外,写库失败后**没有任何重试**,该条短信永久丢失,且无失败计数、无告警。

### 8.5 优雅关闭

```console
2026/09/21 10:31:58 [服务] 正在关闭... signal=terminated
2026/09/21 10:31:58 [清理] 已停止
2026/09/21 10:31:58 [服务] 已停止
```

三行顺序固定,可用于确认关闭流程完整执行。

### 8.6 关闭流程的在途追踪

关闭顺序为:停止接收新请求 → 等待在途 HTTP 请求(`server.Shutdown`)→ 取消清理协程并等待其退出 → 等待在途短信写库完成(`WaitInFlight`)→ 打印 `[服务] 已停止`。

因此 **`[服务] 已停止` 之后不应再有业务日志**。若发现 `[服务] 已停止` 之后仍出现 `[短信] 处理完成`,说明关闭顺序被改动,在途任务未被正确等待,此时正在进行的写库可能被进程退出打断。

### 8.7 建索引:从静默失败到幂等建表

历史实现在 `main.go` 中执行裸 SQL:

```go
db.Exec("CREATE INDEX IF NOT EXISTS idx_recipient_time ON sms_records (recipient, created_at DESC)")
```

MySQL **不支持 `CREATE INDEX IF NOT EXISTS`**,启动时会报错,且返回值被丢弃:

```console

2026/09/21 10:20:54 /workspace/SMSServer/main.go:54 Error 1064 (42000): You have an error in your SQL syntax; check the manual that corresponds to your MySQL server version for the right syntax to use near 'IF NOT EXISTS idx_recipient_time ON sms_records (recipient, created_at DESC)' at line 1
[0.083ms] [rows:0] CREATE INDEX IF NOT EXISTS idx_recipient_time ON sms_records (recipient, created_at DESC)
```

后果是索引**从未创建**,按号码的查询退化为全表扫描,而应用层完全无感知(只有 GORM 的 stdout 日志留下痕迹——注意这条**确实是 stdout**,因为它是错误分支,命中 `LogLevel >= Error`)。

当前实现改为在模型上声明索引标签,由 `AutoMigrate` 幂等创建:

```go
Recipient string    `gorm:"...;index:idx_recipient_time,priority:1"`
CreatedAt time.Time `gorm:"...;index:idx_recipient_time,sort:desc,priority:2"`
```

实测索引正确生成:

```sql
KEY `idx_recipient_time` (`recipient`,`created_at` DESC)
```

且 `AutoMigrate` 的错误已被校验(`[数据库] 迁移失败`),不会静默失败。**日志中不应再出现 `Error 1064`**;若出现,说明迁移逻辑被改回手写 SQL。

## 9. 排障对照表

| 日志中出现 | 含义 | 处置 |
|---|---|---|
| `[配置错误] HMAC_SECRET 不能为空` | 启动即中止,未监听端口 | 注入 `HMAC_SECRET` 后重启 |
| `[配置错误] WEBHOOK_SECRET 不能为空` | 同上 | 注入 `WEBHOOK_SECRET` 后重启 |
| `[数据库] 连接失败` | DSN 错误或 MySQL 不可达 | 核对 `MYSQL_DSN`,确认 `parseTime=true` 与 MySQL 可达 |
| `[数据库] 迁移失败` | 建表/建索引失败 | 检查账号是否有 DDL 权限;确认已不再使用手写 `CREATE INDEX`(8.7) |
| `[服务] 验证码有效期=5m0s 清理间隔=30s` | 未显式设置两个时间变量,使用默认值 | 按业务需求显式设置 `OTP_CACHE_TTL_MINUTES` / `CLEANUP_INTERVAL_SECONDS` |
| `[服务] 验证码有效期=5s 清理间隔=30s` | **异常**:TTL 明显过短,疑似单位解析缺陷回归 | 见 README「配置项」;运行 `go test ./config/` 验证 |
| `[网关] webhook 鉴权失败 path=...` | 密钥不匹配或调用方路径拼错 | 核对 `WEBHOOK_SECRET`;**频率突增即为密钥爆破,需告警** |
| `[网关] webhook 请求体解析失败` | 上游发送的不是合法 JSON 或超过 64 KiB | 核对上游请求体格式与大小 |
| `[短信] 处理完成 ... 提取到验证码=false` | 短信内容中未匹配到 4-8 位数字,该短信仍会落库 | 检查供应商模板是否变更;确认正文含验证码 |
| `[数据库] 插入失败: ... connection refused` | MySQL 中断,短信未落库,**但验证码仍在缓存中可用** | 见 8.4 节;关注 `已入库=false` |
| `[服务] 已停止` 后仍有业务日志 | 关闭顺序被改动,在途任务未等待 | 见 8.6 节 |
| `Error 1064 ... near 'IF NOT EXISTS'` | 迁移逻辑被改回手写 `CREATE INDEX` | 见 8.7 节 |
| webhook 返回 404 但日志无任何输出 | 路由未命中 | 检查调用方路径前缀是否为 `/api/v1/webhook/sms/`;注意 Go 1.21 的 `ServeMux` 不支持 `{token}` 通配语法,路由按前缀注册 |
| 服务已启动但收不到短信 | 优先检查路由与响应码,再查鉴权日志 | 同上 |

## 10. 采集与告警建议

### 采集

1. **同时采集 stdout 与 stderr**(见第 1 节),合流后按标签区分来源;
2. 若沿用文本格式,采集侧需能解析两种格式:标准库格式与应用前缀 `[模块]` 两套正则;
3. 过滤规则:排除 GORM 的建表 DDL 与 `SLOW SQL >= 200ms` 的建表噪声。

### 告警

| 条件 | 级别 | 依据 |
|---|---|---|
| 出现 `[配置错误]` / `[数据库] 连接失败` / `[数据库] 迁移失败` | 紧急 | 服务未起来 |
| `Error 1064` 或 `[数据库] 迁移失败` | 高 | 索引缺失会导致查询退化(8.7) |
| `已入库=false` 或单位时间内 `[数据库] 插入失败` 超过阈值 | 高 | 短信正在丢失,但验证码对外仍可用(8.4) |
| `[网关] webhook 鉴权失败` 频率突增 | 高 | 疑似密钥爆破 |
| `[服务] 已停止` 后进程仍存在 | 中 | 关闭流程卡住 |
| 长时间无 `[清理] 移除=` 输出 | 低 | 可能只是无过期条目,勿误报(8.3) |

## 11. 待落地的改进

1. **接入 `log/slog`**:Go 1.21 已内置,零新增依赖,支持级别与结构化字段;通过 `slog.SetDefault` 统一替换标准库 `log`;
2. **补充级别字段**并为 handler 的 405、OTP 查询结果补 DEBUG 点位(见第 7 节缺口表);
3. **收敛 GORM 日志**:显式配置 logger,关闭或降级 SQL 输出,消除 stdout/stderr 双流;
4. **统一时间戳精度**:启用毫秒级时间与 `Lshortfile`,或在 slog 中统一处理;
5. **补充字段**:为 webhook 处理引入 `request_id`,贯穿 handler → service;
6. **写库失败的重试与计数**:写库失败目前仅一行日志,无重试、无指标(8.4);
7. **文档同步**:字段或模块枚举变更时,同步更新本文档第 4、5、7 节。
