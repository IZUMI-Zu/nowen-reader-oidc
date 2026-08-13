# OIDC 行级安全审计

> 范围：本次修复涉及的行级分析，以及 OIDC 认证链路上安全关键行的逐行核对。
> 状态：基于 `fix/oidc-trusted-proxy` 分支（HEAD `ba4fc34` + 本次修复）。
> 符号：✅ 有测试覆盖 ｜ ⚠️ 部分/间接覆盖 ｜ ➖ 无法/不应单测（需真实 Provider 或刻意不加） ｜ 🔧 本次已修复

---

## 1. `internal/middleware/security.go`（54 行，🔧 已改）

| 行 | 内容 | 安全属性 / 结论 | 测试 |
|---|---|---|---|
| 3–5 | 移除 `strings` import | 修复后 `strings` 不再使用，保持编译干净 | go build/vet ✅ |
| 7–9 | `SecurityHeaders` 入口 | 统一安全头中间件 | — |
| 10–33 | 静态头（nosniff / SAMEORIGIN / XSS / Referrer / Permissions / CSP） | 基线加固。**注意**：CSP `style-src 'unsafe-inline'` 为 Tailwind 所需；缺 `object-src 'none'`、`base-uri` 属可选加固，非本次范围 | ⚠️ 无专门断言 |
| 35–39 | **HSTS 由 `IsRequestSecure` 门控** | 🔧 修复点：旧代码 `strings.Contains(XFP,"https")` 会让直接客户端伪造头注入 HSTS；现在只在可信代理/直连 TLS 下发 | ✅ `TestSecurityHeadersHSTSPolicy` |
| 41 | `c.Next()` | 继续链路 | — |
| 45–54 | `RateLimitHeaders` | 占位（真实限流在 `ratelimit.go`），无安全影响 | — |

---

## 2. `internal/middleware/auth.go`（🔧 已改）

### 2.1 Session 校验与续期（273–297，未改）

| 行 | 内容 | 结论 | 测试 |
|---|---|---|---|
| 276 | `ExpiresAt` 与 `AbsoluteExpiresAt` 双重过期判断 | OIDC 绝对上限 + 滑动过期并存，防“永久续期” | ✅ `auth_session_test.go` 两用例 |
| 284–289 | 剩余 <7 天才续期；`newExpiry` 被 `AbsoluteExpiresAt` 封顶 | 滑动续期不能突破 OIDC 绝对期限 | ✅ `TestOIDCSlidingRenewalIsCappedAtAbsoluteExpiry` |
| 291–294 | OIDC 会话续期时的 Secure 决策：优先取**签发时固化的** `CookieSecure`，回退才用 `IsRequestSecure` | 防代理伪造头降级已签发 OIDC 会话 cookie | ✅ `TestOIDCSessionRenewalPreservesIssuedSecureCookiePolicy`、`TestLegacyOIDCSessionRenewalDerivesSecureCookieFromRequestOrigin` |

### 2.2 🔧 `IsRequestSecure`（335–343）

| 行 | 内容 | 结论 |
|---|---|---|
| 336–338 | 直连 TLS → 恒 true | 即使带伪造头，TLS 优先，正确 |
| 339–341 | `!config.TrustProxyHeadersFrom(c.Request.RemoteAddr)` → false | **修复核心**：只信“直连对端是显式可信代理”的 `X-Forwarded-Proto`；否则任何直接客户端可伪造 |
| 342 | `forwardedProtoIsHTTPS(XFP)` | 交给纯解析函数 |

✅ 覆盖：`TestIsRequestSecure`（14 子用例，含直连 TLS、伪造头拒绝、可信代理放行、非可信对端拒绝、CIDR、最左值、大小写、子串陷阱）。

### 2.3 🔧 `forwardedProtoIsHTTPS`（348–353）

| 行 | 内容 | 结论 |
|---|---|---|
| 349–351 | 取最左侧逗号分隔值（客户端侧协议） | 符合 `X-Forwarded-Proto` 语义：左端是原始客户端协议 |
| 352 | `strings.EqualFold(strings.TrimSpace(value), "https")` | 精确匹配，拒绝 `https://`、`xhttpsx` 等旧 `contains` 的误判 |

✅ 覆盖：`TestForwardedProtoIsHTTPS`（12 子用例）。

### 2.4 Cookie 签发（365–391，未改）

| 行 | 结论 | 测试 |
|---|---|---|
| 365–372 | `SetSessionCookie` 不设 Secure——**文档已声明的 LAN/NAS 历史取舍**，非本次修复范围（对外 HTTPS 暴露时应评估） | ⚠️ 无直接断言 |
| 374–391 | `SetSessionCookieWithOptions`：SameSite=Lax、HttpOnly、Path=BasePath；OIDC 用 `secure` 参数 | ✅ OIDC 会话 cookie 断言见 `oidc_handler_test.go` |

---

## 3. `internal/auth/oidc/provider.go`（未改）

### 3.1 `NewRemoteProvider`（42–76）

| 行 | 内容 | 结论 |
|---|---|---|
| 45–47 | issuer/client_id/secret/redirect 必填 | 缺省即失败 |
| 48–52 | issuer 必须绝对 HTTPS（仅 loopback 允许 http），禁 userinfo/query/fragment | 防 issuer 解析歧义 |
| 53–58 | 默认 scope，强制补 `openid` | OIDC 请求必要条件 |
| 59–75 | 15s 超时 + `providerStatusTransport`（≥400 → 错误） | 网络与 Provider 5xx 统一为“不可用” |

### 3.2 `Exchange`（112–173，最高风险）

| 行 | 内容 | 结论 | 测试 |
|---|---|---|---|
| 119 | `oauthConfig.Exchange(code, VerifierOption(codeVerifier))` | PKCE S256 回传 verifier，防授权码被截获后冒用 | ⚠️ 间接（`provider_test.go` 成功/失败路径） |
| 126–129 | 强制 Token 响应含 `id_token` | 缺即失败 | ✅ `provider_test.go` |
| 130 | `verifier.Verify` | 签名 / `iss` / `aud` / `exp` / 算法 | ✅（多组错误签名/过期用例） |
| 137–141 | 存在 `at_hash` 时 `VerifyAccessToken` | 防 access token 与 ID token 绑定被篡改 | ✅ `TestRemoteProviderRejectsMismatchedAccessTokenHash` |
| 154–159 | 多 audience 无 `azp` → 拒绝；`azp`≠client_id → 拒绝 | **比规范 SHOULD 更严**（防 token 转发到别的 client） | ✅ `TestRemoteProviderEnforcesAuthorizedPartyForMultipleAudiences` |
| 160–172 | 归一化 VerifiedIdentity（含 `auth_time`） | 身份材料 | ⚠️ |

### 3.3 `validateDiscoveredEndpoints`（264–295）

| 行 | 内容 | 结论 |
|---|---|---|
| 265–272 | 解析 3 个 endpoint 元数据 | 缺即失败 |
| 277 | `allowLoopbackHTTP` | 仅 loopback dev 允许 http |
| 278–293 | 端点必须绝对 HTTPS（或 loopback http），禁 userinfo/fragment | 防 `http://` 明文端点 / 内部地址注入 |

➖ **未做端点 host 与 issuer 一致性校验**：`go-oidc` `NewProvider` 已内部校验 discovery `issuer` 与请求 issuer 逐字符一致（未用 `InsecureIssuerURLContext`），且 ID Token `iss` 又被应用二次比对；再加同域校验会破坏 Google 等多域 IdP。**刻意不加**，见 `docs/OIDC.md` 与主审计结论。

### 3.4 `ensure`（211–262）

| 行 | 结论 |
|---|---|
| 214–217 | 命中缓存直接返回（快路径） |
| 219–228 | `discovering` channel 单飞，避免并发重复 Discovery |
| 233–248 | 惰性 Discovery + 校验端点 + 构造 oauth2.Config / verifier |
| 250–260 | 失败关闭 `discovering`，下次重试 |

✅ 并发/失败路径：`provider_test.go` 多组。

---

## 4. `internal/auth/oidc/service.go`（未改）

### 4.1 `Begin`（156–203）

| 行 | 内容 | 结论 |
|---|---|---|
| 157–160 | purpose 合法性 + 会话绑定约束（login 无 session user；config_test 必须有 session id） | 防 purpose 越权 |
| 161–164 | `validateReturnTo` | 防 open redirect |
| 165–177 | `randomToken(32)`×3 + `oauth2.GenerateVerifier` | 256bit CSPRNG state/nonce/binding + PKCE verifier |
| 184–201 | 存 `StateHash`/`BindingHash`（sha256）、`Nonce`、`PKCEVerifier`、purpose、会话绑定、TTL、config revision/fingerprint | 交易服务端化，浏览器只拿到随机 state |

### 4.2 `Complete`（205–242）

| 行 | 内容 | 结论 | 测试 |
|---|---|---|---|
| 206–208 | state/code/binding 必填 | 缺即拒绝 | ✅ |
| 209–215 | **原子 `Consume`**（单次消费） | 授权码/回调重放防护 | ✅ `service_test.go`、`oidc_store_test.go` |
| 220–222 | 交易 fingerprint 与当前配置比对 | 配置变更后旧交易作废 | ✅ `TestComplete...ConfigurationChanged` |
| 223–229 | 换码 | — | ⚠️ |
| 230–232 | `iss`==配置 issuer、`sub` 非空、`nonce` **常量时间**相等 | nonce 防 code injection / replay；iss 防 mix-up | ✅ `TestCompleteRejectsNonceOrIssuerMismatchAfterConsumingTransaction` |
| 233–239 | reauth/config_test 强制 `auth_time` 新鲜（`max_age=0`）+ 2min 偏移 | 确保是“刚刚”真实认证 | ✅ `service_test.go` auth_time 用例 |

### 4.3 `Cancel`（247–262）

| 行 | 结论 |
|---|---|
| 248–261 | 消费交易但不换码，供 Provider 返回 OAuth error 时安全回到本地页；同样校验 fingerprint |

### 4.4 `validateReturnTo`（268–295）

| 行 | 结论 |
|---|---|
| 275–277 | 拒绝 `\`、`//` 前缀 |
| 278–280 | 拒绝绝对 URL / host / userinfo / fragment / 非 `/` 前缀 |
| 282–290 | 解码后拒绝反斜杠、控制字符、`.`/`..` 段 |
| 291–293 | 强制在 `basePath` 之内 |

✅ `service_test.go` returnTo 用例、`oidc_handler_test.go`。

### 4.5 常量时间与哈希（297–315）

| 行 | 结论 |
|---|---|
| 297–303 | `randomToken` = `crypto/rand` + RawURL base64 |
| 305–308 | `hashToken` = sha256 hex（只存摘要） |
| 310–315 | `constantTimeEqual` = `subtle.ConstantTimeCompare`（nonce/fingerprint 比较防时序侧信道） |

---

## 5. `internal/store/oidc_store.go`（未改）

### 5.1 `Consume`（147–186，关键原子性）

| 行 | 内容 | 结论 |
|---|---|---|
| 148–152 | 开事务 | — |
| 153–157 | `UPDATE ... SET consumedAt=? WHERE stateHash=? AND bindingHash=? AND consumedAt IS NULL AND expiresAt>?` | **单条 SQL 内完成“匹配 + 未消费 + 未过期”三重校验并标记**，天然防并发重放 |
| 165–167 | `rows != 1` → 拒绝 | 重复/过期/不匹配统一拒绝 |
| 169–181 | 同事务按 `stateHash` 读回（已通过 bindingHash 匹配） | — |
| 182–184 | commit | — |

✅ `TestOIDCTransactionStoreConsumesBrowserBoundStateOnce`、`TestOIDCTransactionStoreRejectsExpiredStateAndCleansOldRows`。

### 5.2 身份绑定/解绑（43–145）

| 行 | 结论 | 测试 |
|---|---|---|
| 55–65 | 校验 user 存在 | ✅ |
| 67–84 | `(iss,sub)` 已属他人 → `ErrOIDCIdentityAlreadyLinked`；同人则仅刷新资料 | ✅ `manager_test.go`/`oidc_store_test.go` |
| 86–92 | 同一用户同一 issuer 已有别的 sub → 拒绝（`unique(userId,issuer)`） | ✅ |
| 117–132 | 解绑要求“密码登录可用 **且** 用户有本地密码”，否则 `ErrOIDCWouldLockOut` | 防最后一个登录方式被移除 | ✅ |
| 133–144 | 删除后 `RowsAffected==0` → `sql.ErrNoRows` | — | ✅ |

数据库层唯一约束在 `internal/store/db.go` L196–197（`issuer+subject`、`userId+issuer` 两个唯一索引），是应用层防重之外的第二道防线。

### 5.3 `ResolveOIDCLogin`（204–279）

| 行 | 结论 |
|---|---|
| 205–207 | issuer/sub 非空校验 |
| 214–230 | **仅按 `(iss,sub)` 解析**，绝不按 email/username 合并；命中则刷新资料 |
| 231–233 | 未开 auto-provision → `ErrOIDCIdentityNotLinked` |
| 235–241 | 库内 0 用户且不在 bootstrap allowlist → 拒绝（防“首个登录者成管理员”） |
| 247–250 | 首管理员仅当 0 用户 + subject 精确命中才授予 admin |
| 294–328 | username 冲突安全分配（sanitize + 稳定后缀），不改变身份主键 |

✅ `manager_test.go`、`oidc_store_test.go` 覆盖首管理员与冲突路径。

---

## 6. `internal/auth/oidcruntime/secret.go`（未改）

| 行 | 内容 | 结论 |
|---|---|---|
| 33–49 | `NewAESGCMSecretProtector`：AES-256-GCM + 随机 nonce + keyID(sha256 前 8 字节) | 每次加密随机 nonce |
| 71–78 | `Encrypt`：`Seal(nil,nil,plaintext, AAD)` + `v1:keyID:cipher` 信封 | AAD 绑定用途，防密文跨用途复用 |
| 80–94 | `Decrypt`：校验版本/keyID，`Open` 失败即拒 | 篡改/错 key 均失败关闭 |
| 100–144 | 本地 key：`0600`、`O_EXCL`、`fsync`、并发竞态重读 | 防弱权限/并发覆盖 |

✅ `secret_test.go`：roundtrip、篡改拒绝、0600 权限、并发创建。

---

## 7. `internal/auth/oidcruntime/manager.go` — `Apply`（264–401，未改）

| 行 | 内容 | 结论 |
|---|---|---|
| 274–275 | 写锁（`updateMu.Lock`） | 配置热切换串行化 |
| 279–286 | `Load` + revision 比对 | 乐观锁防覆盖 |
| 294–302 | 重算 fingerprint；协议字段变化则清空 `VerifiedFingerprint` | 变更后必须重新测试登录 |
| 303–320 | **启用**前：配置有效 + 已验证 fingerprint + 管理员身份已绑定该 issuer | 防“启用即锁死” |
| 321–339 | **停用**前：管理员有本地密码 + OIDC-only 用户数确认 | 防把纯 OIDC 用户锁在门外 |
| 340–357 | **关密码登录**前：OIDC 已启用 + 管理员有密码 + 显式二次确认 | fail-closed |
| 375–397 | `Save` 携带 `RequireActorPassword` / `RequireActorIdentityIssuer` / `RequireNoOIDCOnlyUsersIssuer` | 见下，SQL 原子强制 |
| 391 | 审计写 `ChangedFields` | 可追溯 |

✅ `manager_test.go` 覆盖各闸门拒绝/确认路径；**原子性由 `oidc_config_store.go` Save（48–66）在单条 `UPDATE ... AND EXISTS(...)` 内强制**（TOCTOU 安全），`oidc_config_store_test.go` 有守卫用例。

---

## 8. `internal/handler/oidc.go`（未改）

| 行 | 内容 | 结论 |
|---|---|---|
| 109–133 | `OIDCLogin`：`Cache-Control:no-store` → 校验可用 → `Begin` → 设交易 cookie → 302 | 登录入口 |
| 181–264 | `OIDCCallback`：校验 state/binding cookie → 清 cookie → error 分支走 `Cancel` → 换码+finalize → 302 | 回调主链路 |
| 296–346 | `finalizeOIDCCallback`：login=按 `(iss,sub)` 解析+发 Session；link=绑定当前用户；reauth=校验身份归属+刷新 `authenticatedAt` | 会话副作用与身份策略 |
| 428–438 | `setOIDCTransactionCookie`：HttpOnly、SameSite=Lax、Secure 来自配置、Path 限定 `/api/auth/oidc` | 浏览器绑定 token 的 cookie 面 |

✅ `oidc_handler_test.go`（login/callback/link/reauth/unlink、取消、重放、错误码）、`oidc_admin_test.go`（secret 只存不回显）。

---

## 9. 覆盖缺口与结论

### 已闭合（本次）
- `IsRequestSecure` / HSTS 对 `X-Forwarded-Proto` 的无条件信任 → 🔧 修复 + 31 个子用例（含红/绿变异验证）。

### 残留（非本次可安全改动）
1. 密码会话 cookie 默认无 `Secure`：项目 LAN/NAS HTTP 的历史取舍，对外 HTTPS 暴露时需评估——**文档建议，非代码 bug**。
2. Discovery 端点 host 一致性：`go-oidc` 已校验 issuer，再加同域会破坏多域 IdP——**刻意不加**。
3. CSP 缺 `object-src 'none'` / `base-uri`：可选加固，与 OIDC 无直接关系。

### 测试边界（诚实声明）
- 安全关键行绝大多数有单测；`Exchange` 成功路径的 PKCE 换码依赖 `httptest` 假 Provider（`provider_test.go`），未对真实 Keycloak/Authentik/Authelia 做端到端烟测（仓库文档亦声明未完成）。
- `authorization_endpoint` 未被应用直连（只是拼 URL 重定向），故无需对“浏览器跳转”本身单测。
