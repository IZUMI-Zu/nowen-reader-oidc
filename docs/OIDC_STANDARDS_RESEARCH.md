# NowenReader OIDC 标准与成熟组件调研

> 调研日期：2026-08-10
> 范围：Go/Gin 后端、React Web、Flutter 多平台客户端
> 来源原则：只使用 OpenID Foundation Final Specification、IETF RFC、Go/Dart/Flutter 组件官方文档或官方仓库。

## 结论摘要

1. 第一版应采用 **OpenID Connect Authorization Code Flow**，`response_type=code`，同时对 Web confidential client 和 Flutter public native client 使用 **PKCE S256**。不要实现 Implicit Flow，也不要把用户的 IdP 密码交给 NowenReader。
2. Web 端应采用后端托管的 OIDC 流程：React 只跳转到 NowenReader 的登录入口；Go 后端负责 Discovery、授权码交换、ID Token 验证和创建现有本地 Session。浏览器不接触 IdP Access Token/Refresh Token。
3. 身份唯一键必须是 **`(issuer, subject)`，即 `(iss, sub)`**。`email`、`preferred_username`、`name` 只能作为可变展示属性，不能当身份主键，也不能默认用于自动合并现有账号。
4. Go 端应复用 `github.com/coreos/go-oidc/v3/oidc` 和 `golang.org/x/oauth2`，不自行实现 JWT/JWK、Discovery、Token Exchange 或 PKCE。
5. Flutter 需要注册为 **public native client**，不能内置 `client_secret`。必须使用系统浏览器或平台认证会话，而不是 WebView；回调优先使用 claimed HTTPS App Link/Universal Link，无法使用时才采用反向域名的 private-use scheme，桌面端也可使用 IP literal loopback redirect。
6. `flutter_web_auth_2` 和 `app_links` 都只是浏览器/回调运输组件，不是 OIDC 验证器。`flutter_web_auth_2` 在 Windows/Linux 默认使用 WebView，且其外部浏览器模式文档限制为 `localhost` 回调，因此不能未经额外验证就作为全平台 RFC 8252 方案。`app_links` 配合项目已经使用的 `url_launcher` 更适合统一的“外部浏览器 + app callback”运输层；所有 OIDC 交易状态和验证仍应放在 Go 后端。
7. Device Authorization Grant（RFC 8628）不适合当前具有浏览器能力的 Flutter 客户端，第一版不做。RP-Initiated Logout 已是 OpenID Foundation Final Specification，但依赖 Provider 是否发布 `end_session_endpoint`；本地退出必须始终可用，上游单点退出作为能力协商后的增强。
8. 实施前仓库声明 Go 1.23，而 `go-oidc` v3.20.0 要求 Go 1.25。本次实现已将项目、Docker 和 CI 基线统一提升至 Go 1.25，并固定使用 `go-oidc` v3.20.0 与 `x/oauth2` v0.36.0；因此不需要自写 JWT/JWK，也没有采用旧组件兼容路径。

严格说，OpenID Connect Core/Discovery/Logout 是 OpenID Foundation 的 Final Specifications，不是 IETF RFC；它们与 OAuth 2.0 Security BCP、PKCE、Native Apps 等 RFC 共同构成本项目应遵循的标准基线。

## 1. 标准基线

第一版的规范集合应固定为：

- [OpenID Connect Core 1.0 incorporating errata set 2](https://openid.net/specs/openid-connect-core-1_0.html)：认证请求、Authorization Code Flow、ID Token、UserInfo 与验证规则。
- [OpenID Connect Discovery 1.0 incorporating errata set 2](https://openid.net/specs/openid-connect-discovery-1_0.html)：通过 Issuer 获取 Provider Metadata、端点和 JWKS。
- [RFC 9700: Best Current Practice for OAuth 2.0 Security](https://www.rfc-editor.org/rfc/rfc9700.html)：当前 OAuth 安全基线，取代只按 RFC 6749 最低要求实现的做法。
- [RFC 7636: Proof Key for Code Exchange](https://www.rfc-editor.org/rfc/rfc7636.html)：PKCE，必须使用 `S256`。
- [RFC 8252: OAuth 2.0 for Native Apps](https://www.rfc-editor.org/rfc/rfc8252.html)：Flutter 原生客户端的系统浏览器、public client 和 redirect URI 规则。
- [OpenID Connect RP-Initiated Logout 1.0](https://openid.net/specs/openid-connect-rpinitiated-1_0.html)：Provider 支持时的上游退出。
- [RFC 8628: OAuth 2.0 Device Authorization Grant](https://www.rfc-editor.org/rfc/rfc8628.html)：只保留为未来无合适浏览器设备的扩展，不进入 v1。

RFC 9700 明确要求 public client 使用 PKCE，并建议 confidential client 也使用；只应使用不会在授权请求中暴露 verifier 的 `S256`。它同时建议停止使用向前端 URL 直接发 Token 的 Implicit Grant，并禁止 Resource Owner Password Credentials Grant。参见 [RFC 9700 §2.1.1、§2.1.2、§2.4](https://www.rfc-editor.org/rfc/rfc9700.html#section-2.1.1)。

## 2. v1 协议剖面

### 2.1 Discovery

配置只接受管理员提供的固定 `issuer`，v1 不允许匿名请求通过参数选择任意 Issuer。启动或首次使用时通过：

```text
{issuer}/.well-known/openid-configuration
```

取得 `authorization_endpoint`、`token_endpoint`、`jwks_uri`、签名算法以及可选的 `userinfo_endpoint`、`end_session_endpoint`。Discovery 响应中的 `issuer` 必须与用于发起 Discovery 的 Issuer **逐字符完全一致**，之后还必须与 ID Token 的 `iss` 完全一致；失败立即停止登录，不能通过“忽略 issuer 检查”兼容。[OpenID Connect Discovery §3、§4.3](https://openid.net/specs/openid-connect-discovery-1_0.html#ProviderConfigurationValidation)

OIDC Discovery Core 文档本身不定义 PKCE metadata 字段；PKCE 支持通常由 Authorization Server Metadata 的 `code_challenge_methods_supported` 表示。Provider 必须实际支持 `S256`，上线前应将其列为 Provider 兼容性门槛。RFC 9700 要求 Authorization Server 提供检测 PKCE 支持的方法，并指出 `S256` 是当前唯一不会暴露 verifier 的方法。[RFC 9700 §2.1.1](https://www.rfc-editor.org/rfc/rfc9700.html#section-2.1.1)

### 2.2 Authorization Request

每次登录都生成一组全新、不可预测、短时有效且只能消费一次的交易材料：

| 参数 | v1 要求 | 作用 |
|---|---|---|
| `response_type` | 固定 `code` | Token 只从 Token Endpoint 返回，不暴露给浏览器 URL |
| `scope` | 至少 `openid`；按需加 `profile email` | 没有 `openid` 就不是 OIDC 请求 |
| `client_id` | Web 与 Native 使用各自注册的 client | 不混用 confidential web client 与 public native client |
| `redirect_uri` | 与 Provider 预注册值精确匹配 | 防止授权码泄漏和 open redirect |
| `state` | CSPRNG、一次性、绑定发起登录的浏览器/App 交易 | 请求与 callback 关联、CSRF 防护 |
| `nonce` | CSPRNG、一次性、绑定同一交易 | ID Token replay/code injection 防护 |
| `code_challenge` | 由全新 verifier 计算 | PKCE |
| `code_challenge_method` | 固定 `S256` | 禁止 `plain` 和降级 |

OIDC Core 将 `state` 定义为推荐的 opaque value，并要求在返回时验证相等；`nonce` 必须具备足够熵，发送后必须与 ID Token claim 比对。[OIDC Core §3.1.2.1、§3.1.3.7](https://openid.net/specs/openid-connect-core-1_0.html#AuthRequest) RFC 9700 进一步要求 PKCE challenge 或 OIDC nonce 必须按交易生成并安全绑定到发起该交易的 client/user agent。[RFC 9700 §2.1.1](https://www.rfc-editor.org/rfc/rfc9700.html#section-2.1.1)

即使 PKCE 或 nonce 在特定攻击模型下能够提供 CSRF 保护，NowenReader 仍应同时使用 `state + nonce + PKCE S256`：它们由成熟库生成/编码，职责清晰，而且 `state` 还承载 callback 与本地登录交易的关联。交易记录应服务端保存，只向客户端暴露随机 `state`；至少保存 `state` 摘要、nonce、PKCE verifier、client 类型、redirect URI、创建时间、过期时间和消费状态。

### 2.3 Callback 与 Token Exchange

Callback 必须按以下顺序失败关闭：

1. 处理 Provider 返回的 OAuth error；不在错误页输出 code、Token、client secret、verifier 或完整回调 URL。
2. 根据 `state` 查找交易，校验发起端绑定、TTL 和未消费状态；原子地标记消费，阻止 callback/code replay。
3. 用同一交易保存的 `code_verifier` 调用 Token Endpoint；Web confidential client 同时按 Provider metadata 指定方式进行 client authentication，Native public client绝不发送静态 `client_secret`。
4. 从 Token Response 读取 `id_token`，交给 OIDC verifier；缺失即失败。
5. 验证 ID Token 后再创建/查找本地用户并签发 NowenReader Session；任何失败都不能创建半成品账号或 Session。

`redirect_uri` 必须精确注册，客户端不能提供任意 `return_to` 形成 open redirect。RFC 9700 要求 redirect URI 精确匹配、禁止 client 和 authorization server 暴露 open redirector，并要求除 Native loopback 外的授权响应使用 HTTPS。[RFC 9700 §2.1、§2.6](https://www.rfc-editor.org/rfc/rfc9700.html#section-2.1)

### 2.4 ID Token 必检项

`go-oidc` 可负责签名、Issuer、Audience、Expiry 和 JWKS key rotation 等核心工作，但应用仍必须补齐库明确不做或与业务策略有关的检查：

| 检查 | 要求 | 组件边界 |
|---|---|---|
| JWS 签名和允许算法 | 使用 Discovery 的 `jwks_uri` 与 Provider 宣告/项目允许的算法；拒绝 `none` | `go-oidc` verifier |
| `iss` | 与配置/Discovery Issuer 完全一致 | `go-oidc` verifier；不得启用 `SkipIssuerCheck` |
| `aud` | 必须包含当前 client ID；额外 audience 必须属于显式信任集合 | `go-oidc` 检查预期 audience；应用应对多 audience/`azp` 做额外策略检查 |
| `exp` | 当前时间早于 expiry，只允许很小的明确 clock skew | `go-oidc` verifier；不得启用 `SkipExpiryCheck` |
| `iat` | 可拒绝明显来自未来或过旧的登录 Token | 应用策略 |
| `nonce` | 必须存在、与该交易保存值相等、交易一次性 | **应用必须自行验证**；`go-oidc` 官方文档明确不代验 nonce |
| `sub` | 必须非空，和已验证的 `iss` 组合成身份键 | 应用 |
| UserInfo `sub` | 如果请求 UserInfo，必须与 ID Token `sub` 精确相同 | 应用/`Provider.UserInfo` 后补检 |

OIDC Core 的完整验证要求见 [§3.1.3.7](https://openid.net/specs/openid-connect-core-1_0.html#IDTokenValidation)。它要求精确检查 `iss`、`aud`、expiry 和发送过的 nonce；UserInfo 的 `sub` 不等于 ID Token `sub` 时，全部 UserInfo claims 都不得使用。[OIDC Core §5.3.2](https://openid.net/specs/openid-connect-core-1_0.html#UserInfoResponse)

`go-oidc` 官方 API 说明 `IDTokenVerifier.Verify` 会验证 Provider 签名并按配置执行检查，但 **不会验证 nonce**；`IDToken.Nonce` 只暴露 claim 供调用方检查。[go-oidc package documentation](https://pkg.go.dev/github.com/coreos/go-oidc/v3/oidc#IDTokenVerifier.Verify)

### 2.5 Session 与 Token 边界

- Web：Go 后端完成整个 OIDC 流程，最后只给浏览器现有的 NowenReader opaque session cookie。Cookie 应为 `HttpOnly`、生产环境 `Secure`、合适的 `SameSite` 和限定 Path；不要把 ID Token/Access Token 放进 `localStorage`、URL 或 React state。
- 如果 NowenReader 登录后不调用 Provider API，就不需要长期保存 Access Token/Refresh Token。最小化 scope，不默认请求 `offline_access`。
- 如实现 RP-Initiated Logout 并需要 `id_token_hint`，只在服务端 session/加密存储中保留所需 Token，并与本地 Session 同寿命或更短；日志不得输出。
- Flutter 登录完成后仍换成现有 NowenReader Session，而不是让所有业务 API 改为接收第三方 Access Token。这样书库权限、阅读进度与本地 `User.id` 继续保持单一授权模型。

## 3. 身份绑定与 Email 限制

外部身份表的规范语义应是：

```text
unique(issuer, subject) -> local_user_id
```

OIDC Core 明确指出，只有 ID Token 的 `iss + sub` 组合是 RP 可依赖的稳定唯一身份；Issuer 可重新分配 email，用户的 email 也可以变化，`email`、`phone_number`、`preferred_username`、`name` 都不得作为唯一标识。[OIDC Core §5.7](https://openid.net/specs/openid-connect-core-1_0.html#ClaimStability)

因此：

- 默认不按 email 自动绑定已有本地账号。
- `email_verified=true` 只表示 Provider 在某个时间点采取措施确认用户当时控制该邮箱；具体校验强度取决于 Provider 的信任框架，并不保证 email 永不变、跨 Issuer 唯一或今后不会被回收。[OIDC Core §5.1](https://openid.net/specs/openid-connect-core-1_0.html#StandardClaims)
- 如需迁移合并，使用“用户先通过现有本地登录，再主动绑定已验证 OIDC 身份”或管理员审核流程；不要用首次碰到相同 email 的方式静默合并。
- `preferred_username` 只能用于生成本地显示名候选；需要冲突处理，不能改变外部身份主键。
- Provider 改 client registration/sector identifier 时，pairwise `sub` 可能变化；这属于显式迁移事件，不能靠 email 猜测恢复。

## 4. 成熟 Go 组件选择

### 4.1 `coreos/go-oidc/v3`

推荐职责：

- `oidc.NewProvider(ctx, issuer)`：Discovery；
- `provider.Endpoint()`：生成 `oauth2.Config` 端点；
- `provider.Verifier(&oidc.Config{ClientID: ...})`：验证签名、Issuer、Audience、Expiry；
- `provider.UserInfo(...)`：按需获取 claims；
- Remote JWK Set 缓存与遇到未知 key id 后刷新。

官方文档特别警告 issuer validation 对多租户 Provider 很关键；不得为了兼容配置错误而使用 `InsecureIssuerURLContext`、`SkipIssuerCheck`、`SkipClientIDCheck`、`SkipExpiryCheck` 或 `InsecureSkipSignatureCheck`。[go-oidc API](https://pkg.go.dev/github.com/coreos/go-oidc/v3/oidc)

### 4.2 `golang.org/x/oauth2`

推荐职责：

- `oauth2.Config.AuthCodeURL` 与 `Exchange`；
- `oauth2.GenerateVerifier()`；
- 授权请求使用 `oauth2.S256ChallengeOption(verifier)`；
- Token Exchange 使用 `oauth2.VerifierOption(verifier)`。

官方包文档要求每次授权生成新的 verifier，并明确 `Exchange` 前/交易处理中必须验证 state。[golang.org/x/oauth2 API](https://pkg.go.dev/golang.org/x/oauth2)

两者不会替应用保存 state/nonce/PKCE transaction、绑定本地用户、创建 Session、执行多 audience/`azp` 策略或防止交易重放；这些是 NowenReader 需要实现的薄业务层，而不是重新实现协议加密原语。

### 4.3 Go 版本兼容决策

实施前仓库 `go.mod` 是 Go 1.23，本机调研环境是 Go 1.26.3。组件官方 module metadata 显示：

- 当前 `go-oidc` v3.20.0 的 `go.mod` 要求 Go 1.25，并依赖 `x/oauth2` v0.36.0。[v3.20.0 go.mod](https://raw.githubusercontent.com/coreos/go-oidc/v3.20.0/go.mod)
- `go-oidc` v3.16.0 release note 明确移除 Go 1.23 支持；v3.15.0 的 `go.mod` 仍声明 Go 1.23，并依赖 `x/oauth2` v0.28.0。[go-oidc releases](https://github.com/coreos/go-oidc/releases)、[v3.15.0 go.mod](https://raw.githubusercontent.com/coreos/go-oidc/v3.15.0/go.mod)
- `x/oauth2` v0.28.0 声明 Go 1.23。[v0.28.0 go.mod](https://raw.githubusercontent.com/golang/oauth2/v0.28.0/go.mod)

最终采用方案：

1. **已采用**：项目最低 Go、Docker builder 与 CI 提升到 1.25，固定 `go-oidc` v3.20.0 + `x/oauth2` v0.36.0。
2. **未采用的兼容路径**：若必须保留 Go 1.23，才固定 `go-oidc` v3.15.0 + `x/oauth2` v0.28.0，并记录旧 Go 基线带来的安全维护债务。

不要为了只加 OIDC 而引入第二套 JWT/JWK 库或自己解析 JWT payload 后直接信任 claims。

## 5. Web 集成边界

React 不需要 OIDC SDK。建议端点模型：

```text
GET  /auth/oidc/login     -> 创建交易并 302 到 Provider
GET  /auth/oidc/callback  -> 验证 state、换码、验证 ID Token、创建本地 Session
POST /auth/logout         -> 永远先撤销本地 Session
GET  /auth/oidc/logout    -> Provider 支持时执行 RP-Initiated Logout
```

登录页只根据后端公开的能力信息显示“使用 SSO 登录”按钮。Callback 是 Go 后端路由，不是 React route。授权码流的 Token 都从 Token Endpoint 返回，而不是 User Agent；这正是 OIDC Core 对 Authorization Code Flow 的定义和主要安全收益。[OIDC Core §3.1](https://openid.net/specs/openid-connect-core-1_0.html#CodeFlowAuth)

## 6. Flutter / Native Apps

### 6.1 RFC 8252 不可妥协项

- Native app 是 public client，分发到安装包中的 `client_secret` 不能视为秘密。
- 必须用 Authorization Code + PKCE。
- 必须使用外部 user-agent（系统浏览器或共享安全上下文的平台认证会话），不得用 app 可读取页面内容/cookie 的 embedded WebView。
- claimed HTTPS redirect（Android App Links / iOS Universal Links）能由操作系统证明目标 App，优先于 private-use scheme。
- private-use scheme 应使用应用控制域名的反向域名形式，且 PKCE 仍为强制。
- 桌面 loopback 使用 `http://127.0.0.1:{ephemeral_port}/...` 或 IPv6 literal；RFC 8252 不推荐 `localhost`，避免名称解析与错误监听网卡问题。

依据见 [RFC 8252 §4、§6、§7、§8](https://www.rfc-editor.org/rfc/rfc8252.html#section-4)。

### 6.2 推荐的 Native 流程边界

为了复用 Go 端的 OIDC 验证和现有 Cookie Session，同时满足 Native redirect/PKCE 要求，可将 public native client 的服务端组件放在 NowenReader：

```text
Flutter 请求 Native login transaction
  -> Go 生成并保存 state + nonce + PKCE verifier，返回 authorization URL
  -> Flutter 用系统浏览器打开 URL
  -> Provider 将 code + state 重定向回已注册的 App/loopback URI
  -> Flutter 把原始 code + state + transaction 结果 POST 给同一 NowenReader 实例
  -> Go 原子验证 state/交易并用保存的 verifier 换码
  -> Go 用 native client_id 验证 ID Token 和 nonce
  -> Go 创建现有 NowenReader Session，响应让 Dio CookieJar 保存 Cookie
```

Web confidential client 与 Native public client 必须使用不同 client registration/config。Native Token Request 不发送 `client_secret`。授权码仍直接回到 Native redirect URI，PKCE 保证截获 code 的其他 App 没有 verifier；Provider Token 只在 Go 后端短暂出现，Flutter 最终只持有 NowenReader Session。

这是一种 client 被拆为 App 与其自有后端组件的部署方式；需要在目标 Provider 上做互操作测试，确认 public client 的 code 可由 NowenReader 后端携带同一 verifier/redirect URI 兑换。如果某 Provider 不接受这种部署，回退到 AppAuth 在客户端完成换码，再由后端用一次性 nonce 重新验证 ID Token 的方案，但不能退回 password grant 或移除 PKCE。

### 6.3 Flutter 组件评估

#### `app_links` + `url_launcher`

`app_links` 官方页面声明支持 Android App Links、iOS Universal Links、private custom schemes，并覆盖桌面平台；它还要求尽早实例化以接住 cold-start initial link。[app_links package](https://pub.dev/packages/app_links) 项目已经依赖 Flutter 官方 `url_launcher`，可用外部应用模式打开系统浏览器。

适用职责只有：

- 打开后端生成的 authorization URL；
- 接收 Provider 回到 app 的 redirect URI；
- 把未解释的 callback 参数交给 Go 后端。

它不提供 Discovery、PKCE、state/nonce、Token Exchange 或 ID Token 验证。需要避免 `go_router` 与 plugin 同时消费同一 initial link；只接受固定 scheme/host/path 的 OIDC callback，其他 URL 不得进入登录完成逻辑。

#### `flutter_web_auth_2`

官方仓库说明 iOS/macOS 使用系统 Web Authentication Session、Android 使用 Auth Tab，并能捕获 callback，因此在这些平台可作为 auth browser transport。[flutter_web_auth_2 repository](https://github.com/ThexXTURBOXx/flutter_web_auth_2)

但不能把它直接视为全平台 RFC 8252 组件：

- 它不验证 OIDC、state、nonce 或 Token；README 示例甚至把 callback query 中的值直接当 token，NowenReader 不得照抄这种简化示例。
- 4.x 之后 Windows/Linux 默认使用 WebView，违反 RFC 8252 对 embedded user-agent 的禁止；必须显式 `useWebview: false`。
- 官方 README 又说明 Windows/Linux 的外部浏览器模式要求 callback 以 `http://localhost:{port}` 开头，而 RFC 8252 推荐 IP literal、明确不推荐 `localhost`。
- 稳定 5.x 官方迁移说明要求 Dart SDK 至少 3.5，而仓库当前声明 `>=3.2.0 <4.0.0`；如果采用当前稳定版，必须先确定并提升受支持的 Flutter/Dart 基线。

因此它可以作为 Android/iOS/macOS 的实现候选，但不能未经 spike 和真机/桌面验证就承诺为全平台统一方案。

#### `flutter_appauth`

`flutter_appauth` 是 Android/iOS/macOS 原生 AppAuth SDK 的 Flutter bridge，官方文档展示了 Discovery、Authorization Code + PKCE、分离 authorize/exchange 以及 End Session；`AuthorizationResponse` 会返回 AppAuth 生成的 code verifier 和 nonce。[flutter_appauth package](https://pub.dev/packages/flutter_appauth)、[AuthorizationResponse API](https://pub.dev/documentation/flutter_appauth/latest/flutter_appauth/AuthorizationResponse-class.html)

它是支持平台上最完整的 Native OIDC 复用组件，但当前不覆盖 Windows、Linux、Web；最新 12.0.2 还要求比仓库当前声明更高的 Dart/Flutter 基线。[flutter_appauth pubspec](https://raw.githubusercontent.com/MaikuB/flutter_appauth/master/flutter_appauth/pubspec.yaml) 如果产品第一阶段只要求 Android/iOS/macOS，可优先采用；若要求现有六平台同时交付，则仍需要独立的桌面 browser/callback 方案。

### 6.4 Flutter 依赖兼容结论

当前环境没有安装 `flutter` 命令，因此无法在本次调研中执行真实 `pub get`。仓库 `pubspec.yaml` 声明 Dart `>=3.2.0 <4.0.0`，而上述组件当前主版本已经提高最低 Dart/Flutter 要求。实施前必须先做一个只解析依赖与跑平台构建的 spike，然后固定经验证版本；不要只因为包名成熟就直接使用最新版或降低平台最低版本而不做构建矩阵。

## 7. Device Authorization Grant

RFC 8628 明确说明 Device Grant 面向缺少合适浏览器或输入能力的设备，例如电视、主机、打印机；它“不用于替代”智能手机等有浏览器能力的 Native App 流程，这些客户端应采用 RFC 8252。[RFC 8628 §1](https://www.rfc-editor.org/rfc/rfc8628.html#section-1)

因此 v1 不做 Device Flow。未来只有在增加 TV/无浏览器客户端且 Provider metadata 明确发布 `device_authorization_endpoint` 与对应 grant 时再启用。`golang.org/x/oauth2` 已有 `DeviceAuth`、`DeviceAccessToken`，届时仍应复用库并正确处理 `authorization_pending`、`slow_down`、`interval`、expiry 和超时退避，不自行写无限轮询。

## 8. Logout 状态

RP-Initiated Logout 1.0 已是 Final Specification。Provider 支持时，会在 Discovery metadata 发布 `end_session_endpoint`；RP 将 User Agent 导向该端点，可带：

- `id_token_hint`（推荐）；
- 预注册的 `post_logout_redirect_uri`；
- logout `state`，返回后必须验证。

详见 [RP-Initiated Logout §2、§3](https://openid.net/specs/openid-connect-rpinitiated-1_0.html#RPLogout)。

v1 行为应是：

1. 用户退出时先撤销 NowenReader 本地 Session，保证 Provider 不支持/不可用时也能退出本应用。
2. 只有 Discovery 提供 `end_session_endpoint` 且 post-logout URI 已在 Provider 注册时，才显示或执行“同时退出 SSO”。
3. 不把任意前端 return URL转发给 Provider；只使用固定 allowlist。
4. RP-Initiated Logout 只解决主动上游退出，不等于 Provider 主动通知 RP 的 Front/Back-Channel Logout。后者可单独作为第二阶段；当前 `go-oidc` v3.19+ 已有 Back-Channel Logout token verifier，但这不是 v1 登录所必需。

## 9. 验收与负向测试清单

实现不能只测“正确账号能登录”，至少覆盖：

- Discovery issuer 与配置不一致、尾斜杠不一致、JWKS 拉取失败；
- Provider 不支持 `code` 或 `S256`；
- state 缺失、不匹配、过期、跨浏览器/App 使用、并发重复 callback；
- nonce 缺失、不匹配、重复；
- PKCE verifier 错误、丢失、降级为 `plain`；
- code 被二次兑换；
- ID Token 签名错误、未知 key/正常 key rotation、`alg=none`、过期、future `iat`；
- `iss` 错误、`aud` 不含 client ID、额外未信任 audience、`azp` 错误；
- UserInfo `sub` 与 ID Token `sub` 不一致；
- 相同 email + 不同 `(iss, sub)` 不自动合并，`email_verified=false` 不参与绑定；
- 本地用户名冲突、首次 OIDC 用户、被禁用本地用户、已绑定身份重复登录；
- `return_to`/post logout URL 注入与 open redirect；
- Provider 超时、Token Endpoint 5xx、JWKS rotation 期间失败时不创建 Session；
- Web Cookie 在 HTTPS 下具备 `Secure`/`HttpOnly`/预期 `SameSite`；
- Flutter 前台、后台、cold start callback，用户取消浏览器，callback 被其他 App 截获后 PKCE 拒绝；
- Android App Link、iOS Universal Link、private scheme 与桌面 callback 的真实平台测试；
- Provider 不支持 logout 时本地退出仍成功，logout state 重放被拒绝。

## 10. 对规划的直接建议

可将实施拆为以下有明确标准门槛的阶段：

1. **Go 依赖与 Provider compatibility spike**：决定 Go 1.25 升级；用目标 IdP 验证 Discovery、`code`、PKCE S256、签名算法、Native public client 和 logout metadata。
2. **Web OIDC**：单 Issuer、后端 Code Flow + PKCE、`iss+sub` 外部身份表、现有 Session、本地管理员恢复登录。
3. **账号生命周期**：首次创建、显式绑定/解绑、冲突处理、禁用用户、API Key 重新认证策略；默认禁止 email 自动合并。
4. **Flutter 平台 spike**：先决定目标平台和最低 Flutter/Dart；验证 `app_links + url_launcher`、Native public client 后端换码以及真实 callback 生命周期。只有 spike 通过的平台进入交付承诺。
5. **Logout 与高级能力**：能力协商后的 RP-Initiated Logout；group/role claim、Back-Channel Logout、Device Flow 均不进入最小 v1。

该顺序把 RFC/OIDC 校验集中在成熟 Go 组件和少量可审计的交易层中，React 与 Flutter 不重复实现 JWT/JWK/Discovery，也不会用 IP 或 email 代替用户身份。
