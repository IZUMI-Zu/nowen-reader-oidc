# NowenReader OIDC Web 管理实施计划

状态：Phase A–D 已实施并完成自动化回归；Phase E 真实 Provider 验收待部署凭据
日期：2026-08-10
依赖调研：[OIDC Web 管理组件调研](./OIDC_WEB_ADMIN_COMPONENT_RESEARCH.md)

## 1. 决策

NowenReader 增加仅管理员可用的“登录与认证”后台，让管理员配置并启停单个 OIDC Provider。现有环境变量继续支持 Docker、Kubernetes 和 GitOps，并在环境托管模式下保持最高优先级、后台只读。

本期不替换现有 OIDC 协议实现：

- Discovery、JWKS 和 ID Token 校验继续使用 `github.com/coreos/go-oidc/v3/oidc`。
- Authorization Code、PKCE 和 Token Exchange 继续使用 `golang.org/x/oauth2`。
- 配置继续存入现有 SQLite，使用 `database/sql` 和 `modernc.org/sqlite`，不引入 ORM。
- React 只负责管理员表单和跳转，不解析 Token，不引入浏览器 OIDC SDK。
- Client Secret 使用 Go 标准库 AES-256-GCM；不自写密码算法或 nonce 管理。

## 2. 成功标准

完成后必须满足：

1. 已登录管理员可以在 Web 后台保存草稿、检查 Discovery、完成真实测试登录并启用 OIDC，无需重启服务。
2. 普通用户、API Key 和未登录请求不能读取或修改 OIDC 管理配置。
3. Client Secret、根密钥、ciphertext、authorization code、Token、PKCE verifier 和完整 Provider 错误正文不会通过接口、日志或审计记录泄漏。
4. Discovery 成功不被描述成“Client Secret 已验证”；只有完整 Authorization Code 回调和 Token Exchange 成功才能标记配置已验证。
5. 关闭密码登录前，当前配置必须完成真实测试登录，且至少一个管理员已绑定当前 issuer。
6. 配置更新失败时继续使用上一份有效运行时快照；无效 Web 配置不能阻止服务启动或本地管理员恢复。
7. 环境托管与 Web 托管不会逐字段混合，后台能清楚显示当前配置来源。

## 3. 范围

### 3.1 本期实现

- 单个 OIDC Provider 的管理员配置页。
- 数据库草稿、验证状态、启停、版本冲突和审计。
- Client Secret 加密、替换、清除与恢复错误处理。
- Discovery 检查和真实测试登录。
- 配置即时生效及未完成登录交易的确定性失效。
- 环境托管只读模式和部署侧强制恢复密码登录。
- 中英文 UI、错误码、配置及恢复文档。

### 3.2 本期不实现

- 多 Provider。
- IdP group/role claim 到本地权限的同步。
- 动态 Client Registration。
- Provider access/refresh token 持久化。
- Flutter OIDC。
- RP-Initiated Logout、Back-Channel Logout。
- Vault/KMS/Tink 集成；接口为以后接入外部 KMS 保留版本和 key ID。

`OIDC_BOOTSTRAP_ADMIN_SUBJECTS` 继续是环境专用能力。数据库为空时不存在可进入后台的管理员，因此把首次管理员引导放进 Web 配置没有可用价值，也会扩大未认证设置面。

## 4. 配置来源与兼容策略

新增：

| 变量 | 默认值 | 作用 |
|:---|:---|:---|
| `OIDC_CONFIG_MODE` | `auto` | `auto` / `environment` / `database` |
| `OIDC_CONFIG_KEY_FILE` | — | Web 托管 Client Secret 的 32-byte 根密钥文件，优先支持 Docker secret |
| `OIDC_CLIENT_SECRET_FILE` | — | 环境托管模式下从文件读取 Client Secret |
| `OIDC_FORCE_PASSWORD_LOGIN` | `false` | 部署侧恢复开关；为 `true` 时 Web 配置不能关闭密码入口 |

来源解析规则：

```text
OIDC_CONFIG_MODE=environment
    -> 只使用完整环境配置，Web 只读

OIDC_CONFIG_MODE=database
    -> 只使用 SQLite Web 配置

OIDC_CONFIG_MODE=auto 或未设置
    -> OIDC_ENABLED 被显式设置时沿用环境配置
    -> 否则使用 SQLite Web 配置
```

约束：

- 不支持 issuer 来自环境、secret 来自 SQLite 之类的逐字段合并。
- 环境模式继续保留当前非法配置的 fail-closed 语义。
- 数据库模式配置无法解密或无效时，OIDC 不可用，但默认恢复密码登录。
- `OIDC_FORCE_PASSWORD_LOGIN=true` 对两种来源都拥有最终恢复优先级，不能从 Web 关闭。
- `BASE_PATH` 始终属于部署配置。
- 数据库模式允许管理员显式填写外部 origin；callback 仍只由该值和 `BASE_PATH` 计算，绝不从请求 `Host` 或 Forwarded headers 推断。
- 环境模式继续从 `PUBLIC_URL` 取得外部 origin。

## 5. 深模块与 seam

在现有 `internal/auth/oidc` 中增加配置生命周期，而不是另建一套认证实现。模块外部只暴露登录意图、公共状态和管理员配置动作；加解密、来源解析、Provider 构建、SQLite 事务、审计和运行时切换都留在实现内。

建议的模块形状：

```text
AuthHandler / OIDCAdminHandler
             |
             v
        OIDC Manager
  Begin / Complete / Cancel
  PublicStatus
  GetAdminConfig / Probe / Apply
             |
      +------+------------------+
      |                         |
      v                         v
ConfigRepository          RuntimeSnapshot
env / SQLite adapters     config + Provider + Service
      |
      v
SecretProtector
AES-256-GCM + key-file adapter
```

这里存在两个真实 adapter seam：

- 配置来源：环境 adapter 与 SQLite adapter。
- 根密钥来源：外部挂载文件与本地 `0600` 文件。

生产 Provider 继续使用现有 `go-oidc` adapter；测试继续使用现有进程内假 Provider。handler 不直接读取环境、SQLite 或 Client Secret。

## 6. 运行时快照与热切换

当前 `AuthHandler` 在路由初始化时固定保存 `OIDCConfig` 和 `Service`。实施时改为注入进程级 OIDC Manager，所有登录、`/api/auth/me`、绑定、解绑、reauth 和 Session Cookie 策略都读取同一份有效快照，不再各自调用 `GetOIDCConfig()`。

运行时规则：

1. 每个快照包含不可变的 resolved config、`RemoteProvider` 和 OIDC `Service`。
2. 读路径使用 `atomic.Pointer[RuntimeSnapshot]` 获取整份快照，禁止逐字段更新共享配置。
3. Apply 使用 `sync.Mutex/RWMutex` 串行化以下动作：候选校验、Provider 构建、SQLite CAS 更新、审计、未完成交易失效和快照发布。
4. `Begin` 创建交易与 `Complete` 消费/换码期间受同一协调锁保护，避免切换后写入旧配置交易。
5. 第一版在配置协议字段变化时让所有未完成 OIDC 交易失效，callback 返回稳定的 `oidc_configuration_changed`，提示重新开始；不保留旧 secret 或旧 Provider 快照。
6. 候选配置构建或数据库提交失败时不发布快照，当前登录能力保持不变。
7. 启动读取 Web 配置失败时记录无敏感信息的状态错误，继续启动本地认证。

协议字段包括 issuer、client ID、Client Secret、scopes、外部 origin 和 `BASE_PATH`。显示名称、Session TTL、自动创建和密码登录策略属于策略字段；仅策略字段变化不要求重新验证 Provider。

## 7. 数据模型

新增单行配置表，具体迁移版本以实施时主分支最新版本为准：

```text
OIDCProviderConfig
  id                        固定为 1
  enabled
  issuerURL
  clientID
  secretCiphertext
  secretKeyID
  providerName
  scopes
  publicURL
  autoProvision
  sessionTTLSeconds
  disablePasswordLogin
  revision
  verifiedFingerprint
  lastVerifiedAt
  updatedBy
  updatedAt
```

新增审计表：

```text
OIDCConfigAudit
  id
  actorUserID
  action
  result
  changedFields
  configRevision
  requestID
  createdAt
```

并为 `OIDCLoginTransaction` 增加配置 revision/fingerprint，用于测试登录和配置变化错误分类。

Web 表单、API 与持久化字段采用一一对应关系：

| Web 表单 | API `config` | `OIDCProviderConfig` | 说明 |
|---|---|---|---|
| 启用 OIDC | `enabled` | `enabled` | SQLite `CHECK` 布尔值 |
| Issuer URL | `issuerURL` | `issuerURL` | 规范化后精确匹配 Discovery issuer |
| Client ID | `clientID` | `clientID` | 不透明字符串，不做 trim |
| Client Secret | `clientSecret` / `clearClientSecret` | `secretCiphertext` + `secretKeyID` | 响应只返回 `clientSecretConfigured` |
| 登录按钮名称 | `providerName` | `providerName` | 策略字段 |
| Scopes | `scopes[]` | `scopes` | API 保留数组边界，数据库保存规范化空格分隔值 |
| 站点公开地址 | `publicURL` | `publicURL` | 与 `BASE_PATH` 派生 callback URL |
| 自动创建用户 | `autoProvision` | `autoProvision` | SQLite `CHECK` 布尔值 |
| Session 最长时长 | `sessionTTLSeconds` | `sessionTTLSeconds` | 300–2,592,000 秒 |
| 关闭密码登录 | `disablePasswordLogin` | `disablePasswordLogin` | false→true 需要独立确认 |

`callbackURL`、`BASE_PATH`、验证状态、OIDC-only 用户数和两个危险操作确认属于派生值或请求门禁，不作为第二份配置字段写入数据库。往返测试覆盖全部持久化字段和派生响应字段。

实现约束：

- 配置更新使用 `revision` 做 optimistic compare-and-swap；冲突返回 `409 config_revision_conflict`。
- 配置与成功审计在同一个 `sql.Tx` 中提交。
- `scopes` 按现有规则规范化后保存，必须包含 `openid`。
- `verifiedFingerprint` 对规范化后的协议字段和不透明 secret 版本计算；数据库模式使用 AEAD ciphertext，环境模式使用进程随机标识，绝不把明文 Client Secret 或其可离线猜测的普通哈希写入数据库。Client Secret 变化必然改变 fingerprint。
- 只有真实测试登录成功才更新 `verifiedFingerprint` 和 `lastVerifiedAt`。
- GET DTO 只返回 `clientSecretConfigured`，不返回明文或 ciphertext。
- PUT 未提供 secret 表示保留；清除 secret 使用独立显式动作，空字符串不能意外删除。

## 8. Client Secret 保护

Web 托管需要可逆读取 Client Secret，不能使用密码哈希。采用：

- Go 1.25 `crypto/aes` + `cipher.NewGCMWithRandomNonce`。
- 32-byte key，AEAD associated data 绑定用途、配置行 ID 和格式版本。
- ciphertext 使用版本化封装，包含格式版本和 key ID，为以后轮换或 KMS adapter 留出入口。
- 首选从 `OIDC_CONFIG_KEY_FILE` 读取外部只读密钥。
- 没有外部 key 时，在专用数据目录生成 `0600` 本地 key，使用安全随机数和排他创建，后台明确标记“本地密钥，仅防数据库单文件泄露”。
- key 丢失或 AEAD 校验失败时不覆盖 ciphertext，不显示底层错误，不启用 OIDC。

安全边界必须写进文档：本地 key 与数据库位于同一数据卷时，不能抵御整卷或主机权限泄露；它只保护 SQLite 单文件被复制、误发或篡改。需要更强隔离的部署必须挂载外部 key file。

## 9. 管理员接口

专用路由不能复用公开的 `/api/site-settings`：

| 方法 | 路径 | 保护 | 用途 |
|:---|:---|:---|:---|
| GET | `/api/admin/oidc` | Session + Admin | 获取脱敏配置、来源、callback、状态和 revision |
| POST | `/api/admin/oidc/probe` | Session + Admin + recent auth + strict rate limit | 校验候选字段并执行 Discovery |
| PUT | `/api/admin/oidc` | Session + Admin + recent auth + strict rate limit | 以 revision CAS 保存草稿或策略；显式 `clearClientSecret` 清除 secret |
| POST | `/api/admin/oidc/test-login` | Session + Admin + recent auth + strict rate limit | 发起真实测试登录并绑定当前管理员 |

OIDC callback 继续使用现有固定路径，通过新的 `config_test` purpose 完成测试。测试回调必须绑定发起管理员 Session；成功后验证 ID Token，并在明确的管理员操作中把该 `(issuer, subject)` 绑定到当前管理员。不得创建第二个管理员，也不得根据 email 自动合并。

建议 GET 响应：

```json
{
  "managedBy": "database",
  "editable": true,
  "status": "draft",
  "revision": 3,
  "clientSecretConfigured": true,
  "secretProtection": "external-key-file",
  "callbackURL": "https://reader.example.com/reader/api/auth/oidc/callback",
  "lastVerifiedAt": null,
  "config": {
    "enabled": false,
    "issuerURL": "https://id.example.com",
    "clientID": "nowen-reader",
    "providerName": "Company Login",
    "scopes": ["openid", "profile", "email"],
    "publicURL": "https://reader.example.com",
    "autoProvision": false,
    "sessionTTLSeconds": 43200,
    "disablePasswordLogin": false
  }
}
```

稳定状态值：`disabled`、`draft`、`ready`、`active`、`invalid`、`environment-managed`。错误响应只返回应用稳定错误码和可操作说明，不透传 Provider 原始正文。

## 10. 保存、验证与启用流程

```text
管理员输入配置
  -> 保存为 disabled 草稿
  -> 检查 Discovery
  -> 发起“测试登录并绑定当前管理员”
  -> Provider callback + Token Exchange + ID Token 校验
  -> 标记当前协议 fingerprint 已验证
  -> 管理员启用 OIDC
  -> 双登录模式真实验收
  -> 可选关闭密码登录
```

Discovery 检查只显示以下结论：

- issuer 是否精确匹配；
- Discovery 文档是否可达、issuer 是否精确匹配，授权/Token/JWKS 端点 URL 是否符合 HTTPS 约束；
- Discovery 请求耗时和稳定错误分类；
- callback URL 与需要在 Provider 注册的值。

它不会主动调用授权端点或向 Token 端点发送伪造请求，也不能显示“Client ID/Secret 正确”。只有测试登录进入 Token Endpoint 并完整验证 ID Token 后才可显示“已验证”。

## 11. 防锁死规则

### 11.1 启用 OIDC

启用必须满足：

- 协议字段完整且验证通过；
- `verifiedFingerprint` 与当前协议 fingerprint 一致；
- 发起测试的管理员已经绑定当前精确 issuer；
- 当前管理员 Session 近期重新认证；
- 未被环境配置声明为只读。

### 11.2 关闭密码登录

还必须满足：

- OIDC 已启用并使用当前已验证 fingerprint；
- 至少一个现有 `admin` 已绑定当前 issuer；
- 当前管理员拥有本地 break-glass 密码；
- UI 二次确认并展示恢复命令；
- `OIDC_FORCE_PASSWORD_LOGIN` 的部署侧恢复路径写入文档并经过测试。

### 11.3 停用或更换 Provider

- 当前管理员没有本地密码时禁止停用 OIDC。
- 存在 OIDC-only 用户时显示准确数量和影响，要求显式确认。
- issuer 变化时旧 `ExternalIdentity` 不删除；它们不代表新 issuer 已绑定。
- 新 issuer 必须重新完成测试登录和管理员绑定。
- 已有本地 Session 不因配置保存而立即删除；新登录按新快照处理。

## 12. 管理页面

在设置页新增仅管理员可见的“登录与认证”标签，不放进普通账号页或公开站点设置。

页面分区：

1. **配置来源与状态**：环境/Web、editable、运行状态、最后验证时间、密钥保护等级。
2. **Provider**：issuer、client ID、Client Secret、显示名、scopes。
3. **部署回调**：外部 origin、只读 `BASE_PATH`、最终 callback URL 和复制按钮。
4. **用户策略**：自动创建、Session 绝对期限。
5. **验证操作**：保存草稿、检查 Discovery、测试登录并绑定管理员。
6. **危险操作**：启用/停用 OIDC、关闭密码登录、清除 secret。
7. **恢复说明**：`OIDC_FORCE_PASSWORD_LOGIN=true`、key file 备份和日志位置。

环境托管模式显示字段和状态但禁用编辑，同时明确指出需要修改的环境变量或 secret file；不能让保存按钮看似成功但实际被环境覆盖。

## 13. 实施阶段

### Phase A：配置仓储与密钥保护（1–1.5 人日）

- SQLite migration、repository、revision CAS 和审计表。
- AES-GCM `SecretProtector`、外部 key file、本地 `0600` fallback。
- 环境/数据库整套来源解析和恢复开关。
- 配置校验从环境读取中抽成可复用纯逻辑。

退出条件：仓储、加解密、来源优先级、脱敏和 key 丢失测试通过。

### Phase B：运行时 Manager 与热切换（1–1.5 人日）

- 进程级 Manager 和不可变快照。
- `AuthHandler`、`/me`、绑定/解绑/reauth、Session Cookie 统一改读 Manager。
- Apply 原子发布和未完成交易失效。
- 启动失败降级及稳定状态码。

退出条件：无需重启即可切换；并发测试证明没有混合配置或旧交易穿透。

### Phase C：管理员接口与真实测试登录（1.5–2 人日）

- GET、probe、PUT、test-login、secret clear。
- `config_test` 交易、fingerprint 验证和管理员绑定。
- 启用与关闭密码登录门禁。
- 安全审计和限流。

退出条件：Discovery 与 Client Secret 验证语义分离，所有锁死场景有测试。

### Phase D：React 后台和文档（1–1.5 人日）

- 管理员标签、表单、状态、二次确认和中英文文案。
- 环境只读、版本冲突、reauth 恢复和 Provider 错误体验。
- Docker secret、密钥备份、恢复、升级与回滚文档。

退出条件：桌面和移动 Web 管理流程通过，前端构建和 Base Path 检查通过。

### Phase E：真实 Provider 验收（1–2 人日）

- Keycloak、Authentik、Authelia 至少两个完整回调烟测。
- Client Secret 轮换、Provider 故障、配置切换和 break-glass 演练。

Phase A–D 已完成；真实 Provider 验收仍需可用的 Provider 配置和 **1–2 人日**。

## 14. 测试矩阵

### 14.1 配置与密钥

- 环境、数据库、auto 三种来源及非法/不完整环境配置。
- 环境模式只读，数据库模式可写，不发生逐字段混合。
- AES-GCM round-trip、随机 nonce、associated data、错误 key、篡改 ciphertext、key 丢失。
- secret 未提供时保留、显式清除、替换、轮换和所有响应/日志脱敏。
- revision CAS 冲突不会覆盖另一管理员的修改。

### 14.2 运行时与并发

- 配置保存后 `/api/auth/me` 和新登录立即反映，无需重启。
- 候选构建、Discovery 或 DB 更新失败保留旧快照。
- Apply 与 Begin/Complete 并发时不出现 issuer/client/secret 混用。
- 配置变化使旧交易稳定失败，不创建 Session 或身份绑定。
- `go test ./... -race` 覆盖快照发布和配置更新。

### 14.3 管理权限

- 未登录为 401，普通用户为 403，API Key 被 SessionRequired 拒绝。
- 过期 recent-auth 返回 `reauth_required`。
- probe/test/apply 使用严格限流。
- GET、错误、审计和日志均不包含 secret/ciphertext/token/code。

### 14.4 工作流与锁死

- Discovery 成功但错误 Client Secret：probe 成功，真实测试登录失败，不能启用。
- 测试登录成功并绑定管理员后可启用。
- 改变协议字段后验证状态失效；仅改变策略字段时保持验证状态。
- 没有当前 issuer 管理员绑定时不能关闭密码登录。
- 当前管理员无本地密码时不能停用最后登录方式。
- `OIDC_FORCE_PASSWORD_LOGIN=true` 能在 Provider 故障和 Web 配置损坏时恢复本地登录。
- issuer 更换不误用旧身份记录。

### 14.5 前端与回归

- 环境只读、草稿、invalid、ready、active 状态。
- secret placeholder 不把保存值重新放进 DOM。
- callback、取消、版本冲突和 reauth 后恢复未完成操作。
- 子路径部署 callback 和返回地址正确。
- 密码登录、现有 OIDC 登录、绑定/解绑、API Key、OPDS 全部回归。

验证命令：

```bash
go test ./... -race
go vet ./...

cd frontend
npm run lint
npm run build
```

真实 Provider 验收不能被假 Provider 测试替代。

## 15. 发布与回滚

- 首次发布默认使用数据库草稿且 OIDC disabled，不自动导入或删除现有环境配置。
- 已设置 `OIDC_ENABLED` 的现有安装通过 auto 模式继续使用环境配置，行为不变。
- 先发布密码 + OIDC 双模式，再由管理员完成测试登录和绑定后选择关闭密码登录。
- 数据库 migration 只新增表/列，不删除现有 OIDC identity、Session 或环境变量支持。
- 回滚旧版本前保持 `OIDC_FORCE_PASSWORD_LOGIN=true` 或确认环境密码登录可用；旧版本会忽略新增表，但不会读取 Web OIDC 配置。
- Client Secret key file 必须进入部署备份与恢复清单；不能把 key 内容写入普通应用备份日志。

## 16. 完成定义

只有同时满足以下条件才可声明本功能完成：

- 组件、接口、迁移、Web 页面和文档全部完成。
- 自动化测试矩阵通过，包括 `-race`、secret 泄漏检查和并发配置切换。
- 至少两个真实 Provider 完成“配置、测试登录、绑定、启用、轮换、恢复”闭环。
- 环境托管升级不改变现有部署行为。
- Web 配置损坏、Provider 故障和根密钥丢失均有可执行恢复路径。
- Flutter、multi-provider、claim 权限映射等非目标没有被混入本期。
