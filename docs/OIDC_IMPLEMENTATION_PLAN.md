# NowenReader OIDC 实施计划

状态：Web Phase 0–3 已实现；真实 Provider 验收与 Flutter 待后续阶段
日期：2026-08-10
目标版本：待定

## 1. 结论

NowenReader 应将 OIDC 作为新的**身份验证方式**，而不是替换现有本地用户和授权模型。

推荐的核心链路是：

```text
OIDC Provider
    -> Authorization Code Flow
    -> OIDC 认证模块验证身份
    -> (issuer, subject) 映射本地 User.id
    -> 签发现有 UserSession
    -> 继续使用现有 AuthRequired / AdminRequired / 书库权限
```

这样可以保留阅读进度、收藏、用户组、书库权限和 API Key 的用户隔离，同时把协议复杂度集中在一个深 OIDC 模块内。

### 1.1 2026-08-10 实施快照

已完成：

- Go 1.25、Docker builder 和 CI 基线同步升级。
- `go-oidc` + `x/oauth2` 的 Discovery、Authorization Code、PKCE S256、ID Token/JWKS 校验。
- browser-bound 单次交易、state/nonce、固定 callback、严格本地 return target。
- `(issuer, subject)` 身份映射、显式绑定/安全解绑、可控自动创建及首管理员 allowlist。
- OIDC Session 绝对期限、交互式 reauth、`auth_time` 校验和 OIDC-only API Key 管理。
- Web 登录入口、账号生命周期、API Key reauth 后自动恢复未完成操作。
- OIDC 配置、Docker 和反向代理文档；密码登录与本地恢复路径保持可用。
- 可选 fail-closed 密码登录开关；同时关闭自助注册，并保护最后一个 OIDC 身份不被解除。
- 管理员 Web 配置、环境只读兼容、AES-GCM Client Secret 保护、Discovery 检查、真实测试登录和运行时热切换。

尚未完成：

- Keycloak、Authentik 或 Authelia 的真实环境烟测（当前未提供 issuer/client 配置）。
- Flutter 原生客户端阶段。

## 2. 设计目标

### 2.1 必须实现

- 符合 OpenID Connect Core 1.0 的 Authorization Code Flow。
- 使用 OIDC Discovery 获取授权、Token 和 JWKS 端点。
- 使用成熟实现完成 Discovery、Token 交换、签名和 ID Token 校验，不自行实现 JWT/JWK/OAuth 协议。
- 每次授权使用随机 `state`、`nonce` 和 PKCE S256，且交易只能消费一次。
- 使用 `(issuer, subject)` 作为外部身份唯一键，不把 email 或 username 当作身份主键。
- OIDC 身份必须映射到本地 `User.id`，现有授权逻辑保持不变。
- 本地密码登录继续可用，至少保留一个 break-glass 管理员恢复路径。
- 支持安全的已有账号绑定，不通过相同 email 自动绑定。
- OIDC Provider 暂时不可用时，服务仍能启动，已有本地 Session 和本地登录仍可使用。
- Web、子路径部署和可信反向代理场景均可生成确定且不可伪造的回调地址。
- OPDS 继续使用现有用户 API Key；不要求 OPDS 客户端实现 OIDC。

### 2.2 第一版不实现

- 不把 Provider access token 当作 NowenReader 业务接口的 Bearer Token。
- 不保存 Provider access token、refresh token 或原始 ID Token。
- 不根据 email 自动合并账号。
- 不在第一版同步 IdP group claim 到本地角色或书库权限。
- 不在第一版支持多个并存的 OIDC Provider；数据模型应允许后续扩展。
- 不自建 OAuth/OIDC Provider。
- 不把 IP 白名单当成用户身份。

## 3. 采用的标准与成熟实现

### 3.1 标准基线

| 领域 | 标准 | 本项目要求 |
|:---|:---|:---|
| OIDC 登录 | OpenID Connect Core 1.0 | Authorization Code Flow、ID Token、nonce、标准 claims |
| Provider 配置 | OpenID Connect Discovery 1.0 | 从 issuer 发现授权、Token、JWKS 等端点 |
| OAuth 安全 | RFC 9700 | 避免隐式流、密码模式、开放重定向和授权码重放 |
| PKCE | RFC 7636 | 每次请求生成 verifier，固定使用 S256 |
| 原生客户端 | RFC 8252 | Flutter 使用外部浏览器，不使用嵌入式 WebView |
| Device Flow | RFC 8628 | 仅用于缺少合适浏览器或输入能力的设备；不用于规避普通 App 回调问题 |
| Logout | OIDC RP-Initiated Logout 1.0 | Provider 支持时再增加；本地登出必须始终可用 |

具体标准来源和组件核验见 [OIDC_STANDARDS_RESEARCH.md](./OIDC_STANDARDS_RESEARCH.md)。

### 3.2 组件选择

后端生产实现：

- `github.com/coreos/go-oidc/v3/oidc`
  - 负责 Discovery、Provider 元数据、JWKS 获取与缓存、ID Token 签名和标准 claim 校验。
- `golang.org/x/oauth2`
  - 负责 Authorization Code URL、Token 交换、PKCE S256 参数。
- Go 标准库 `crypto/rand`、`crypto/sha256`、`net/url`
  - 只负责生成应用交易标识、哈希持久化键以及严格 URL 校验。

Web 不引入浏览器 OIDC SDK。NowenReader 后端是 confidential client，React 只跳转到后端登录入口并在回调完成后刷新 `/api/auth/me`。

依赖版本存在一个必须在 Phase 0 解决的硬门槛：实施前仓库声明 Go 1.23，而 `go-oidc` v3.20.x 要求 Go 1.25。本次已将项目、Docker 和 CI 基线一起提升到 Go 1.25，并固定使用受维护的当前版本；没有采用固定旧版组件的兼容路径，也没有改为自写 JWT/JWK。

Flutter 的 transport 组件以 Phase 0 平台 spike 结果为准：

- 使用系统浏览器或系统认证会话。
- 支持 Authorization Code + PKCE。
- Android/iOS 不使用嵌入式 WebView。
- 回调 URI 有平台级绑定与明确 allowlist。
- 不把 client secret 打包进 App。
- `flutter_appauth` 只覆盖 Android/iOS/macOS，不能承诺为全平台方案。
- `flutter_web_auth_2` 在 Windows/Linux 默认可能使用 WebView，且其 external-browser 回调约束与 RFC 8252 推荐的 IP literal loopback 不完全一致，不能直接选作全平台基线。
- 优先验证 `app_links` + 项目现有 `url_launcher` 作为“外部浏览器 + 固定回调”运输层；OIDC 交易和验证仍由 Go 后端负责。

## 4. 模块与 seam

新增 `internal/auth/oidc` 深模块，外部接口只暴露认证意图，不泄漏 OAuth/OIDC 细节：

```go
type Service interface {
    Begin(ctx context.Context, request BeginRequest) (AuthorizationRedirect, error)
    Complete(ctx context.Context, request CallbackRequest) (AuthenticatedIdentity, error)
}
```

接口语义：

- `Begin` 创建一次性登录交易，返回 Provider 授权地址和短期浏览器绑定 Cookie。
- `Complete` 原子消费交易，交换授权码，校验 ID Token，返回规范化外部身份。
- handler 不解析 JWT，不直接读取 Provider claim，不保存 Provider Token。
- 本地用户查找、自动创建、账号绑定和角色策略放在认证用例层，不塞进 HTTP handler。
- Session 签发抽出为共享函数，密码登录和 OIDC 登录共用同一实现。

OIDC Provider 是不可控远程依赖。模块内部定义最小 Provider port：生产 adapter 使用 `go-oidc`/`x/oauth2`，测试 adapter 使用进程内假 Provider。测试只通过 `Begin`/`Complete` 和最终 HTTP 行为验收。

## 5. Web 登录流程

### 5.1 开始登录

```text
GET /api/auth/oidc/login?returnTo=/
```

1. 检查 OIDC 已启用且 Provider 可用。
2. 只接受站内相对 `returnTo`，拒绝 scheme、host、反斜线和跨 `BASE_PATH` 路径。
3. 生成至少 256 bit 随机 `state`、`nonce` 和 PKCE verifier。
4. 数据库仅保存 `state` 哈希；保存 nonce、verifier、用途、过期时间和 returnTo。
5. 设置短期 HttpOnly、SameSite=Lax、Secure 的交易绑定 Cookie。
6. 使用 `response_type=code`、`scope=openid profile email` 和 PKCE S256 重定向 Provider。

### 5.2 回调

```text
GET /api/auth/oidc/callback?code=...&state=...
```

1. 对比查询参数、浏览器绑定 Cookie 和数据库交易。
2. 原子标记交易已消费；过期、重复或不匹配均失败。
3. 用保存的 PKCE verifier 交换授权码。
4. 用 `go-oidc` 验证签名、`iss`、`aud`、过期时间和 ID Token 结构；多 audience 时额外执行 `azp` 策略检查。
5. 由应用显式校验 ID Token `nonce` 与交易一致；`go-oidc` 不代替应用完成 nonce 校验。
6. 读取 `sub`；缺失时拒绝登录。
7. 可读取 `preferred_username`、`name`、`email`、`email_verified` 作为资料候选，但不作为身份主键。
8. 通过 `(issuer, sub)` 查找本地身份绑定。
9. 根据账号策略完成登录、自动创建或返回可操作的冲突错误。
10. 创建新的本地 Session，清理交易 Cookie，重定向到经过验证的 returnTo。

回调不得记录授权码、Token、完整 claims 或 client secret。

## 6. 本地身份与数据库迁移

当前最新迁移为 v39；OIDC 变更从 v40 开始，具体版本在开发时以合并基线为准。

### 6.1 `ExternalIdentity`

```sql
CREATE TABLE "ExternalIdentity" (
    "id"            TEXT NOT NULL PRIMARY KEY,
    "userId"        TEXT NOT NULL,
    "issuer"        TEXT NOT NULL,
    "subject"       TEXT NOT NULL,
    "email"         TEXT NOT NULL DEFAULT '',
    "emailVerified" BOOLEAN NOT NULL DEFAULT 0,
    "displayName"   TEXT NOT NULL DEFAULT '',
    "createdAt"     DATETIME NOT NULL,
    "lastLoginAt"   DATETIME NOT NULL,
    FOREIGN KEY ("userId") REFERENCES "User"("id") ON DELETE CASCADE
);

CREATE UNIQUE INDEX "ExternalIdentity_issuer_subject_key"
ON "ExternalIdentity"("issuer", "subject");

CREATE UNIQUE INDEX "ExternalIdentity_user_issuer_key"
ON "ExternalIdentity"("userId", "issuer");
```

约束：

- issuer 使用 Discovery 后验证过的规范 issuer，不接受请求参数传入的任意 issuer。
- subject 只来自已验证 ID Token。
- email 允许为空且不唯一。
- 同一 Provider 的身份不能静默绑定到多个本地用户。

### 6.2 `OIDCLoginTransaction`

保存短期一次性状态：

- `stateHash`
- `nonce`
- `pkceVerifier`
- `purpose`: `login` / `link` / `reauth`
- `sessionUserId`：link/reauth 时绑定当前用户
- `returnTo`
- `expiresAt`
- `consumedAt`

交易默认 5 分钟过期，消费必须在单个数据库事务中完成。后台清理器删除过期和已消费记录。

### 6.3 本地密码表示

为避免重建 SQLite `User` 表，第一版保留 `password TEXT NOT NULL`：

- `password = ''` 明确表示没有本地密码。
- 本地登录在 bcrypt 前显式拒绝空密码哈希，并继续返回统一的“用户名或密码错误”。
- `AuthUser` 增加 `hasPassword`，不暴露哈希。
- OIDC 自动创建的用户默认没有本地密码。
- 不生成未知随机密码作为占位符，避免系统误判用户具有本地密码。

### 6.4 Session 安全信息

为支持敏感操作和 IdP 停用后的会话收敛，`UserSession` 增加：

- `authMethod`: `password` / `oidc`
- `authenticatedAt`
- `absoluteExpiresAt`

策略：

- 密码 Session 保持现有兼容行为。
- OIDC Session 使用可配置的绝对期限，默认建议 12 小时；不能通过现有滑动续期突破绝对期限。
- 不保存 Provider refresh token。
- 管理员仍可删除用户或吊销其全部本地 Session。

## 7. 用户创建、绑定与角色策略

### 7.1 新用户

- `OIDC_AUTO_PROVISION=false` 为安全默认值。
- 关闭自动创建时，未知 `(issuer, sub)` 返回 403，不创建任何记录。
- 开启后，新用户默认角色为 `user`。
- username 候选顺序：`preferred_username`、已验证 email 的本地部分、`oidc-<subject摘要>`。
- username 必须经过现有长度/字符规则；冲突时添加稳定短后缀。
- 昵称可来自 `name`，失败时回退 username。

### 7.2 首次管理员

不得简单采用“第一个 OIDC 登录者成为管理员”。支持以下安全引导方式：

1. 推荐：先通过现有首次设置创建本地 break-glass 管理员，再由该管理员开启/绑定 OIDC。
2. 无本地设置模式：必须配置精确的 `OIDC_BOOTSTRAP_ADMIN_SUBJECTS`；仅匹配 subject 的用户可成为首个管理员。

生产默认选择方式 1。

### 7.3 绑定已有账号

- 已登录的本地用户从账号设置发起 `purpose=link` 的 OIDC 流程。
- 回调交易必须绑定原 Session 用户。
- 若 `(issuer, sub)` 已属于其他用户，拒绝绑定。
- 不因 email 相同自动绑定，即使 `email_verified=true`。
- 解除绑定前必须确认用户还有本地密码或另一种可用登录方式。

### 7.4 角色和书库权限

- 第一版角色、AI 权限、用户组和书库权限继续由 NowenReader 管理。
- 不在每次登录时用 IdP claims 覆盖本地角色。
- `OIDC_ADMIN_SUBJECTS` 如实现，只用于明确的管理员引导或升级策略，并记录审计日志。
- group/role claim 映射作为后续独立功能设计，不能混入第一版登录链路。

## 8. 敏感操作与 API Key

OIDC-only 用户没有“当前密码”，但 OPDS 依赖用户 API Key，因此必须把敏感操作从“必须输入密码”提升为统一的“最近重新认证”。

新增内部接口：

```go
RequireRecentAuthentication(maxAge time.Duration)
```

行为：

- Session 必须在指定时间窗口内完成密码校验或 OIDC reauth。
- API Key credential 永远不能管理其他凭据。
- 过期时返回结构化 `reauth_required`，而不是普通 401。
- 密码用户可提交当前密码刷新 `authenticatedAt`。
- OIDC 用户走 `purpose=reauth` 流程；请求 `prompt=login`/`max_age=0` 时还需校验 Provider 返回的 `auth_time`。
- API Key 创建和批量撤销改用这一接口；单个撤销是否要求 reauth 保持现状并单独评审。

## 9. 配置

当前版本同时支持管理员 Web 托管和完整环境变量托管，绝不逐字段混用。Web 托管的 Client Secret 加密后存入专用 SQLite 表，接口只返回 `clientSecretConfigured`；环境托管模式下后台只读。组件选择和具体实施分别见 [OIDC Web 管理组件调研](./OIDC_WEB_ADMIN_COMPONENT_RESEARCH.md) 与 [OIDC Web 管理实施计划](./OIDC_WEB_ADMIN_IMPLEMENTATION_PLAN.md)。

| 变量 | 默认值 | 说明 |
|:---|:---|:---|
| `OIDC_CONFIG_MODE` | `auto` | `auto` / `environment` / `database` 配置来源 |
| `OIDC_CONFIG_KEY_FILE` | — | Web 托管 secret 的外部 32-byte 根密钥文件 |
| `OIDC_FORCE_PASSWORD_LOGIN` | `false` | 部署侧强制恢复密码入口 |
| `OIDC_ENABLED` | `false` | 总开关 |
| `OIDC_ISSUER_URL` | — | Provider issuer；生产必须 HTTPS |
| `OIDC_CLIENT_ID` | — | Web confidential client ID |
| `OIDC_CLIENT_SECRET` | — | 环境托管 Web client secret |
| `OIDC_CLIENT_SECRET_FILE` | — | 环境托管 secret 文件，与明文变量互斥 |
| `OIDC_NATIVE_CLIENT_ID` | — | Flutter public native client ID，仅 Flutter 阶段启用 |
| `PUBLIC_URL` | — | 明确的外部 origin，例如 `https://reader.example.com` |
| `OIDC_DISPLAY_NAME` | `OpenID Connect` | 登录按钮名称 |
| `OIDC_SCOPES` | `openid profile email` | 必须包含 `openid` |
| `OIDC_DISABLE_PASSWORD_LOGIN` | `false` | OIDC 验收后可关闭密码登录和自助注册；配置错误时 fail-closed |
| `OIDC_AUTO_PROVISION` | `false` | 是否自动创建本地普通用户 |
| `OIDC_BOOTSTRAP_ADMIN_SUBJECTS` | — | 无本地管理员时允许引导的精确 subject 列表 |
| `OIDC_SESSION_MAX_AGE` | `12h` | OIDC 本地 Session 绝对期限 |

配置约束：

- 启用 OIDC 时 `issuer`、client ID、client secret 和 `PUBLIC_URL` 必填。
- 回调固定为 `PUBLIC_URL + BASE_PATH + /api/auth/oidc/callback`。
- 不根据未验证的 `Host`、`X-Forwarded-Host` 或 `X-Forwarded-Proto` 动态生成回调。
- `PUBLIC_URL` 不含 query/fragment，路径部分由 `BASE_PATH` 唯一管理。
- 开发环境可显式允许 loopback HTTP；非 loopback HTTP 启动时拒绝。
- 同时补充 Gin `SetTrustedProxies` 的明确配置，不能依赖默认信任全部代理。

## 10. HTTP 接口

### 10.1 第一阶段

| 方法 | 路径 | 认证 | 用途 |
|:---|:---|:---|:---|
| GET | `/api/auth/me` | 可选 | 增加公开的 `loginMethods` 和当前用户 `hasPassword` |
| GET | `/api/auth/oidc/login` | 无 | 创建登录交易并重定向 |
| GET | `/api/auth/oidc/callback` | 交易 Cookie + state | 完成登录 |
| POST | `/api/auth/oidc/link` | Session | 创建绑定交易 |
| DELETE | `/api/auth/oidc/link` | Session + recent auth | 解除绑定 |
| GET | `/api/auth/oidc/reauth` | Session | 创建敏感操作重新认证交易 |
| POST | `/api/auth/logout` | 可选 Session | 删除本地 Session；保持幂等 |

所有开始登录/绑定/reauth 的入口都使用专用限流器。回调错误返回不包含 Provider 原始响应和 Token。

### 10.2 `/api/auth/me` 响应扩展

```json
{
  "user": null,
  "needsSetup": false,
  "registrationMode": "closed",
  "loginMethods": {
    "password": true,
    "oidc": {
      "enabled": true,
      "displayName": "Company SSO"
    }
  }
}
```

客户端据此渲染，不读取或推断后端 OIDC 配置。

## 11. Web 改动

- `AuthContext` 增加 `loginMethods`、`beginOIDCLogin()` 和 callback 错误恢复。
- 登录页根据服务端能力显示 OIDC 按钮。
- `needsSetup=true` 时，仅当配置了安全的 OIDC 管理员引导才显示 OIDC 首次登录。
- 本地登录与 OIDC 并存时使用清晰分隔，不默认隐藏 break-glass 入口。
- 账号设置增加外部身份状态、绑定和解除绑定。
- API Key 面板处理 `reauth_required`：保存未完成意图，完成 reauth 后重试。
- callback 最终只回到严格校验过的站内相对路径。

React 不持有 Provider Token，也不解析 ID Token。

## 12. Flutter 分期

Flutter 当前仅支持用户名/密码并依赖 Dio CookieJar，不能直接复用系统浏览器中的 Web Cookie。

### 12.1 第一阶段

- 服务端 `/api/auth/me` 返回 OIDC 能力，但 Flutter 继续显示本地登录。
- Web OIDC 稳定并完成 Provider 兼容验证后再启用 Flutter 按钮。

### 12.2 原生 OIDC 阶段

必须先完成一份小型 ADR 和真实 Provider spike。优先验证以下 public native client 链路：

```text
Flutter 请求 Native 登录交易
  -> Go 保存 state / nonce / PKCE verifier / redirect URI
  -> Flutter 用外部系统浏览器打开授权地址
  -> Provider 把 code + state 回调到已注册的 App/loopback URI
  -> Flutter 把未解释的 code + state 交回同一 NowenReader 实例
  -> Go 原子消费交易并使用原 verifier 兑换授权码
  -> Go 验证 ID Token 和 nonce
  -> Go 创建现有 Session，Dio CookieJar 保存 Cookie
```

Web confidential client 与 Native public client 使用不同注册信息；Native Token Request 不发送 `client_secret`。`app_links` + `url_launcher` 只负责浏览器和回调运输，不解析或信任 OIDC claims。

如果目标 Provider 不支持由 App 与自有后端拆分完成 public client 流程，则 Android/iOS/macOS 可回退到 `flutter_appauth` 在客户端完成标准 Authorization Code + PKCE。此时 App 必须把 ID Token、服务端预先签发的一次性交易标识交给后端；后端仍要独立验证签名、issuer、audience、expiry、nonce 和单次消费，不能信任“App 已验证”。Windows/Linux/Web 需要另行选定符合 RFC 8252 的外部浏览器与回调实现。

任何后端一次性 App grant 都只是 NowenReader 内部会话兑换协议，必须单独做威胁建模，不能宣称它本身是 OIDC/RFC 标准流程。

选择标准：

- RFC 8252 符合度。
- Android App Link/iOS Universal Link 或 claimed HTTPS redirect 支持。
- 自托管用户的 IdP 配置复杂度。
- Android/iOS/macOS 与 Windows/Linux/Web 的真实支持程度。
- Token 是否暴露给 App、重放面和一次性兑换能力。

无论选哪项：

- 使用外部系统浏览器。
- client secret 不进入 App。
- App 回调 URI 使用 allowlist，不能由请求任意指定。
- App 获取到的最终凭据必须进入现有 Dio CookieJar。
- 离线状态不得因浏览器取消或 IdP 临时不可用而清除原有 Cookie。

Device Authorization Grant 只用于缺少合适浏览器或输入能力的设备，且仅在 Provider Discovery/配置明确支持时启用。普通手机和桌面端必须解决 RFC 8252 回调，不能用 Device Flow 绕开。

## 13. Logout 与账号停用

第一版保证：

- `/api/auth/logout` 删除本地 Session 和 Cookie。
- 即使 Provider 不可用，本地登出也成功。
- 不因 Provider 登出失败而恢复本地 Session。

可选后续能力：

- Provider 暴露 `end_session_endpoint` 时支持 RP-Initiated Logout。
- OIDC Back-Channel Logout。
- IdP group/账号状态变化触发本地 Session 吊销。

在没有 back-channel logout 的第一版，OIDC Session 通过绝对期限限制 IdP 停用后的最长残留访问时间；文档必须明确该窗口。

## 14. 安全验收矩阵

| 场景 | 预期 |
|:---|:---|
| state 缺失、不匹配、过期或重放 | 拒绝且不创建 Session |
| 浏览器交易 Cookie 缺失或不匹配 | 拒绝登录 |
| nonce 缺失或不匹配 | 拒绝登录 |
| PKCE verifier 不匹配 | Token 交换失败，不创建 Session |
| issuer 与配置不一致 | 拒绝 ID Token |
| audience 不包含 client ID | 拒绝 ID Token |
| 多 audience 且 `azp` 缺失或不匹配 | 拒绝 ID Token |
| 签名、`exp`、Token 格式无效 | 拒绝 ID Token |
| Provider 返回 OAuth error | 显示稳定错误码，不泄漏响应内容 |
| returnTo 为外部 URL、`//host`、反斜线或跨 Base Path | 拒绝或回退首页 |
| 相同 email、不同 `(issuer, sub)` | 不自动绑定 |
| 两个并发首次登录使用同一身份 | 只创建一个绑定和一个本地用户 |
| 同一回调并发消费 | 仅一次成功 |
| Provider 不可用 | OIDC 返回 503；本地登录和已有 Session 可用 |
| OIDC-only 用户创建 API Key | recent OIDC reauth 后成功 |
| API Key 调用凭据管理接口 | 保持拒绝 |
| OIDC Session 达绝对期限 | 不再滑动续期，要求重新登录 |
| 非可信代理伪造 Forwarded headers | 不影响回调 origin、Secure 判断或客户端 IP |
| 日志检查 | 不包含 code、Token、secret、完整 claims |

## 15. 测试策略

### 15.1 Go 单元与模块测试

- 配置校验和 canonical issuer/public URL。
- returnTo 验证与 Base Path。
- claim 规范化、username 生成和冲突处理。
- 外部身份 store 的唯一约束和并发创建。
- 交易过期、单次消费和清理。
- Session auth method、absolute expiry 和 recent auth。

### 15.2 假 OIDC Provider 集成测试

使用 `httptest.Server` 提供测试专用 Discovery、Authorization、Token 和 JWKS 端点。生产仍只使用成熟库。

覆盖：

- 成功登录。
- state/nonce/PKCE 错误。
- issuer/audience/签名/过期错误。
- JWKS key rotation 与未知 `kid`。
- Provider 超时和 5xx。
- link、reauth 和重复 callback。
- 子路径及 HTTPS 代理配置。

### 15.3 Web 验收

- 本地登录、OIDC 登录和注册模式组合。
- callback 错误、取消登录、刷新和返回原页面。
- 绑定/解除绑定。
- API Key recent-auth 重试。
- 浏览器 Cookie 的 Path、HttpOnly、SameSite 和 Secure。

### 15.4 Provider 兼容烟测

至少选择一个标准 Provider 做自动或半自动验收；发布前建议覆盖 Keycloak、Authentik、Authelia 中至少两个。兼容结论必须来自真实 Discovery 和完整登录回调，不以配置页面截图代替。

### 15.5 回归命令

```bash
go test ./... -race
go vet ./...

cd frontend
npm run lint
npm run build

cd ../flutter_app
flutter analyze
flutter test
```

CI 目前主要覆盖 Go；OIDC 合并前应把前端构建，以及进入 Flutter 阶段后的 analyze/test 纳入必需检查。

## 16. 实施阶段与工作量

### Phase 0：决策与测试骨架（0.5–1 人日）

- 确认 Provider、公开 URL、首次管理员和自动创建策略。
- 首选完成 Go 1.25、Docker builder 和 CI 基线升级；否则明确 pin Go 1.23 兼容依赖。
- 固定并记录 `go-oidc`、`x/oauth2` 依赖版本。
- 建立假 OIDC Provider 测试骨架。
- 确认 Flutter 是首发范围还是第二阶段。

退出条件：关键配置和账号策略没有未决安全项。

### Phase 1：后端 OIDC 核心（2–3 人日）

- 配置、Discovery 和 OIDC 深模块。
- v40+ 数据库迁移和 store。
- login/callback、身份映射和 Session 签发。
- state/nonce/PKCE/回调安全测试。
- 可信代理和 Secure Cookie 收口。

退出条件：假 Provider 集成测试通过，现有密码/API Key 回归通过。

### Phase 2：Web 与账号生命周期（1.5–2.5 人日）

- 登录页、AuthContext、绑定/解除绑定。
- 首次管理员与自动创建策略。
- recent auth 和 OIDC-only 用户 API Key 管理。
- 中英文文案与错误恢复。

退出条件：Web 端端到端场景和敏感操作矩阵通过。

### Phase 3：管理员后台配置（已完成）

- 专用 SQLite 配置仓储、AES-GCM Client Secret 保护和审计。
- 环境/Web 整套来源解析、运行时不可变快照和无重启热切换。
- Discovery 检查、真实测试登录、管理员绑定和防锁死门禁。
- 仅管理员可见的“登录与认证”页面、中英文文案和恢复说明。

退出条件：后台配置闭环、安全及并发测试通过，现有环境托管部署保持兼容。详细拆分见 [OIDC Web 管理实施计划](./OIDC_WEB_ADMIN_IMPLEMENTATION_PLAN.md)。

### Phase 4：文档与真实 Provider 验收（1–2 人日）

- 配置、Docker、反向代理、Provider 注册说明。
- Keycloak/Authentik/Authelia 至少两个真实烟测。
- 故障恢复、secret 轮换和 break-glass 演练。

退出条件：Web OIDC 达到可发布状态。

### Phase 5：Flutter（额外 4–8 人日）

- 完成原生流程 ADR 和组件验证。
- Android/iOS 回调配置和系统浏览器流程。
- Session 兑换、CookieJar、取消/离线恢复。
- macOS/Windows/Linux/Web 按组件能力分别验收。

现有 Web OIDC Phase 0–3 已实现。真实 Provider 验收仍需 **1–2 人日**；Flutter 仍按额外 **4–8 人日**估算。

## 17. 发布与回滚

- `OIDC_ENABLED=false` 且未请求关闭密码登录时，新代码完全旁路，现有行为不变；`OIDC_DISABLE_PASSWORD_LOGIN=true` 与关闭 OIDC 的组合会被拒绝并保持密码入口 fail-closed。
- 先以 `password + oidc` 双模式发布，不直接切成 OIDC-only。
- 数据迁移只新增表和列，不删除密码或旧 Session。
- 回滚旧版本前必须确认旧二进制能忽略新增表；新增 Session 列应提供默认值。
- OIDC 配置错误不能阻止数据库、Web 和本地登录启动。
- 发布后观察登录成功/失败类型、Provider 延迟和 callback 错误率，但不记录身份 Token。

## 18. 开工前需要锁定的配置值

这些值不阻塞总体设计，但在 Phase 0 必须确定：

1. 首个用于验收的 Provider 及 issuer URL。
2. 是否将 Go 基线提升到 1.25；若不提升，接受固定旧兼容版本的维护成本。
3. 生产 `PUBLIC_URL` 和是否使用 `BASE_PATH`。
4. 是否允许自动创建用户；若允许，允许范围如何控制。
5. 首次管理员采用本地 break-glass 还是显式 subject allowlist。
6. OIDC Session 绝对期限。
7. Flutter 是否属于首发阻塞项，以及首发平台列表。
8. 是否在第一版要求 Provider 单点退出；默认不阻塞。

## 19. 完成定义

只有同时满足以下条件才可声明 OIDC 完成：

- 标准和安全验收矩阵全部通过。
- OIDC 用户仍具有独立本地 `User.id`、阅读数据和书库权限。
- 密码、本地 Session、Bearer API Key 和 OPDS 全部回归通过。
- OIDC-only 用户可通过 recent reauth 安全创建 API Key。
- Provider 不可用、回调取消和服务重启不会破坏本地恢复路径。
- Web 及声明支持的 Flutter 平台完成真实 Provider 登录。
- 配置、代理、HTTPS、回滚和管理员恢复文档齐全。
