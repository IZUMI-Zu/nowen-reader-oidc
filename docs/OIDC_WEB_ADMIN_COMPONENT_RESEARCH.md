# NowenReader OIDC Web 管理组件调研

> 调研日期：2026-08-10
> 来源范围：项目当前源码、组件官方文档/官方仓库、OpenID 规范、OWASP。
> 目标：让管理员在 Web 后台安全配置单个 OIDC Provider，同时保留环境托管和故障恢复能力。

## 结论

不需要替换现有 OIDC 实现，也不需要增加 OAuth/OIDC 前端 SDK、JWT 库、ORM 或数据库。推荐组合如下：

| 职责 | 采用 | 结论 |
|---|---|---|
| OIDC Discovery、JWKS、ID Token 验证 | 现有 `github.com/coreos/go-oidc/v3/oidc` v3.20.0 | 保留；每份配置建立一个不可变 Provider 快照 |
| Authorization Code、PKCE、Token Exchange | 现有 `golang.org/x/oauth2` v0.36.0 | 保留；不在 React 中重复实现协议 |
| 配置持久化 | 现有 `database/sql` + `modernc.org/sqlite` v1.34.5 | 新增专用表和仓储；不塞进公开的 `SiteConfig`，不引入 ORM |
| 运行时切换 | Go 标准库 `sync.Mutex` + `atomic.Pointer` | 候选配置先完整构建和检查，成功后整份原子替换，禁止逐字段热改 |
| Client Secret 加密 | Go 1.25 标准库 AES-256-GCM，使用 `cipher.NewGCMWithRandomNonce` | 不自写 nonce、CBC/HMAC 或加密算法；根密钥必须与 SQLite 分离 |
| 管理端保护 | 复用现有 `SessionRequired`、`AdminRequired`、`RequireRecentAuthentication`、严格限流 | 所有读取、测试、保存、启停都只允许管理员浏览器 Session；高风险修改要求近期重新认证 |
| 审计 | SQLite 审计事件表 + 结构化日志 | 记录操作者、动作、结果和变更字段名，绝不记录 secret/ciphertext/token/code |

当前代码已经有合适的协议边界：[`internal/auth/oidc/provider.go`](../internal/auth/oidc/provider.go) 把 `go-oidc`/`x/oauth2` 封装为 Provider，[`internal/auth/oidc/service.go`](../internal/auth/oidc/service.go) 管理 state、nonce 和 PKCE，[`internal/store/oidc_store.go`](../internal/store/oidc_store.go) 用 SQLite 原子消费登录交易。Web 管理功能应在这些边界外增加“配置生命周期”，不重写协议层。

## 1. `go-oidc` 与 `x/oauth2` 是否适合热更新和测试

### 1.1 继续使用，两者已覆盖协议难点

`oidc.NewProvider(ctx, issuer)` 会执行 Discovery，并且在 Discovery 文档返回的 `issuer` 与输入不一致时失败；`Provider.Verifier` 负责签名、issuer、audience、expiry 等 ID Token 校验。其 `RemoteKeySet` 是长生命周期对象，会缓存 JWKS，并在遇到未知 key ID 时重新获取，因此官方建议复用而不是按请求创建。[go-oidc 官方 API](https://pkg.go.dev/github.com/coreos/go-oidc/v3/oidc)

`x/oauth2` 已提供 `Config.AuthCodeURL`、`Config.Exchange`、`GenerateVerifier`、`S256ChallengeOption` 和 `VerifierOption`，正好对应项目当前的 Authorization Code + PKCE 流程。[x/oauth2 v0.36.0 官方 API](https://pkg.go.dev/golang.org/x/oauth2@v0.36.0)

结论：保留当前依赖和现有适配层，不增加第二套 JWT/JWK/OIDC 库，也不在 React 中引入 OIDC SDK。

### 1.2 库不负责 reload；应用应替换不可变快照

`go-oidc.Provider` 和当前 `RemoteProvider` 都适合作为某一版本配置的长生命周期对象，不适合在使用中改 `issuer/clientID/clientSecret/scopes`。推荐引入：

```text
OIDCConfigRepository  ->  OIDCConfigManager  ->  atomic.Pointer[RuntimeSnapshot]
                              |
                              +-> Provider + Service + 有效配置（整份不可变）
```

- 写操作由一个 `sync.Mutex` 串行化；读路径只 `Load()` 当前快照。
- 候选配置先解析、解密、Discovery 和端点校验，全部成功后写入数据库，再用 `atomic.Pointer.Store` 发布已经构建完成的快照。
- 失败时保留上一份运行时快照；Provider 故障或密钥解密失败不能让整个服务无法启动。
- 配置切换时应让旧的未完成 OIDC 登录交易明确失效并提示重试，或者给交易记录增加配置版本并在 5 分钟 TTL 内保留旧快照。第一版选择“使旧交易失效”更简单，但切换与创建交易必须由同一个配置管理器协调，避免切换后又写入旧交易。

Go 官方为 `atomic.Pointer[T]` 提供原子 `Load/Store/Swap`；官方也提醒更复杂的同步优先使用 `sync`，因此这里只让 atomic 承担“发布完整只读快照”，更新流程仍用普通互斥锁。[Go 1.25 `sync/atomic`](https://pkg.go.dev/sync/atomic@go1.25.0#Pointer)

### 1.3 “测试连接”必须分成两级

非交互检查可以验证：字段格式、HTTPS/loopback 规则、网络超时、Discovery、issuer 精确匹配、授权/Token/JWKS 端点和 metadata。项目现有 `NewRemoteProvider` 的 URL、端点和超时规则应原样复用，不能为后台测试另写一套宽松校验。

但是 Discovery **不能验证 Client Secret**。OIDC Authorization Code Flow 直到 Token Endpoint 换码阶段才使用 confidential client 的认证信息；因此只调用 `oidc.NewProvider` 后显示“配置全部正确”会误导管理员。[OIDC Core §3.1.3.1](https://openid.net/specs/openid-connect-core-1_0.html#TokenRequest)

UI 和 API 应明确区分：

- `检查 Discovery`：无浏览器跳转，结果只代表 issuer、metadata、端点和网络可用。
- `完成测试登录`：使用候选配置走一次真实 Authorization Code + PKCE + ID Token 验证；只有这一层成功，才能标记 `lastVerifiedAt`，并允许关闭密码登录。

“测试”不应申请 `client_credentials` grant 或调用 Provider 私有接口，因为那不是 OIDC 登录客户端的通用验证方式。

## 2. Client Secret 的存储边界

### 2.1 推荐实现

Web 托管模式需要可逆读取 Client Secret，哈希无法满足 Token Exchange。推荐：

1. SQLite 只保存 AEAD ciphertext、格式版本和 key ID。
2. 使用 Go 1.25 标准库 `crypto/aes` + `cipher.NewGCMWithRandomNonce`。该 API 自动生成 96-bit 随机 nonce、把 nonce 前置到 ciphertext，并在解密时提取，避免应用自行管理 nonce；同时用 associated data 绑定固定用途和配置行 ID。[Go 1.25 `crypto/cipher`](https://pkg.go.dev/crypto/cipher@go1.25.0#NewGCMWithRandomNonce)
3. 32-byte 根密钥优先从只读文件读取，例如 `OIDC_CONFIG_KEY_FILE=/run/secrets/nowen_oidc_config_key`。Docker Compose 的 secret 会按服务授权并挂载为文件，避免把值放进镜像或直接暴露为环境变量。[Docker Compose secrets 官方文档](https://docs.docker.com/compose/how-tos/use-secrets/)
4. ciphertext 使用带版本的封装，例如 `v1:key-id:base64(...)`，为将来轮换留出兼容入口；更新 secret 时永远以当前主 key 加密，旧 key 只用于迁移读取。

这不是“加密后就绝对安全”：AEAD 能保护数据库文件单独泄露、误发备份和篡改，但运行中的应用必须能解密，因此拥有容器/主机执行权限的攻击者仍可取得明文。

### 2.2 没有外部根密钥时必须诚实降级

如果为了零配置体验在 `DATA_DIR` 自动生成一个权限为 `0600` 的本地 key 文件，它只能防“只拿到 SQLite 文件”的泄露；数据库、key 和备份若在同一个数据卷一起被复制，就没有实质隔离。不能把 key 存回同一个 SQLite，也不能从数据库字段或硬编码常量派生。

建议产品策略：

- 首选外部 key 文件；后台显示“外部密钥保护”。
- 可选本地自动生成 key 作为 NAS 易用模式，但明确显示“仅防数据库单文件泄露”，并提醒备份 key，否则重装后无法解密。
- 根密钥不可用或 ciphertext 校验失败时，不回显任何密文/错误细节；OIDC 标为不可用，保留本地登录恢复路径。

OWASP 建议机密使用最小权限、可轮换、可撤销、可审计的生命周期管理，并明确要求 secret 不进入日志；它也提醒环境变量可能被其他进程、日志或 dump 暴露。[OWASP Secrets Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html)

### 2.3 不推荐的方案

- **操作系统 Keyring**：`zalando/go-keyring` 是成熟的跨平台库，但 Linux/*BSD 后端依赖 Secret Service D-Bus 和 GNOME Keyring 的 `login` collection。NowenReader 的主要环境是 headless NAS/container，通常没有用户会话、D-Bus 或可解锁 collection，因此不适合作为服务器默认存储。[go-keyring 官方仓库](https://github.com/zalando/go-keyring)
- **把加密 key 与密文放在同一数据库/同一配置 JSON**：只提供外观上的加密。
- **SQLCipher 作为本期依赖**：即使全库加密，应用仍需管理外部 key；它也不能替代“不向 API/日志返回 Client Secret”的应用级边界。当前 `modernc.org/sqlite` 路径无需为一个字段更换驱动。
- **当前就引入 Vault/KMS/Tink**：Tink 是成熟方案并支持 keyset 轮换及外部 KMS，但官方同样要求使用外部 KEK/KMS 保护 keyset。[Tink Key Management](https://developers.google.com/tink/key-management-overview) 当前单机 NAS 只加密一个配置 secret，用 Go 标准库 AEAD 更小、更容易审计；未来真的需要多实例 KMS 时，应采用 Tink/KMS，而不是继续扩展本地封装。

## 3. SQLite 与配置来源

### 3.1 沿用现有数据库组件

项目已经使用 `database/sql`、`modernc.org/sqlite`、WAL 和 `busy_timeout`，并在 OIDC identity/login transaction 中使用 `BeginTx`。官方 `database/sql` 事务可把配置、版本和审计事件作为一个原子目标提交，失败则全部回滚；事务内不能混用事务外的 `sql.DB` 调用。[Go 数据库事务指南](https://go.dev/doc/database/execute-transactions)

建议专用表而不是 `site-config.json`：

```text
OIDCProviderConfig
  id=1, enabled, issuerURL, clientID, secretCiphertext,
  providerName, scopes, autoProvision, sessionTTL,
  disablePasswordLogin, version, lastVerifiedAt,
  updatedBy, updatedAt

OIDCConfigAudit
  id, actorUserID, action, result, changedFields,
  configVersion, requestID, createdAt
```

- `GET` DTO 只返回 `clientSecretConfigured: true/false`，永不返回明文或 ciphertext。
- `PUT` 中 secret 缺省表示“不变”；清除 secret 使用独立、明确的动作，避免空字符串误删。
- 通过 `version` 做 optimistic compare-and-swap，防止两个后台页面互相覆盖。
- 配置更新和成功审计放进一个 `sql.Tx`；失败尝试用无敏感数据的独立安全日志记录。
- 不需要为了这张单行表改变全局 DSN。SQLite 的 `BEGIN IMMEDIATE` 会立即启动写事务，但也可能在已有 writer 时返回 `SQLITE_BUSY`；只有并发测试证明需要时，才考虑 `modernc` 已支持的 `_txlock=immediate`，不要先影响全库事务。[SQLite 事务官方文档](https://sqlite.org/lang_transaction.html)、[modernc SQLite 官方仓库](https://gitlab.com/cznic/sqlite)

### 3.2 环境配置与后台配置必须是整套来源，不做逐字段混合

推荐优先级：

```text
完整且显式启用的 OIDC 环境配置  >  SQLite Web 配置  >  disabled
```

当环境配置生效时，后台返回 `managedBy: "environment"` 并只读。不要把 issuer 来自环境、secret 来自数据库、scopes 又来自环境；这种逐字段覆盖难以诊断和审计。`PUBLIC_URL`、`BASE_PATH` 等部署拓扑仍由部署配置管理，后台只展示由它们计算出的 callback URL。

Open WebUI 的官方做法值得借鉴：OAuth 数据库持久化由 `ENABLE_OAUTH_PERSISTENT_CONFIG` 单独显式开启，默认仍让环境变量权威，以适配 GitOps/不可变部署；官方文档同时提醒关闭本地登录前必须先完成 OAuth 配置。[Open WebUI 环境配置](https://docs.openwebui.com/reference/env-configuration/#enable_oauth_persistent_config) 其源码还把 OAuth 加密 key 默认关联到进程外提供的 `WEBUI_SECRET_KEY`，并在认证启用但 key 缺失时拒绝继续使用不安全空 key。[Open WebUI `env.py`](https://github.com/open-webui/open-webui/blob/main/backend/open_webui/env.py)

## 4. 管理权限、重新认证、审计和锁死保护

所有 `/api/admin/auth/oidc/**` 路由应按以下顺序复用现有中间件：

```text
SessionRequired -> AdminRequired -> RequireRecentAuthentication(10m) -> RateLimitStrict
```

API key 不得管理认证配置。读取接口也只给管理员，因为 issuer/client ID、运行状态和验证时间属于安全配置元数据。

OWASP 建议敏感设置修改前要求当前凭据/重新认证，以降低 CSRF、会话劫持和临时占用浏览器造成的风险；同时要求记录认证成功/失败、授权失败、配置变化和管理员高风险操作。[OWASP Authentication Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html#require-re-authentication-for-sensitive-features)、[OWASP Logging Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html)

关闭密码登录必须同时满足：

1. 当前候选配置已完成真实测试登录，而不只是 Discovery；
2. 至少一个现有管理员已绑定该配置的精确 `(issuer, subject)`；
3. 当前管理员近期重新认证；
4. 有一个部署侧、不能从 Web 关闭的恢复开关，例如 `OIDC_FORCE_PASSWORD_LOGIN=true`；
5. UI 二次确认并展示准确 callback URL 和恢复方法。

建议要求至少一个管理员先设置本地 break-glass 密码。Provider 不可达、配置解密失败或 Web 配置无效时，默认恢复本地登录；只有明确的环境托管 fail-closed 策略才继续禁止密码登录。OIDC 登录/测试入口继续使用现有严格限流；密码登录还应遵循 OWASP 的账户维度失败计数、观察窗口和临时/指数锁定建议，不能只依赖 IP 限流，同时要避免永久锁定被用于拒绝服务。[OWASP 登录限流与账户锁定](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html#login-throttling)

审计只记录：actor、动作、目标配置版本、变更字段名、成功/失败分类、时间、request ID。禁止记录 Client Secret、根密钥、ciphertext、authorization code、Token、PKCE verifier、完整 callback query 或 Provider 返回的原始错误正文。

## 5. Open WebUI 与 Jellyfin 的可借鉴边界

### Open WebUI

可借鉴的是“环境默认权威、数据库持久化单独 opt-in、后台管理、公共 URL 明确配置、关闭本地登录有警告”这一配置治理方式，而不是复制其 Python 实现。Open WebUI 官方文档明确说明 OAuth 持久化开关的行为，并且目前也只支持一个通用 OIDC Provider。[Open WebUI SSO/OIDC 文档](https://docs.openwebui.com/features/authentication-access/auth/sso/)

### Jellyfin

截至本次调研，Jellyfin 官方核心文档只描述默认认证 Provider 和插件扩展，没有可验证的核心 OIDC 后台实现可直接作为基线。[Jellyfin 官方用户管理文档](https://jellyfin.org/docs/general/server/users/adding-managing-users/)

常被引用的 `9p4/jellyfin-plugin-sso` 不属于 Jellyfin 官方组织；其仓库已于 2026-05-12 归档，README 自称 alpha，并暴露 `doNotValidateEndpoints`、`doNotValidateIssuerName` 等危险兼容开关。因此它只能证明“管理员希望在 Dashboard 配置 SSO”的产品需求，不能作为成熟安全组件或实现模板。[插件仓库原始说明](https://github.com/9p4/jellyfin-plugin-sso)

## 6. 建议的第一期组件边界

```text
Admin API / React page
        |
        v
OIDCConfigManager
  - ResolveSource()       env 或 database，整套选择
  - ValidateCandidate()   复用现有 RemoteProvider 规则
  - ProbeDiscovery()      go-oidc + 超时 HTTP client
  - Activate()            sql.Tx 后发布不可变快照
        |                         |
        v                         v
OIDCConfigRepository       atomic.Pointer[RuntimeSnapshot]
  database/sql + modernc      Provider + Service + Config
        |
        v
SecretProtector
  AES-256-GCM random nonce
  key from mounted file / local 0600 fallback
```

推荐实施顺序：先做专用配置仓储、密钥保护和运行时快照；再做管理员 API 与 Discovery 检查；然后补候选配置的真实测试登录和锁死门禁；最后接 React 后台页和审计展示。每一步都继续使用现有 OIDC 协议测试夹具，不另造协议模拟器。
