# 计费单元 API 与接入说明

本模块基于 `bdef11750` 开发。此模块取代旧“付款账户下的成员 Token + knowengine 同步消费配置”方向；known-engine 仅保留独立身份接入，旧资源包同步实验已删除。线上没有旧账单，不需要迁移实验余额或共享 Token。

## 边界与数据

每个用户保持独立 native User 和个人 Token。`billing_units` 记录名称、负责人、专用 native 付款账户；`billing_unit_members` 记录用户所属单元，用户 ID 唯一。单元付款账户必须为普通启用账户、无 Token、无自定义 OAuth 绑定、无历史消费，且不能为成员或负责人本人。由平台管理员通过原生创建用户 API 创建专用账户，再创建单元；其余额/订阅仍由 native 模型维护。负责人不自动成为消费成员，需显式加入。

此轮支持 `/v1/chat/completions`（含流式）。已加入计费单元的用户访问其他 Token 鉴权推理入口被拒绝，不回退个人扣费；Playground 不开放单元消费。Token 的用户归属、限额、模型权限、路由分组保持原义；付款偏好及钱包/订阅来自付款账户。价格沿用原有模型和调用分组配置，不额外实现单元专属价格。单元不足不回退成员个人钱包。负责人身份禁用不等同于单元冻结，单元冻结直接操作付款账户状态。

付款方在请求鉴权时解析并固定，成员中途退出不改变该次请求结算或退款。未加入单元的后续调用使用个人账户。消费者与付款账户分别统计使用/支出，但只对付款账户扣款一次；日志新增 billing_user_id、billing_unit_id，仍保留原 user_id、token_id。

## 公共 API

使用 new-api 原生用户会话 Bearer 或个人访问凭证；推理接口仍使用个人模型 Token。管理凭证不能交给模型客户端。所有接口复用 UserAuth，服务端检查平台管理员或对应负责人，不依赖前端角色。

| 方法 | 路径 | 权限/语义 |
|---|---|---|
| POST | `/api/billing-units` | 平台管理员创建；body: name、owner_user_id、payer_user_id |
| GET | `/api/billing-units` | 管理员看全部，负责人只看自己的；p/page_size 分页 |
| GET | `/api/billing-units/self` | 本人当前 personal/unit 付款归属；不公开账户凭证 |
| GET | `/api/billing-units/:id` | 负责人/管理员查询单元、启停、balance_quota、used_quota |
| PATCH | `/api/billing-units/:id` | 负责人/管理员启停；body: enabled 布尔值 |
| GET | `/api/billing-units/:id/members` | 负责人/管理员查询成员，分页 |
| PUT | `/api/billing-units/:id/members/:user_id` | 添加成员；同单元重复添加幂等；已在其他单元返回 409 |
| DELETE | `/api/billing-units/:id/members/:user_id` | 移除成员；重复移除幂等，不修改历史账单或 Token 所有者 |
| GET | `/api/billing-units/:id/usage` | 负责人/管理员查询由此单元支付的分人消费明细，分页；不返回成员个人付款历史 |

成功格式沿用 `{ "success": true, "data": ... }`。无权限访问特定单元返回 404，非管理员创建返回 403，无效 ID/请求返回 400，成员冲突/重复绑定付款账户返回 409。数据库故障不返回内部 SQL。启停写入后发生错误返回结果不确定，查询 GET 确认，不自动重放。

## 响应字段

所有 ID 均为 new-api 内部整数 ID，不能直接传 Casdoor subject 或 known-engine 的字符串 user_id。

- 创建：`data = {id, name, owner_user_id, payer_user_id}`；HTTP 200。
- 单元列表：`data = {items: [单元对象], total}`。
- 单元详情：`data = {unit: 单元对象, enabled, balance_quota, used_quota}`。
- 本人归属：个人为 `{payment_mode: "personal", billing_unit_id: null}`；单元成员为 `{payment_mode: "unit", billing_unit_id, name}`。
- 成员列表：`data = {items: [{user_id, billing_unit_id}], total}`。
- 加入/移除：`data = null`；启停：`data = {enabled}`。
- 消费明细：`data = {items: [{user_id, token_id, quota, prompt_tokens, completion_tokens, created_at, request_id}], total}`；`created_at` 为 Unix 秒，`quota` 为系统内部额度单位。

`p` 从 1 开始；接口提供分页明细，没有在此版本提供按用户聚合报表。调用方需检查 HTTP 状态和 `success`，不能把 HTTP 200 当成所有原生接口均成功。

## 可操作顺序

先使用已有 `/api/user/` 创建真实业务用户及专用付款账户。下列变量由操作者安全配置，不在文档填写实际凭证。`ADMIN_AUTH`、`OWNER_AUTH` 是不同角色的 native 管理访问凭证，`ALICE_MODEL_TOKEN` 是 A 自己创建的模型 Token。

```bash
# 创建计费单元，记下响应 data.id 为 UNIT_ID。
curl -X POST "$BASE/api/billing-units" \
  -H "Authorization: Bearer $ADMIN_AUTH" -H 'Content-Type: application/json' \
  -d "{\"name\":\"研发组\",\"owner_user_id\":$OWNER_ID,\"payer_user_id\":$PAYER_ID}"

# 本地验证资金注入复用原生管理员额度接口，value 是 quota 整数单位，不是人民币。
curl -X POST "$BASE/api/user/manage" \
  -H "Authorization: Bearer $ADMIN_AUTH" -H 'Content-Type: application/json' \
  -d "{\"id\":$PAYER_ID,\"action\":\"add_quota\",\"mode\":\"add\",\"value\":10000}"

curl -X PUT "$BASE/api/billing-units/$UNIT_ID/members/$ALICE_ID" \
  -H "Authorization: Bearer $OWNER_AUTH"

curl "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $ALICE_MODEL_TOKEN" -H 'Content-Type: application/json' \
  -d '{"model":"mock-fixed","messages":[{"role":"user","content":"hello"}],"max_tokens":20}'

curl "$BASE/api/billing-units/$UNIT_ID" -H "Authorization: Bearer $OWNER_AUTH"
curl "$BASE/api/billing-units/$UNIT_ID/usage?p=1&page_size=20" -H "Authorization: Bearer $OWNER_AUTH"

curl -X PATCH "$BASE/api/billing-units/$UNIT_ID" \
  -H "Authorization: Bearer $OWNER_AUTH" -H 'Content-Type: application/json' -d '{"enabled":false}'

curl -X DELETE "$BASE/api/billing-units/$UNIT_ID/members/$ALICE_ID" \
  -H "Authorization: Bearer $OWNER_AUTH"
```

成员本人仍使用原生 Token 管理接口及个人消费日志接口。此轮资金注入为管理员测试/运营接口，不是支付宝充值；负责人在线购买单元订阅/充值的支付入口尚未交付，不能用管理员凭证下发前端代替。已通过可信平台身份接口打通 Casdoor 身份与 knowengine 个人 Token 调用；new-api 用户在首次模型任务时按需开通。

## 验证与升级

- 真正运行原生 new-api 两个进程，共享 MySQL/Redis；上游为本地 mock。通过管理 HTTP 创建全部用户、创建单元、注入额度、加入/退出、查询和启停；消费 Token 由各自用户创建。
- A/B 个人余额均为 0，分别消费并按用户记账，共扣单元。A 请求在途退出仍扣原单元，之后个人余额为零拒绝。B 请求失败并在途退出，预扣仍退原单元。三个成功请求累计 A=280、B=140、付款账户使用=420、剩余=580。跨单元权限拒绝、未适配端点拒绝、停用后双实例拒绝。
- Go 定向矩阵覆盖 SQLite 3.50.4、MySQL 8.0.46、PostgreSQL 16.15。每种引擎建立独立主库与日志库，新增表重复迁移、代表性历史日志扩列且原值保留、唯一关系、角色权限、固定付款方、幂等结算与退款、付款方订阅消耗等通过。不是生产数据迁移演练，也未验证 ClickHouse 日志库。
- 服务层既有 Billing/Funding/Quota 测试回归；未修改 relaykit 公共协议或新增外部模型调用。
- 权限设计沿用之前已阅读的 OWASP Authentication / Session Management 指引：服务端逐操作权限检查、推理凭证与管理凭证分离；创建、成员变更、启停写 native 审计，不包含凭证。此记录不宣称完整 ASVS 合规认证。

复现命令：

```bash
# new-api-billing-units；DSN 必须指向可创建临时库的回环实例。
TEST_MYSQL_DSN='...' TEST_POSTGRES_DSN='...' go test ./controller -run TestBillingUnitAPIAndSettlement -count=1 -v
go test ./service -run 'Billing|Funding|Quota' -count=1

# known-engine-new-api；MODEL_GATEWAY_TEST_NEW_API_BINARY 指向此 feature 分支构建的二进制。
MODEL_GATEWAY_TEST_MYSQL_URL='...' MODEL_GATEWAY_TEST_NEW_API_BINARY='...' \
  python -m pytest tests/test_native_billing_units_live.py -q --tb=line --show-capture=no
```

启动时 native AutoMigrate 新增两表及日志两个字段。旧日志付款方为 0 时按原 user_id 解读；不要按当前成员关系回填历史。首次上线无需迁移旧计费数据；开发期共享账户 Token 不属于线上数据。

回退前暂停推理入口、排空请求；不删除余额、订阅或日志。不能直接把已加入单元用户的流量交给旧二进制，否则旧版本会按个人账户扣费。关闭新功能时，先暂停并排空任务，再在所有 known-engine 实例关闭网关开关，恢复原模型配置；保留 new-api 已产生的账单与余额。异步任务的持久化付款方与恢复结算尚未实现，本轮通过拒绝这些单元请求避免误扣。

最终记录：服务层 63 个顶层测试全部通过，无 skip/fail；最新三引擎 API/结算矩阵全部通过（5.697 秒）；真实双实例 HTTP 闭环 1 passed（6.76 秒）。`git diff --check` 通过。构建验证使用 feature 源码快照，临时入口仅监听 127.0.0.1，嵌入占位 HTML，仅验证后端 API，不宣称前端页面完成。

## 可信平台身份接口

`POST /api/integrations/model-credentials`：仅 RootAuth，供已完成 SSO 验证的受信任平台后端调用。它不是公开的“传 subject 登录”接口，不接受普通模型 Token，不接受调用方指定 native 用户、付款账户、额度或角色。root 管理凭证仅放服务器配置，严禁发给浏览器、业务用户或作为模型调用的 Authorization。

请求：

```json
{"provider_id": 1, "issuer": "https://sso.example.com", "subject": "immutable-sso-subject"}
```

响应（HTTP 200，`Cache-Control: no-store`）：

```json
{"success": true, "data": {"user_id": 12, "token_id": 34, "key": "sk-<personal-model-token>"}}
```

- `provider_id` 是已通过原生 `/api/custom-oauth-provider/` 配置的自定义 OAuth 提供商 ID。提供商必须启用，`user_id_field` 为 `sub`，`well_known` 精确对应 issuer 后加 `/.well-known/openid-configuration`。当前断言接口不支持配置了 `access_policy` 的提供商，会拒绝而不绕过该策略。
- 调用平台必须先验签、校验 issuer/audience/会话与用户权限，再从可信身份绑定读取 subject。此接口信任 root 后端断言，本身不接收或二次校验 Casdoor access_token；不可直接代理任意客户端提交的 subject。
- 按原生 `user_oauth_bindings(provider_id, provider_user_id)` 查找用户；不存在时，在同一事务内创建普通 native User、绑定和个人模型 Token，不按姓名或邮箱自动认领旧账户。新用户沿用原生 `QuotaForNewUser` 配置；正式计费可设为 0。
- 绑定增加 `model_token_id`（默认 0）来记录该平台个人 Token。首次生成无限 Token 额度（钱包或订阅依然必须有余额）；重复/并发调用返回同一 Token，不重置原生限额、用量、状态或模型权限。
- 原生用户禁用、Token 禁用/删除/过期、身份冲突、提供商不匹配等返回 409 `model identity unavailable`；缺少字段返回 400；无 root 权限由鉴权层拒绝。没有自动重建、恢复 Token 的兜底。需要显式运维处理后重试。
- 原生账户、绑定和 Token 写入在一个数据库事务里。响应丢失后再次调用仍返回已提交的同一身份；没有跨系统“已创建/待同步”状态表。既有用户按用户行锁并发，只有首次开户按提供商行锁串行，避免重复开户。
- 审计只记录提供商、用户和 Token ID；响应包含真实模型密钥，调用方禁止记录响应正文。

```bash
curl -X POST "$BASE/api/integrations/model-credentials" \
  -H "Authorization: Bearer $ROOT_AUTH" -H 'Content-Type: application/json' \
  -d '{"provider_id":1,"issuer":"https://sso.example.com","subject":"immutable-sso-subject"}'
```

## 完整链路最新验收

已执行真实本机 Casdoor → aibrain BFF → known-engine Gateway/AgentWorker/DeepAgentExecutor → new-api → mock 推理。认证未 mock；浏览器只用于真实登录，业务操作走实际 BFF API。8 次任务中 4 次成功，每次上游返回输入 100/output 20 token、扣 140 quota；单元支付三次共 420（余额 1000→580），成员退出并通过管理 API 注入个人测试额度后支付一次 140（500→360）。个人零余额、单元停用、退出后个人无余额拒绝，上游失败预扣退回。消费者与付款方日志逐条核对，两个消费者只有两个独立 Token；known-engine 旧资源包/网关绑定表为空。

三引擎测试通过（SQLite/MySQL/PostgreSQL，8.300 秒），包括历史 OAuth 绑定新增列保留及重复迁移、独立日志库、Token 生命周期和旧计费场景。双实例 HTTP 计费回归通过；完整业务任务 E2E 通过（107.64 秒）。仅测试后端业务链路，未构建业务前端或实际支付，未验证真实模型供应商缓存命中。

本次修复双实例首次获取凭证时 MySQL 可重复读快照不可见的问题：在用户锁下读取最新绑定与 Token，第二实例不会因旧快照误判 Token 不存在。

升级时随 native AutoMigrate 增加 `user_oauth_bindings.model_token_id`；默认 0 不影响旧 OAuth 身份。回滚保留该字段与新建的真实用户/Token/消费记录。旧共享付款账户只是未上线实验，相关实现已删除，不实施历史余额迁移。此接口可重复调用以核对绑定，不用于转移余额或重写账单。
