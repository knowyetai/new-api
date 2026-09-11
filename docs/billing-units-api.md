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
{"provider_id": 1, "issuer": "https://sso.example.com", "subject": "immutable-sso-subject", "display_name": "张三"}
```

响应（HTTP 200，`Cache-Control: no-store`）：

```json
{"success": true, "data": {"user_id": 12, "token_id": 34, "key": "sk-<personal-model-token>"}}
```

- `display_name` 为可选显示名，来自受信任平台已经验证的 SSO `name`，不能取浏览器自行提交的任意资料。首次开户写入；后续断言仅在非空且有变化时更新。去除首尾空白后最多保留 20 个 Unicode 字符；省略或全空白保留原值，首次无值沿用 `SSO user`。
- 显示名允许重复，绝不用于查找或合并账户；原有 username、用户 ID、身份绑定、模型 Token、权限、余额和账单关系不变。不新增表或唯一约束。
- 已有模型 Token 绑定的账户直接使用该 OAuth 提供商登录时，也从验证后的用户资料更新显示名。普通 OAuth 账户（`model_token_id=0`）沿用原有行为。同步发生于登录/获取模型凭证时，不运行定时同步；SSO 改名后旧页面可能需重新登录或刷新。
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

## 显示名同步发布与回退

先发布支持可选 `display_name` 的 new-api，再发布 known-engine Gateway/Worker。旧调用方省略字段仍可使用，不需要数据库升级或全量重开户。回退代码只停止后续同步，保留已经写入的显示名，不涉及额度或账单回滚。Casdoor 与 aibrain 无须改代码，OAuth `display_name_field` 应映射为 `name`。

安全依据：OWASP Authentication、Session Management、OAuth2 Cheat Sheets；保留既有凭证验证、身份绑定、会话和权限判断。显示名是展示资料，不参与认证授权。

2026-09-10 本机验证：显示名与计费回归在 SQLite、MySQL、PostgreSQL 全部通过；实际 OAuth 登录处理器返回新显示名，空值重登保留原值。Go 后端构建通过。known-engine 的 10 项测试全部通过（98.23 秒），包含真实 Casdoor 浏览器登录、aibrain、Gateway/Worker、new-api 与 mock 推理链路，核对自动开户显示名以及个人/单元扣费、退款、Token 稳定性。本次验证未部署生产环境。

## 2026-09-10 显示名同步预发部署验收

- new-api 功能提交 `ed3c64868` 已推送 `test`；SSO 主机的共享网关运行源码构建镜像 `knowyet-new-api:ed3c64868`（镜像 `85807e3a4550`），沿用未修改的已构建前端。旧镜像 `knowyet-new-api:billing-units-20260910` 和部署配置备份保留，可切回；无数据库迁移。
- known-engine 显示名功能提交 `d16c82c`，随后将测试夹具修正一起推送 `test`，GitHub 自动部署已执行。使用 `docker-compose.test.yml` 加 `.env.sso` / `docker-compose.sso.yml`，Gateway、Worker 的运行文件 SHA 与提交代码一致，Casdoor 模式和模型网关开关正常。aibrain 源码未变。
- 临时将测试账号 peilong 的 Casdoor 显示名改为“佩龙预发验证”。直接 new-api 浏览器登录、aibrain 登录换取 known-engine 凭证、Agent 获取模型 Token 均验证显示名一致。完成后恢复为 peilong 并重登验证。用户 ID 2 / Token ID 1 / username peilong 不变。
- new-api 部署后接口验证：非空改名、空白/缺失保留、20 个中文字符截断、模型 Token 无权调用后台身份断言；显示名变化不改变其他账户字段。首次开户、同名不同账户、普通 OAuth 不覆盖及错误身份拒绝由本机自动化覆盖。
- 预发金融审查测试知识库共执行 6 次真实 Agent → new-api → DeepSeek 任务：个人付款成功、加入单元付款成功、单元停用拒绝、单元恢复后成功、个人 Token 停用拒绝、退出单元且 Token 恢复后个人付款成功。四条消费日志输入合计 19829 / 输出 177 token，个人扣 2741 quota，单元扣 2740 quota，总计 5481；余额变动与消费日志一致，拒绝请求无新增消费。测试结束时个人余额 43236，测试付款账户余额 44607，个人 Token 已启用，已退出测试单元且单元停用。
- 本机回归：模型网关/真实 SSO/多用户共享计费完整链路 10 项；SSO、仓库单写、崩溃审计、恢复、webhook/部署配置 106 项；独立真实 Gitea 的 4 项另行全部通过（首次未启用该实例而跳过）；aibrain 认证/会话/刷新 39 项；真实 SDK + mock 推理 Worker 协议 5 项。共 164 项 Python 测试通过。new-api SQLite/MySQL/PostgreSQL 计费与显示名测试、OAuth 登录响应测试及后端构建通过。
- 回归中修正两处测试夹具问题：SQLite 恢复 CLI 改用 ACH_HOME_ROOT 定位测试库；旧 Worker 测试先持久化任务再入队，并使用独立 Redis/mock 上游，避免绕过现有 SQL 执行认领流程。没有为测试放宽业务权限。
- 边界：共享生产服务未进行真实供应商故障注入、真实支付扣款、运行中 Worker 强杀或 NAS 并发破坏测试；相应可重复的退款/崩溃/写冲突场景在本机隔离环境回归。以上不代表整个产品所有无关测试或真实支付链均已验收。

## 自助团队计费（2026-09-11 本地验收）

普通用户通过 `/expenses` 费用中心创建团队、按准确用户名邀请、确认加入、退出、查看本人消费；负责人可管理成员、暂停团队、查看成员用量并用支付宝充值。采用 new-api Go 管理 API 与 React 页面扩展，复用原生钱包、订单、日志；任务 JavaScript 插件不承担同步权限与计费事务。knowengine/aibrain 不维护第二份团队余额，可链接此费用中心。

### 规则与接口

- 每人同时只有一个计费团队，可管理多个团队（最多 20 个）。首次建团队自动加入；已有团队时新建不会切换。团队专用 User 钱包初始额度为零，不执行注册赠送，不生成登录密码、SSO 绑定或推理 Token。
- 邀请绑定稳定 provider + subject；SSO 用户尚未在 new-api 开户也可被邀请。接受后建立成员关系，用户仍使用自己的 Token。邀请 7 天过期，每团队最多 500 条未过期待处理邀请。邀请查询用身份摘要避免 SQL 大小写规则造成身份混淆。
- 加入/切换提交 `expected_unit_id`，个人钱包为 0；用户事务锁下核验，防止旧页面覆盖新选择。普通负责人不能绕过邀请直接添加成员，旧 PUT 添加成员接口仅平台管理员可调用。
- 团队优先扣费。个人余额兜底缺省关闭，须本人确认并且平台开关开启；仅团队预扣明确额度不足且 Token 预扣已退款时可触发。团队暂停、权限、数据库及 Token 额度错误不会触发。请求开始后付款方固定，结算和退款不跟随成员变更。
- 团队充值订单固定收款 User、团队、操作人；切换团队不改变已有订单。回调验签、金额校验与事务内幂等入账。支付页面跳转不作为到账证明。

以下均使用 new-api 当前用户认证，操作者从服务端身份取得，不能由客户端指定 owner/payer：

| API | 参数/用途 | 权限 |
| --- | --- | --- |
| `POST /api/billing-units/self-service` | `{name,request_key}`，创建幂等 | 登录用户 |
| `GET /api/billing-units/self` | 当前团队、个人余额、兜底与功能开关 | 本人 |
| `DELETE /api/billing-units/self/membership` | `{expected_unit_id}`，退出 | 本人 |
| `PUT /api/billing-units/self/preference` | `{personal_billing_fallback:boolean}` | 本人 |
| `POST /api/billing-units/:id/select` | `{expected_unit_id}`，选择自己创建的团队 | 创建者 |
| `POST /api/billing-units/:id/invitations` | `{username}` | 负责人 |
| `GET /api/billing-units/:id/invitations` | 发出的邀请 | 负责人 |
| `GET /api/billing-unit-invitations/self` | 收到的邀请 | 本人 |
| `POST /api/billing-unit-invitations/:id/accept` | `{expected_unit_id}` | 被邀请者 |
| `POST /api/billing-unit-invitations/:id/reject` | 拒绝 | 被邀请者 |
| `POST /api/billing-unit-invitations/:id/revoke` | 撤销 | 负责人 |
| `GET /api/billing-units/:id/usage-summary` | start/end 秒时间戳；默认 UTC 当月，最大 366 天 | 负责人 |
| `POST /api/billing-units/:id/topups/quote` | `{amount}`，计算支付金额 | 负责人 |
| `POST /api/billing-units/:id/topups` | `{amount,payment_method:"alipay"}`，原生 Epay 响应 | 负责人 |
| `GET /api/billing-units/:id/topups` | 充值历史 | 负责人 |

列表使用 `p/page_size`。余额、成员移除及启停复用已有团队 API。普通成员只能查看本人账单，不能读取团队其他用户消费。

### 配置与升级回滚

三项环境开关默认关闭：`BILLING_TEAM_SELF_SERVICE_ENABLED`、`BILLING_TEAM_PERSONAL_FALLBACK_ENABLED`、`BILLING_TEAM_TOPUP_ENABLED`，启用值为 `true`，所有 new-api 实例保持一致。支付仍需原生 Epay 配置。

SSO 目录配置：`BILLING_TEAM_CASDOOR_PROVIDER_ID`、`BILLING_TEAM_CASDOOR_ORGANIZATION`、`BILLING_TEAM_DIRECTORY_CLIENT_ID`、`BILLING_TEAM_DIRECTORY_CLIENT_SECRET`。目录凭据只在后端，需目标组织精确查询用户权限；生产 HTTPS，本地允许 localhost HTTP。未配置 provider 时仅可邀请已有本地用户；配置后目录失败不会回退到同名本地账号。

迁移新增 `billing_unit_invitations`；`billing_units` 增加 nullable `creation_key` 和 `(owner_user_id,creation_key)` 唯一索引，历史值 NULL；`top_ups` 增加默认 0 的 `billing_unit_id/operator_user_id`。User.setting JSON 增加可选兜底字段，缺省 false。旧钱包、Token、账单及成员关系保留。

先备份关系库，关闭功能开关发布并迁移；验证历史行、唯一性和待支付订单后再启用。回滚优先关闭新入口/兜底开关，不删除新增表列或钱包。关闭团队充值入口不关闭原生支付回调，已有订单仍需处理。可回退到此前支持 billing_units 的版本继续已有团队扣费，不可直接回退到完全不支持团队的版本接流量。产生新消费后禁止恢复发布前数据库快照覆盖账单，金额纠正须对账补偿。

### 本轮验证与资源约束

用例覆盖创建零赠送与幂等、权限隔离、邀请接受/拒绝/撤销/过期/重放、稳定 SSO 身份、并发切换、团队与个人扣费、暂停不兜底、固定退款、充值失败/篡改/重复回调、SQL 迁移及页面操作。后端要求 SQLite/MySQL/PostgreSQL 真实引擎；完整链路要求真实 SSO → aibrain → knowengine → new-api → mock 推理。以下记录仅代表本轮列明的本地场景，不代表真实支付或生产环境已经验收。

2026-09-11：机器重启后 `/tmp` 的工具和测试库丢失；内核日志显示持续内存压力，尚不能单凭该日志判定重启直接原因。改用持久目录 `/home/ubuntu/code/.local/team-validation` 存放工具、缓存、日志与临时验证资源（权限 0700，不纳入 Git）。重型构建串行，Go `-p=1/GOMAXPROCS=1/GOMEMLIMIT=400MiB`，Node 堆 768 MiB、Rayon 单线程；监控可用内存与进程组 RSS，低可用内存/高交换增量时中止任务。不得并行恢复 Casdoor 编译、new-api 编译及前端构建。

参考 OWASP Authentication、Session Management、Authorization Cheat Sheets，采用服务端授权、稳定主体绑定、邀请有效期、支付幂等及无凭据审计；不声明已经完成全面 ASVS 验证。

本轮最终数据库回归：`go test ./controller -run '^Test(BillingTeamSelfService|BillingUnitAPIAndSettlement|IntegrationDisplayNameOAuthLogin)$' -count=1 -v` 通过（7.504 秒）。实际引擎 SQLite 3.50.4、MySQL 8.0.46、PostgreSQL 16.15，使用独立主库/日志库，覆盖新库、代表性旧表升级及重复迁移、原有计费/显示名行为和上述邀请、扣费、支付边界。随后重新构建最新后端通过；执行组峰值 RSS 954 MiB、匿名内存 432 MiB。`relaykit` 已单独执行 `GOWORK=off go build ./...` 通过。

本轮发现并修复两类问题：支付测试夹具未执行原生数据库方言初始化，改为调用 InitDB；真实费用中心成员列表因 GORM 根据展示 DTO 推导出不存在的列而返回 503，增加先失败的接口回归后，改为显式读取成员关系列，再查询用户展示资料。三引擎及真实浏览器均已重新验证该修复。

真实本机联调分别通过原有管理员建单元和普通用户自助建团队两条链路：Casdoor → aibrain BFF → known-engine Gateway/AgentWorker/DeepAgentExecutor → new-api → mock 推理。自助场景使用普通用户权限创建团队，经真实 Casdoor 目录按用户名邀请，另一用户接受后调用模型。每条链路执行 8 次 Agent 任务，4 次成功、4 次预期拒绝/上游失败；每次成功输入 100、输出 20 token，扣 140 quota；团队三次合计扣 420（1000→580），退出后个人一次扣 140（500→360）。验证独立用户 Token、消费者/付款方逐条日志、零余额拒绝、暂停拒绝、退出、上游失败退款。充值和余额通过本地测试夹具准备，不使用生产额度。

实际构建产物的 Chromium 页面验证通过：普通用户登录、创建零余额团队、查看成员姓名、按用户名邀请尚未自动开户的 SSO 用户、取消个人兜底确认、退出团队、390px 移动视口无横向溢出。浏览器同时断言团队接口没有 HTTP 错误，并核对数据库成员和邀请状态。截图与测试记录保存在本机验证目录。

费用中心 RTL 7 个交互用例通过，覆盖创建校验/失败重试幂等、邀请、切换团队确认、关闭功能后的禁用行为，以及支付宝充值确认收款团队/报价后才提交支付、报价失败不发起支付。后端使用 mock Epay 测试真实下单处理器和签名回调：失败、签名/金额篡改、成功、重复回调、成员变更后仍固定收款方。没有进行真实支付宝付款。

最终应用和 Node 配置类型检查均通过（分别执行原生 `tsgo --singleThreaded -p tsconfig.app.json`、`-p tsconfig.node.json`），受影响文件 lint、格式及 `git diff --check` 通过。费用中心用例命令为 `vitest run --maxWorkers=1 src/features/expenses/__tests__/team-management.test.tsx`。前端生产构建通过（28.6 秒）。默认构建曾在产物体积统计阶段超出本机阈值而被主动中止；`BUILD_LOW_MEMORY=true` 仅关闭 Rsbuild 的 printFileSize 报告，保留生产压缩和产物逻辑。源码、测试及运行结果位于功能分支，本轮尚未提交、合入 test 或部署新功能。

资源保护：所有重任务使用文件锁串行，每秒记录可用内存、交换空间、进程组 RSS/匿名内存、负载、磁盘。可用内存低于 650 MiB、交换空间较任务开始增长超过 384 MiB，或进程组匿名内存超过 1200 MiB 时终止整个任务组。RSS 包含可回收文件映射，记录但不单独用作内存耗尽判断。类型检查直接运行原生 `tsgo --singleThreaded`，避免额外包装进程。应用全量检查超过默认匿名内存上限，单独授予最高 1600 MiB 的任务预算，同时将系统可用内存保护线提高至 900 MiB，交换空间增量上限仍为 384 MiB；本次正常完成，匿名内存峰值 1346 MiB、RSS 1362 MiB，采样最低可用内存约 1413 MiB，交换空间未增长。Go 内存参数是软限制，仍以进程组实测和保护机制为准。不把主动中止的检查记为通过。

### 2026-09-11 原有功能补充回归

本次在 `feature/team-self-service` 最新未提交源码及 known-engine/aibrain 的 `new_api` 工作区执行。去重后 302 项 Python 用例通过；new-api `service` 包 216 个顶层测试通过（子用例不重复计数），无未解决失败或跳过。未调整业务实现以通过测试。

| 模块 | 结果 | 验证范围 |
| --- | --- | --- |
| aibrain `backend/tests/module tests/unit` | 88 通过 | 登录、注册、会话、Casdoor 回调、Gateway 刷新、接口权限、项目/知识库文件代理、配置与部署脚本 |
| known-engine 写锁及 webhook 回归 | 80 通过 | 真实 MySQL 单写互斥、commit=false、只读/可写任务、取消、独立进程崩溃、失联核查、人工恢复、Git 安全同步、日志脱敏、部署门禁 |
| known-engine 认证与模型凭证 | 100 个独立用例通过 | JWT、本地登录、SSO Redis 状态、身份迁移、存储身份保留、Casdoor API、模型凭证身份绑定、显示名、原环境变量路径与模型路由 |
| known-engine 知识库/附件 HTTP | 25 通过 | 知识库增删改查、图谱实体/关系、二进制上传下载保真、目录过滤、路径穿越拒绝、会话附件及多轮附件声明 |
| 真实 Gitea 1.24.6 | 4 通过 | 并发初始化复用、两级去重/保留不同配置/恢复、分页、真实 push 签名投递及工作区更新 |
| Worker 真实 SDK + mock 上游 | 5 通过 | 入队执行、事件、session_id、workspace 清理、输出文件清单、非法任务错误 |
| new-api `go test ./service -count=1 -json` | 216 个顶层测试通过 | 认证 Token/会话、渠道选择/亲和性、HTTP 传输、计费、配额、退款等现有服务层用例 |

写锁使用 `WRITER_TEST_MYSQL_URL` 指向一次性本机 `single_writer_test` 库，未使用 SQLite fallback。认证组初次 97 通过、3 项因 MySQL 配置缺失跳过；补配 `MODEL_GATEWAY_TEST_MYSQL_URL` 后凭证文件 7 项全部通过，其中 4 项重复运行，故认证组去重计 100。Gitea 初次因默认 SSH 文件目录不可写未启动，配置 `SSH_ROOT_PATH` 到独立测试目录后健康检查和 4 项用例均通过。new-api 服务层补装本地 PostgreSQL 客户端后运行完成。以上属于测试依赖/配置问题，未修改生产路径或放宽权限。

new-api 固定价格矩阵另设专用 `TEST_FIXED_MYSQL_DSN` / `TEST_FIXED_POSTGRES_DSN`，真实 SQLite 3.50.4、MySQL 8.0.46、PostgreSQL 16.15 全部通过；覆盖 missing/zero usage、流式/音频计费、预扣结算、工具附加费、失败精确退款和额度不足拒绝，结束后删除专用数据库。

复现入口：本机私有目录 `/home/ubuntu/code/.local/team-validation/run-regression.py` 按组调用 pytest，`run-service-regression.py` 调用 Go 服务层测试；统一由 `run-limited.py` 串行执行。Python 的测试文件范围为 `tests/test_resource_write_cases.py`、`test_resource_write_audit.py`、`test_webhook_safe_sync.py`、`test_gitea_sync_result.py`、`test_git_push_log_privacy.py`、`L0_unit/test_deploy_config.py`；认证组为 `L0_unit/test_casdoor.py`、`test_jwt_utils.py`、`test_local_provider.py`、`test_sso_redis_state.py`、`L1_sqlite/test_casdoor_migration.py`、`L2_storage/test_sso_storage.py`、`L4_gateway/test_casdoor_api.py`、`test_auth_api.py`、`tests/test_model_gateway_credentials.py`、`test_model_chat_routing.py`；知识库组为 `L4_gateway/test_http_kms.py`、`test_kb_file_download.py`、`test_session_files.py`；真实依赖组为 `tests/test_gitea_webhook_management.py`、`L5_worker/test_agent_runtime.py`。执行时保留完整父目录前缀；私有 JUnit XML 和 Go JSON 记录在同一验证目录，不包含于 Git 文档。

本轮执行组峰值 RSS 744 MiB、匿名内存 435 MiB，未触发内存保护。仍未覆盖真实 NAS/FUSE 故障、真实支付扣款、真实供应商故障及整个仓库所有无关测试；这些结果不能表述为生产验收完成。本轮不提交或部署代码。
