# SMSServer 短信验证码中继服务

从短信供应商回调中自动提取验证码并缓存,供调用方轮询获取的服务。调用方无需接入各短信平台的 API,只需接收 webhook 并按手机号取码。

## 数据流

```
供应商 / 运营商 / 短信网关
      │  POST /api/v1/webhook/sms/{token}
      ▼
  handler.WebhookSMS ── 校验 token ──► 立即返回 200 {"status":"ok"}
      │  ProcessIncomingSMSAsync(纳入 WaitGroup 在途追踪)
      ▼
  service.ProcessIncomingSMS
      ├─ extractCode()  正则 \b(\d{4,8})\b 提取验证码
      ├─ cache.Set(HMAC-SHA256(收件人号码), 验证码)   ← 内存缓存,带 TTL
      └─ repo.Create()  原始短信落库 MySQL

业务调用方
      │  POST /api/v1/otp  {"token": "<HMAC(收件人号码)>"}
      ▼
  handler.GetOTP ──► cache.GetAndDelete(key)   ← 阅后即焚
```

**关键设计**:内存缓存的 key 不是手机号本身,而是 `HMAC-SHA256(手机号, HMAC_SECRET)` 的十六进制。调用方必须持有同一个 `HMAC_SECRET` 才能算出 key 取码,服务端因此不需要在内存里保存明文手机号作为索引。

## 目录结构

```
SMSServer/
├── main.go                          入口:配置加载、数据库连接、路由注册、优雅关闭
├── config/config.go                 环境变量配置加载
├── config/config_test.go            配置单位与默认值测试
├── handler/handler.go               HTTP 处理器、路由注册
├── handler/handler_test.go          路由匹配、鉴权、请求校验测试
├── service/otp_service.go           业务逻辑:验证码提取、缓存写入、在途追踪、清理协程
├── service/otp_service_test.go      验证码提取、缓存写入、在途追踪测试
├── cache/otp_cache.go               线程安全内存缓存(TTL 过期 + 阅后即焚)
├── cache/otp_cache_test.go          TTL、阅后即焚、并发安全测试
├── repository/sms_repo.go           MySQL 仓储层(GORM)
├── model/sms.go                     数据模型(含索引标签)、HMAC 工具函数
├── docs/LOGGING.md                  运行日志规范
├── .env.example                     环境变量示例
├── .gitignore
├── go.mod / go.sum
└── sms-server                       编译产物(已被 .gitignore 忽略)
```

代码分层为 `main → handler → service → {cache, repository} → model`,无 Web 框架,仅依赖 `gorm.io/gorm` 与 MySQL 驱动。为便于测试,`handler` 与 `service` 分别通过 `otpService`、`smsRepository` 接口解耦具体实现。

## 环境要求

| 项 | 版本 |
|---|---|
| Go | 1.21.4(`go.mod` 声明) |
| MySQL | 8.0(使用 `datetime(3)` 与 `CREATE INDEX ... DESC`) |

> **关于路由实现**:Go 1.21 的 `net/http.ServeMux` **不支持** `{token}` 通配语法(该特性在 Go 1.22 引入)。因此 webhook 路由按**前缀**注册(`handler.WebhookPathPrefix`),由处理器从路径中取出 token。若将来升级到 Go 1.22+,可改用 `r.PathValue("token")`,但当前实现无需依赖版本特性。

## 快速开始

### 1. 准备 MySQL

```bash
docker run -d --name sms-mysql \
  -e MYSQL_ROOT_PASSWORD=root \
  -e MYSQL_DATABASE=smsdb \
  -p 3306:3306 \
  mysql:8.0
```

表结构与索引由启动时的 `AutoMigrate` 自动创建,无需手工建表或建索引。索引在 `model.SMSRecord` 上通过 GORM 标签声明:

```go
Recipient string    `gorm:"...;index:idx_recipient_time,priority:1"`
CreatedAt time.Time `gorm:"...;index:idx_recipient_time,sort:desc,priority:2"`
```

`AutoMigrate` 具备幂等性,重复启动不会报错。之所以不手写 `CREATE INDEX`,是因为 **MySQL 不支持 `CREATE INDEX IF NOT EXISTS`**,手写会导致启动时报 `Error 1064` 且索引静默缺失。

### 2. 配置环境变量

两个密钥均为**必填**,为空时进程立即退出。本服务**不读取 `.env` 文件**,`.env.example` 仅作变量清单参考,需自行 `export` 或用进程管理器注入。

```bash
export MYSQL_DSN='root:root@tcp(127.0.0.1:3306)/smsdb?parseTime=true&loc=Local&charset=utf8mb4'
export HMAC_SECRET='至少16位的随机字符串-请务必更换'
export WEBHOOK_SECRET='至少16位的随机字符串-请务必更换'
export SERVER_ADDR=':53340'
```

### 3. 编译运行

```bash
go build -o sms-server .
./sms-server
```

`Ctrl+C`(SIGINT)或 SIGTERM 触发优雅关闭:先停止接收新请求并等待在途 HTTP 请求,再停止清理协程,最后**等待在途短信处理完成**后才退出。

### 4. 验证

```bash
SECRET='替换为 WEBHOOK_SECRET'
HMACKEY='替换为 HMAC_SECRET'
PORT=53340

# 发送一条模拟短信,期望 200 {"status":"ok"}
curl -s -w '\n[HTTP=%{http_code}]\n' -X POST "http://127.0.0.1:$PORT/api/v1/webhook/sms/$SECRET" \
  -H 'Content-Type: application/json' \
  -d '{"provider":"twilio","sender":"+8613800000000","recipient":"+8613900000000","body":"【某某】您的验证码是123456，5分钟内有效。"}'

# 计算收件人号码的 token 并取码,期望 {"status":"success","code":"123456"}
TOKEN=$(python3 -c "import hmac,hashlib;print(hmac.new(b'$HMACKEY',b'+8613900000000',hashlib.sha256).hexdigest())")
curl -s -X POST "http://127.0.0.1:$PORT/api/v1/otp" -d "{\"token\":\"$TOKEN\"}"

# 再次查询,期望 {"status":"pending"}(阅后即焚)
curl -s -X POST "http://127.0.0.1:$PORT/api/v1/otp" -d "{\"token\":\"$TOKEN\"}"
```

启动日志中会打印生效配置,便于确认:

```
[服务] 监听地址: :53340
[服务] 验证码有效期=5m0s 清理间隔=30s
[清理] 已启动，间隔=30s
```

## 配置项

全部配置来自环境变量,无配置文件。

| 变量 | 必填 | 默认值 | 单位 | 说明 |
|---|---|---|---|---|
| `SERVER_ADDR` | 否 | `:53340` | — | HTTP 监听地址 |
| `MYSQL_DSN` | 否 | `user:password@tcp(127.0.0.1:3306)/smsdb?parseTime=true&loc=Local` | — | GORM 连接串,`parseTime=true` 必须保留 |
| `HMAC_SECRET` | **是** | 无 | — | 手机号 HMAC-SHA256 密钥。为空时启动失败 |
| `WEBHOOK_SECRET` | **是** | 无 | — | webhook 鉴权密钥。为空时启动失败 |
| `OTP_CACHE_TTL_MINUTES` | 否 | `5` | **分钟** | 验证码有效期。设置为 `5` 即 5 分钟 |
| `CLEANUP_INTERVAL_SECONDS` | 否 | `30` | **秒** | 后台清理间隔。不设置即为 30 秒 |

两个时间变量分别由 `config.getMinutesEnv` / `config.getSecondsEnv` 解析,单位与变量名一致,且非法值(非整数)会回退到默认值。相关行为由 `config/config_test.go` 覆盖。

## 接口文档

服务使用 Go 标准库 `net/http`,超时配置为:读 5s、写 5s、空闲 120s。请求体上限为 `64 KiB`(`handler.maxBodyBytes`),超出返回 400。

### 1. 短信回调

```
POST /api/v1/webhook/sms/{token}
```

| 项 | 说明 |
|---|---|
| `{token}` | 即 `WEBHOOK_SECRET`,作为路径的最后一段传递,服务端用 `subtle.ConstantTimeCompare` 比较 |
| 请求体 | JSON,`Content-Type` 未强制校验,上限 64 KiB |
| 处理方式 | **立即返回 200,业务异步处理**,但异步任务纳入在途追踪,优雅关闭时会等待完成 |

请求体字段:

| 字段 | 类型 | 说明 |
|---|---|---|
| `provider` | string | 供应商标识,如 `twilio` |
| `sender` | string | 发送方号码 |
| `recipient` | string | 接收方号码,**同时是缓存 key 的计算依据** |
| `body` | string | 短信原文,服务端从中提取验证码 |

响应:

| 状态码 | 响应体 | 场景 |
|---|---|---|
| 200 | `{"status":"ok"}` | 校验通过,已投递到后台处理 |
| 401 | `unauthorized`(纯文本) | token 为空、不匹配,或路径含多余层级(如 `.../sms/密钥/extra`) |
| 400 | `invalid json`(纯文本) | 请求体不是合法 JSON,或超过 64 KiB |
| 405 | `method not allowed`(纯文本) | 非 POST 方法 |

由于是异步处理,**返回 200 不代表短信已入库**,写库失败仅体现在服务端日志中。401 与 400 均会写入服务端日志(级别语义见 `docs/LOGGING.md`),便于识别密钥爆破。

### 2. 查询验证码

```
POST /api/v1/otp
```

请求体:

```json
{ "token": "<HMAC-SHA256(手机号, HMAC_SECRET) 的十六进制>" }
```

响应(`Content-Type: application/json`):

| 响应体 | 说明 |
|---|---|
| `{"status":"success","code":"123456"}` | 命中,`code` 为该号码最近一次提取到的验证码 |
| `{"status":"pending"}` | 未命中:未收到短信、已过期、已被取走、或 key 不匹配 |

| 状态码 | 响应体 | 场景 |
|---|---|---|
| 200 | 上述 JSON | 正常查询(未命中时也是 200) |
| 400 | `missing token`(纯文本) | `token` 为空或全为空白字符 |
| 400 | `invalid json`(纯文本) | 请求体不是合法 JSON,或超过 64 KiB |
| 405 | `method not allowed`(纯文本) | 非 POST 方法 |

`token` 会先做 `TrimSpace` 处理,首尾空白不影响查询。

**阅后即焚**:`code` 一旦被成功返回,该 key 立即从缓存删除,第二次查询得到 `pending`。因此调用方需要处理"取码失败"的轮询逻辑,且重试同一次查询不会有结果。

**缓存覆盖语义**:同一号码再次收到短信时,新验证码会**直接覆盖**旧值(`OTPCache.Set` 不做存在性检查),旧码立即失效。

### 错误响应格式不一致

4xx 错误统一为纯文本,200 成功为 JSON。客户端需要先判断 `Content-Type` 或状态码再决定如何解析。

## 数据模型

表 `sms_records`,由 GORM `AutoMigrate` 创建:

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| `id` | `bigint unsigned` | 主键,自增 | |
| `provider` | `varchar(64)` | not null, default `''` | 供应商 |
| `sender` | `varchar(128)` | not null, default `''` | 发送方号码 |
| `recipient` | `varchar(128)` | not null, default `''` | 接收方号码 |
| `body` | `text` | not null | 短信原文(含验证码明文) |
| `extracted_code` | `varchar(32)` | 可空 | 解析出的验证码;未提取到时为 NULL |
| `received_at` | `datetime(3)` | not null | 短信接收时间(毫秒精度) |
| `created_at` | `datetime(3)` | not null, default `CURRENT_TIMESTAMP(3)` | 入库时间 |

索引 `idx_recipient_time (recipient, created_at DESC)` 由索引标签声明并自动创建,用于支撑按号码的时间倒序查询:

```sql
KEY `idx_recipient_time` (`recipient`,`created_at` DESC)
```

## 验证码提取规则

`service/otp_service.go` 使用正则 `\b(\d{4,8})\b`,取**第一个**匹配到的 4-8 位数字。

| 短信内容 | 提取结果 |
|---|---|
| `【某某】您的验证码是123456，5分钟内有效。` | `123456` |
| `Your code is 12345` | `12345` |
| `您的验证码：123456` | `123456` |
| `验证码123456` | `123456` |
| `订单号20240921123456，验证码为9876` | `9876` |
| `您的话费余额不足，请及时充值。` | 无(不写入缓存,但原始短信仍落库) |

该规则是通用启发式,**没有针对验证码的语义识别**:短信中任何先于验证码出现的 4-8 位数字(订单号、金额、日期片段等)都可能被误当作验证码,并覆盖该号码当前的验证码缓存。若供应商模板固定,建议改为按模板匹配。

## 日志

日志格式为 `<时间戳> [模块] 消息 键值对`,例如:

```
2026/09/21 10:29:20 [服务] 监听地址: :53343
2026/09/21 10:29:20 [服务] 验证码有效期=3m0s 清理间隔=5s
2026/09/21 10:29:23 [短信] 处理完成 recipient_hash=52e9f5a2 提取到验证码=true 已入库=true
2026/09/21 10:29:30 [清理] 移除=1 剩余=0
```

**日志中不包含验证码明文、完整 token 或手机号明文**,仅记录收件人 HMAC 的前 8 位(`recipient_hash`)用于跨日志关联。字段与级别的完整规范见 [docs/LOGGING.md](docs/LOGGING.md)。

注意服务日志分布在两个输出流:**GORM 的 SQL 日志走 stdout,应用日志走 stderr**。容器采集需同时收集并合流。

## 开发

```bash
go build ./...         # 编译
go vet ./...           # 静态检查
gofmt -l .             # 格式检查
go test ./...          # 运行全部测试
go test -race ./...    # 竞态检测
go test -cover ./...   # 覆盖率
```

测试覆盖范围:

| 包 | 覆盖内容 |
|---|---|
| `cache` | TTL 过期、阅后即焚、覆盖写入、`Cleanup` 只清过期项、并发访问(`-race`) |
| `config` | 默认值、环境变量覆盖、分钟/秒单位正确性、非法值回退 |
| `handler` | **前缀路由能命中真实密钥**(关键回归)、字面量 `{token}` 被拒、鉴权各分支、非 POST、非法 JSON、超大请求体、`token` 裁剪、响应结构 |
| `service` | 验证码提取 13 种格式、缓存 key 正确性、无验证码时不写缓存、写库失败语义、阅后即焚、新码覆盖旧码、`WaitInFlight` 在途追踪、清理协程退出 |

`handler` 与 `service` 的测试通过接口注入 `fakeService` / `fakeRepository`,**不需要数据库**;`model`、`repository` 目前无独立测试(均为薄封装,由上述测试间接覆盖)。

## 安全建议

当前实现已修复路由失效、索引缺失、配置单位错误、验证码明文入日志等问题。生产环境使用前仍需注意:

| 项 | 说明 |
|---|---|
| **供应商签名校验** | 当前仅比对 URL 中的静态 token,**没有校验供应商签名**。拿到 URL 即可向任意号码注入任意验证码。建议接入各供应商的签名机制 |
| **每账号独立密钥** | 目前所有调用方共享同一个 `WEBHOOK_SECRET`,无法单独吊销。建议按调用方分配密钥 |
| `/api/v1/otp` 鉴权与限流 | 该接口无认证。知道 `HMAC(号码, secret)` 即可读取该号码验证码;建议增加调用方认证与限流 |
| 短信原文留存 | `body` 含验证码明文且**长期保存、无清理策略**。建议缩短保留期或仅存必要字段 |
| 密钥强度 | `HMAC_SECRET` 若为低熵人工字符串,可被离线字典攻击反推手机号。请使用高熵随机值 |
| 缓存 TTL 与 DB 记录不一致 | 验证码在缓存中 TTL 到期即失效,但数据库记录永久保留,注意合规要求 |

> 排障提示:写库失败时(如 MySQL 中断),**验证码仍会留在内存缓存中可用**,`/api/v1/otp` 依然返回 `success`,而数据库中并无该条记录。"验证码可用"与"记录已留存"是两件独立的事,详见 `docs/LOGGING.md` 第 8 节。
