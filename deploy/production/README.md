# SSO 主机上的独立模型网关

目标主机：8.145.51.132，目录 `/opt/knowyet-model-gateway`。独立 Compose 项目 `knowyet-model-gateway`，不修改 Casdoor 服务、数据库或端口。应用只绑定 127.0.0.1:3000，Redis 不发布宿主机端口；通过独立 Nginx 域名提供 HTTPS。

初次部署使用 feature/billing-units 工作区源码快照，历史 `source-manifest.json` 记录当时的基线、分支及归档 SHA256。当前部署流程以明确指定的 Git 提交为准，见下文。所有密钥在服务器生成，runtime.env 和 .env 为 0600，不入库。

## 部署条件

- 公网 api.knowyet.com → 8.145.51.132；内网 api-inner.knowyet.com → 172.16.0.65。公网和内网证书均已签发；内网使用手工 DNS TXT 验证，目前不能自动续期。
- 用户确认使用现有 RDS 中的独立数据库 zhicui_api，并复用 Casdoor 现有数据库账号凭证。连接信息在服务器内读取，不写入版本库；不操作 Casdoor 的表。
- runtime.env 中 SQL_DSN 已指向 zhicui_api。SESSION_SECRET / CRYPTO_SECRET 应保持稳定，并纳入密钥备份。

## 按提交部署（当前脚本）

在本地仓库运行，提交必须已推送到 GitHub，且在本地可解析。必须明确传入提交 hash（支持 7～40 位），不接受分支名或标签：

```bash
bash deploy/production/deploy.sh 91c7c7059
# 或指定目标：
bash deploy/production/deploy.sh 91c7c7059 root@8.145.51.132 /opt/knowyet-model-gateway
```

服务器需事先存在源码仓库；若尚未准备，首次在服务器执行：

```bash
cd /opt/knowyet-model-gateway
git clone git@github.com:knowyetai/new-api.git new-api
```

服务器自身必须有访问 GitHub 仓库的权限。origin 支持上述 SSH 地址或对应 HTTPS 地址。脚本不会配置凭据或修改 origin。

执行流程：

1. 本地将 hash 解析为完整提交 ID，仅通过 SSH 发送部署命令，不再打包或上传源码。
2. 服务器取得现有部署锁，检查配置和 Redis。
3. 检查 `/opt/knowyet-model-gateway/new-api` 为干净仓库；存在修改、未跟踪或忽略文件时停止，避免将非指定提交的内容带入镜像，不自动 reset/clean。
4. 在该仓库执行 `git fetch --no-tags origin <完整 hash>` 和 detached HEAD 检出，核对实际提交。
5. 回到部署根目录，以 `new-api` 为构建上下文，用 `Dockerfile.cached` 和 `DOCKER_BUILDKIT=1 docker build --progress=plain --pull=false` 构建。服务器必须安装 Docker Buildx；构建日志显示每步耗时和缓存命中，脚本输出构建耗时与远端总耗时。
6. 更新 NEW_API_IMAGE，仅重建 new-api，等待健康检查；成功后将完整 hash 写入部署根目录的 `.deployment-successful-commit`。失败直接退出，不自动回滚，也不阻止下一次人工部署。

本地未提交修改不会部署，也不会阻止部署另一个明确指定的已推送提交。服务器源码目录会在后续部署时切换到新的提交，不再每次创建 build-source 目录；版本记录保存在 Git HEAD、镜像 revision 标签及上次成功提交文件中。失败不会覆盖上次成功提交。首次使用简化脚本前尚无成功记录时，请人工记录当前运行镜像的 revision 标签。

部署失败后，由人查看日志，再用同一个脚本传入上次成功的 hash：

```bash
bash deploy/production/deploy.sh <上次成功的提交hash>
```

这会重新获取、检出和构建该版本，再启动容器，不需要单独的回滚脚本。已删除 `deploy_with_rollback.sh` 和 `rollback.sh`，不再生成回滚标签、部署 JSON、配置备份或切换状态标记。保留部署锁，避免两个人同时发布。旧流程留下的记录文件不参与新流程，也不会被自动删除。

按旧 hash 部署只切换应用代码，不恢复数据库或 runtime.env 等运行配置；如果失败版本已执行不兼容的数据库迁移，需先人工处理兼容性，重新部署旧代码不能撤销迁移。

Compose 启动参数、数据库及 Redis 操作范围未改变。已有历史源码目录和回滚镜像不会自动删除。

## 构建与运行

生产部署使用 `Dockerfile.cached`，基础镜像为 node:20.20.1-alpine / golang:1.25.8-alpine / alpine，Bun 固定 1.4.0，依赖沿用锁文件。使用 Docker 默认 builder 的 BuildKit，以复用本机镜像和持久缓存；不创建独立 builder。`--pull=false` 不强制更新基础镜像，但缺失镜像仍需要联网获取。实际构建完整 web/dist。

缓存配置在 Dockerfile 中：Bun 下载缓存 `/root/.bun/install/cache`，Go 模块缓存 `/go/pkg/mod`，Go 编译缓存 `/root/.cache/go-build`。Go 下载和编译共用模块缓存；缓存使用项目专属 ID 和 `sharing=locked`。保持 `GOMAXPROCS=2` 与 `go build -p 2`。缓存不进入最终运行镜像。首次切换 BuildKit 需建立新缓存，后续构建才能受益；缓存清理后会重新下载和编译，脚本不自动清理缓存。

`Dockerfile.cached.dockerignore` 覆盖根目录的忽略规则，只包含生产构建输入，排除部署脚本、文档和本地产物。仅修改部署脚本不会使应用源码层失效。新增 Go 源码目录或嵌入资源时，需更新该文件的允许列表。现有 `go:embed` 的前端产物、翻译、Lua 和任务插件资源均保留。前端未变时可复用前端层；前端变化时仍需构建前端，并重新构建嵌入前端产物的 Go 程序。

缓存会占用服务器磁盘，可用 `docker buildx du` 查看。首次完整构建成功后，再比较重复构建、仅改 Go 和仅改前端三种场景的耗时；不可用模拟部署测试代替真实构建验证。

```bash
# 在 /opt/knowyet-model-gateway/new-api 内执行。
DOCKER_BUILDKIT=1 docker build --progress=plain --pull=false -f deploy/production/Dockerfile.cached \
  -t knowyet-new-api:billing-units-20260910 .
# 配置好独立数据库以后，在部署根目录执行。
docker compose config --quiet
docker compose up -d
```

首次初始化在回环入口完成，设置随机管理员凭证、关闭公开注册并将新用户赠送额度设为 0。初始化完成前不开放公网。域名配置按 nginx.conf.template 渲染；nginx -t 通过才 reload。流式代理关闭缓冲。

上线验证：容器健康、主页静态资源、未授权接口拒绝、SSO 原域名仍正常。部署本身不配置真实上游 key、不启用 known-engine 网关开关、不注入或消费真实额度。首次计费验证另行使用 mock。

停止或回退仅操作当前 Compose 项目，保留数据库与数据卷。不要使用 docker compose down -v，不操作 Casdoor 容器或 RDS 表。

Docker Hub 拉取 Redis 超时时，使用 Dockerfile.redis 从缓存 Alpine 安装发行版签名 Redis 包（本次 8.8.0-r0），镜像 knowyet-redis:alpine；通过服务器 .env 的 REDIS_IMAGE 选择。该容器仅在独立 Compose 网络可达，不暴露公网端口。

## 2026-09-10 部署结果

已从完整源码构建 knowyet-new-api:billing-units-20260910（镜像 d62efba4f971），在 SSO 主机启动。RDS 独立库 zhicui_api 原为空库，默认 utf8mb3 触发应用检查，已将默认字符集调整为 utf8mb4 / utf8mb4_unicode_ci，随后应用自动建表成功。

生产配置启用 SESSION_COOKIE_SECURE、SESSION_COOKIE_TRUSTED_URL=https://api.knowyet.com 和 PASSWORD_LOGIN_ENCRYPTION_ENABLED。Redis 启用独立随机密码；服务器 redis.conf 通过只读挂载供非 root Redis 进程读取，部署根目录为 0700；同一密码配置在 runtime.env 的 REDIS_CONN_STRING 中。该配置文件不入库，启动 Compose 前必须准备好，不要输出其内容。

验证通过：两个容器 healthy；公网 HTTPS 主页、静态资源、状态接口返回 200；管理员加密登录、设置更新、资源包列表和退出通过；匿名访问资源包接口、凭据分配 POST、模型调用 POST 返回 401；HTTP 跳转 HTTPS；原 SSO 主页仍为 200。管理员随机凭据仅保存于服务器 bootstrap-admin.json（0600）。公网证书已签发，已有 certbot 续期 Nginx reload hook。

内网 https://api-inner.knowyet.com 已接通，Nginx 限制私网来源；SSO 主机和预发服务器 8.130.130.198 访问状态接口均为 200，证书验证通过；从公网指定该域名访问返回 403。内网证书有效期至 2026-12-09，采用 certbot --manual DNS 验证，尚未配置 DNS 自动验证 hook，因此不能自动续期，需要到期前手工续期或接入 DNS 自动化。未配置真实模型渠道、充值商户或 SSO OAuth Provider，未切换 known-engine 生产模型调用；本次验证不代表生产端到端计费链路已开通。后续配置 SSO 集成与 mock 链路验证，并补齐内网证书自动续期。

## 预发业务接入验收

2026-09-10 已配置独立 Casdoor model-gateway 应用和 new-api casdoor 提供商（ID 1），scopes 为 openid profile。8.130.130.198 的 known-engine Gateway/Worker 同时启用内网模型网关，使用 deepseek-v4-flash。peilong 真实 SSO/aibrain/Agent 验收共 8 个任务：5 次成功，余额不足、单元停用、个人 Token 停用各拒绝 1 次；个人与单元付款日志和余额逐笔一致。new-api 自身 SSO 页面登录也通过，映射到同一用户 ID 2。

测试用户现为个人付款，个人 Token 已恢复启用；专用测试计费单元 ID 1 已停用，保留历史账目和余额。配置切换使用完整 test + sso Compose 组合，回退配置在预发主机 .env.before-model-gateway。验收详情见 known-engine 的 docs/model-gateway/design-and-usercases.md。生产 known-engine 未切换；真实支付、真实上游故障注入、第二个业务系统及双真实 SSO 用户共享消费仍未在预发验收。
