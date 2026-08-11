# OpenID Connect 配置

[English](./OIDC.en.md) · 简体中文

NowenReader 可以作为 OpenID Connect 1.0 Relying Party（RP），通过外部身份提供方完成 Web 单点登录。当前实现采用后端托管的 Authorization Code Flow，并对 confidential Web client 使用 PKCE S256。

> **重要说明**
>
> 本文档描述 Web 登录。Flutter 原生客户端尚未接入 OIDC，不应复用本文中的 confidential client 或把 `OIDC_CLIENT_SECRET` 写入客户端应用。

## 开始之前

你需要准备：

- 一个支持 OpenID Connect Discovery、Authorization Code Flow、PKCE S256 和 confidential client 的 Provider；
- NowenReader 的固定 HTTPS 外部地址；
- 在 Provider 中创建 OIDC client 的权限；
- 一个足够长的随机 client secret；
- 一个用于首次配置和故障恢复的本地管理员账号。

生产环境必须使用 HTTPS。只有 `PUBLIC_URL` 使用 `localhost`、`127.0.0.1` 或其他 loopback IP 时才允许 HTTP；Provider issuer 始终必须使用 HTTPS。

## 推荐：在 Web 后台配置

没有显式设置 `OIDC_ENABLED` 的新部署默认使用数据库托管模式。先用本地账号创建管理员，然后进入 **设置 → 统一登录**：

1. 填写 issuer、Client ID、Client Secret、公开站点地址和 scopes，保持“启用 OIDC”关闭并保存草稿。
2. 点击“检查 Discovery”。这只验证 Discovery 文档和端点，不代表 Client Secret 正确。
3. 点击“进行真实测试登录”。NowenReader 会走完整 Authorization Code + PKCE 流程，并把验证后的身份明确绑定到当前管理员。
4. 测试通过后启用 OIDC，先保留密码登录，并用隐身窗口验收双登录模式。
5. 只有确认恢复路径后，才考虑关闭用户名密码登录。

协议字段发生变化会使测试状态失效，必须重新完成真实测试登录。配置保存后立即生效，不需要重启服务。

停用已启用的 OIDC 时，当前管理员必须拥有本地恢复密码。后台会重新统计当前 issuer 下没有本地密码的用户并显示准确数量；数量大于零时必须显式确认影响后才能保存。紧急情况下可在部署环境设置 `OIDC_FORCE_PASSWORD_LOGIN=true` 并重启服务。

Web 托管的 Client Secret 使用 AES-256-GCM 加密。推荐通过 `OIDC_CONFIG_KEY_FILE` 挂载独立的 32-byte key；未设置时会在 `{DATA_DIR}/secrets/oidc-config.key` 创建 `0600` 本地 key。本地 key 与数据库位于同一数据卷时只防止 SQLite 单文件泄露，不能抵御整卷或主机权限泄露，备份时必须同时安全备份 key。

Docker Compose 可这样挂载外部 key：

```bash
openssl rand -out oidc-config.key 32
chmod 600 oidc-config.key
```

```yaml
services:
  nowen-reader:
    environment:
      OIDC_CONFIG_KEY_FILE: /run/secrets/oidc_config_key
    secrets:
      - oidc_config_key
secrets:
  oidc_config_key:
    file: ./oidc-config.key
```

不要在尚未重新输入 Client Secret 前替换或丢失这个 key；否则现有 ciphertext 无法解密，OIDC 会保持不可用而不会覆盖密文。

已有环境变量部署保持兼容：只要显式设置了 `OIDC_ENABLED`，`OIDC_CONFIG_MODE=auto` 就继续使用完整环境配置，Web 页面只读。也可以显式设置 `OIDC_CONFIG_MODE=environment` 或 `database`；两种来源绝不逐字段混合。

部署侧恢复开关 `OIDC_FORCE_PASSWORD_LOGIN=true` 会强制重新开放密码入口，并覆盖 Web 中的关闭密码设置。配置损坏或 Provider 故障时，可设置该变量并重启后使用本地恢复密码登录。

## 示例假设

本文示例使用以下值：

| 项目 | 示例值 |
|:---|:---|
| NowenReader 外部地址 | `https://reader.example.com/reader/` |
| `PUBLIC_URL` | `https://reader.example.com` |
| `BASE_PATH` | `/reader` |
| Provider issuer | `https://auth.example.com` |
| Client ID | `nowen-reader` |
| 回调地址 | `https://reader.example.com/reader/api/auth/oidc/callback` |

回调地址始终由下面三部分组成：

```text
PUBLIC_URL + BASE_PATH + /api/auth/oidc/callback
```

`PUBLIC_URL` 只能包含 origin，不能包含部署路径、query 或 fragment；部署路径只能配置在 `BASE_PATH`。

## Provider 要求

Provider 的 Discovery 文档必须至少提供以下 HTTPS endpoint：

- `authorization_endpoint`
- `token_endpoint`
- `jwks_uri`

ID Token 必须包含标准的 `iss`、`sub`、`aud` 和有效时间声明，并返回请求中的 `nonce`。NowenReader 会验证签名、issuer、audience、`azp`、nonce，以及 Provider 返回时可用的 `at_hash`。重新认证和后台测试登录使用 `max_age=0`，因此还要求符合 OIDC Core 的 `auth_time`，并拒绝缺失、过旧或异常未来时间。

`preferred_username`、`name`、`email` 和 `email_verified` 只用于创建显示资料。账号唯一身份始终是经过验证的 `(issuer, subject)`；系统不会按 email 或 username 自动合并账号。

## Authelia

下面的组织方式和字段说明遵循 Authelia 官方集成文档。示例只包含 NowenReader client；Authelia Provider 自身所需的 signing key、HMAC secret、storage 等仍需按 Authelia 的 Provider 文档配置。

### 生成 Client Secret

使用 Authelia CLI 同时生成随机明文 secret 和 PBKDF2 digest：

```bash
docker run --rm authelia/authelia:latest \
  authelia crypto hash generate pbkdf2 \
  --variant sha512 \
  --random \
  --random.length 72 \
  --random.charset rfc3986
```

命令会输出两个不同的值：

- 明文 secret：只配置为 NowenReader 的 `OIDC_CLIENT_SECRET`；
- `$pbkdf2-sha512$...` digest：只配置在 Authelia 的 `client_secret`。

> **安全说明**
>
> 不要把示例 secret、明文 secret 或展开后的 Docker Compose 配置提交到 Git。不要把 Authelia 中的 digest 误填到 NowenReader；NowenReader 需要的是原始明文 secret。

### Authelia Client 配置

```yaml
identity_providers:
  oidc:
    # 此处还需要 Authelia OIDC Provider 的其他必需配置。
    clients:
      - client_id: 'nowen-reader'
        client_name: 'NowenReader'
        client_secret: '$pbkdf2-sha512$...'
        public: false
        authorization_policy: 'two_factor'
        require_pkce: true
        pkce_challenge_method: 'S256'
        redirect_uris:
          - 'https://reader.example.com/reader/api/auth/oidc/callback'
        scopes:
          - 'openid'
          - 'profile'
          - 'email'
        response_types:
          - 'code'
        grant_types:
          - 'authorization_code'
        access_token_signed_response_alg: 'none'
        userinfo_signed_response_alg: 'none'
        token_endpoint_auth_method: 'client_secret_basic'
```

Provider 中的 `redirect_uris` 必须与 NowenReader 计算出的回调地址完全一致，包括 scheme、host、端口、`BASE_PATH` 和大小写。

## NowenReader

### Docker Compose

把原始明文 secret 放在权限受限的 `.env` 文件中：

```dotenv
NOWEN_READER_OIDC_CLIENT_SECRET=replace-with-the-raw-random-secret
```

```bash
chmod 600 .env
```

在 `docker-compose.yml` 中配置：

```yaml
services:
  nowen-reader:
    environment:
      OIDC_CONFIG_MODE: 'environment'
      PUBLIC_URL: 'https://reader.example.com'
      BASE_PATH: '/reader'
      OIDC_ENABLED: 'true'
      OIDC_ISSUER_URL: 'https://auth.example.com'
      OIDC_CLIENT_ID: 'nowen-reader'
      OIDC_CLIENT_SECRET: '${NOWEN_READER_OIDC_CLIENT_SECRET:?required}'
      OIDC_DISPLAY_NAME: 'Authelia'
      OIDC_SCOPES: 'openid profile email'
      OIDC_DISABLE_PASSWORD_LOGIN: 'false'
      OIDC_AUTO_PROVISION: 'false'
      OIDC_BOOTSTRAP_ADMIN_SUBJECTS: ''
      OIDC_SESSION_MAX_AGE: '12h'
```

应用配置后重建或重启容器：

```bash
docker compose up -d --force-recreate nowen-reader
docker compose logs --tail=100 nowen-reader
```

> **重要说明**
>
> 环境托管模式可使用 `OIDC_CLIENT_SECRET_FILE` 从 Docker/Kubernetes secret 文件读取 client secret；不要同时设置 `OIDC_CLIENT_SECRET`。任何能读取挂载 secret、容器配置或 Docker socket 的主体仍可能取得凭据，应限制相应权限。

## 配置选项

### `PUBLIC_URL`

`string` · 启用 OIDC 时必需

用户访问 NowenReader 的外部 origin，例如 `https://reader.example.com`。不得包含路径、query、fragment 或 userinfo。生产环境必须为 HTTPS；HTTP 只允许用于 loopback 开发地址。

### `BASE_PATH`

`string` · 默认值：`/` · 非必需

NowenReader 的子路径前缀，例如 `/reader`。空值、`/` 和带尾部斜线的形式会被规范化。Web、API、PWA、OPDS 和 OIDC callback 都使用同一前缀。

### `OIDC_ENABLED`

`boolean` · 默认值：`false` · 非必需

OIDC 总开关。设为 `true` 时，`PUBLIC_URL`、`OIDC_ISSUER_URL`、`OIDC_CLIENT_ID` 和 `OIDC_CLIENT_SECRET` 都必须有效。

### `OIDC_ISSUER_URL`

`string` · 启用 OIDC 时必需

Provider 发布的精确 issuer URL。必须是没有 userinfo、query 或 fragment 的绝对 HTTPS URL。不要自行追加 `/authorize`、`/.well-known/openid-configuration` 或 token endpoint。

Issuer 是账号唯一键的一部分。修改 issuer 会使旧身份记录无法用于新 Provider 登录，参见[更换 Provider 或 Issuer](#更换-provider-或-issuer)。

### `OIDC_CLIENT_ID`

`string` · 启用 OIDC 时必需

Provider 中注册的 confidential Web client ID。必须与 ID Token 的 audience，以及存在时的 `azp` 完全匹配。

### `OIDC_CLIENT_SECRET`

`string` · 启用 OIDC 时必需 · 敏感

Provider 为 confidential client 分配的原始明文 secret。不得使用 Authelia 保存的 PBKDF2 digest，不得提交到源码仓库。

### `OIDC_DISPLAY_NAME`

`string` · 默认值：`OpenID Connect` · 非必需

Web 登录按钮显示的 Provider 名称，例如 `Authelia`、`Authentik` 或 `Company Login`。

### `OIDC_SCOPES`

`string` · 默认值：`openid profile email` · 非必需

使用空格分隔的 scope 列表。必须包含 `openid`，重复值会自动移除。不建议添加 `offline_access`；NowenReader 不保存 Provider 的 access token 或 refresh token。

### `OIDC_AUTO_PROVISION`

`boolean` · 默认值：`false` · 非必需

控制未知 `(issuer, subject)` 是否自动创建本地普通用户：

- `false`：只有已经显式绑定的身份可以登录；
- `true`：未知身份可创建 `user` 角色账号；
- 不会通过 email 或 username 绑定到已有账号；
- 不会根据 Provider group/role claim 自动授予管理员权限。

### `OIDC_BOOTSTRAP_ADMIN_SUBJECTS`

`string` · 默认值：空 · 非必需

逗号分隔的精确 OIDC `sub` allowlist，仅在数据库没有任何用户时生效。必须同时启用 `OIDC_AUTO_PROVISION=true`。匹配的第一个用户会创建为管理员；其他 subject 会被拒绝。

推荐先创建本地 break-glass 管理员再绑定 OIDC，因此通常应保持为空。该选项不会提升已有用户的角色。

### `OIDC_SESSION_MAX_AGE`

`duration` · 默认值：`12h` · 非必需

OIDC 本地 Session 的绝对最大寿命。允许范围为 `5m` 到 `720h`，采用 Go duration 格式，例如 `30m`、`12h`、`168h`。滑动续期不能突破该上限。

### `OIDC_DISABLE_PASSWORD_LOGIN`

`boolean` · 默认值：`false` · 非必需

在 OIDC 验收后关闭默认账号登录。设为 `true` 后：

- `POST /api/auth/login` 被拒绝；
- `POST /api/auth/register` 和 Web 自助注册被拒绝；
- Web 隐藏用户名/密码表单；
- 不删除已有密码 hash、Session 或 API Key；
- 已登录 Session 仍可用本地密码完成敏感操作 reauth；
- 后端拒绝解绑当前 OIDC 身份，避免锁定账号。

该选项只能与 `OIDC_ENABLED=true` 一起使用。它采用 fail-closed 语义：当自身值非法或其他 OIDC 配置错误时，密码登录仍保持关闭，防止配置错误意外重新开放入口。

> **锁定风险**
>
> 只有在真实 Provider 登录、管理员绑定和恢复流程全部验证后才能设为 `true`。Provider 故障或配置错误会让新登录不可用。通用恢复方法是设置 `OIDC_FORCE_PASSWORD_LOGIN=true` 并重启服务；只有明确使用环境托管模式时，也可以把 `OIDC_DISABLE_PASSWORD_LOGIN` 改回 `false`。

## 账号初始化与绑定

### 推荐流程：保留本地恢复账号

1. 先保持 `OIDC_DISABLE_PASSWORD_LOGIN=false` 和 `OIDC_AUTO_PROVISION=false`。
2. 通过首次设置创建本地管理员。
3. 登录后进入账号设置，选择绑定 OIDC。
4. 使用隐身窗口验证 OIDC 登录能够进入同一个管理员账号。
5. 为至少一个受控管理员保留本地密码。
6. 验证恢复步骤后，再按需关闭密码登录。

### 纯 OIDC 首次初始化

只有无法先创建本地管理员时才使用：

```yaml
OIDC_AUTO_PROVISION: 'true'
OIDC_BOOTSTRAP_ADMIN_SUBJECTS: 'exact-provider-subject'
OIDC_DISABLE_PASSWORD_LOGIN: 'false'
```

第一个用户创建成功后，建议清空 `OIDC_BOOTSTRAP_ADMIN_SUBJECTS` 并重启。这个 allowlist 不是持续的管理员角色映射。

### 显式绑定与解绑

已有本地账号必须从已认证的浏览器 Session 发起绑定。相同 `(issuer, subject)` 不能绑定到多个本地用户，一个用户也不能同时绑定同一 issuer 下的两个 subject。

解绑要求密码登录当前可用且该用户已经设置本地密码。关闭密码登录时无法解绑，即使数据库中残留其他旧 issuer 身份。

## 安全地关闭密码登录

在 Web 数据库模式中，管理员必须在保存时单独勾选危险操作确认；仅打开开关不会生效。环境变量模式按下述部署流程操作。

执行以下验收后再修改开关：

- 管理员 OIDC 登录能进入预期的原账号，而不是新账号；
- 普通用户登录和自动创建策略符合预期；
- Provider 侧 MFA/访问策略已经启用；
- 回调只注册了预期的 HTTPS 地址；
- 至少一名管理员知道如何修改部署环境并重启服务；
- 已验证把 `OIDC_DISABLE_PASSWORD_LOGIN=false` 后可以恢复本地登录。

然后设置：

```yaml
OIDC_DISABLE_PASSWORD_LOGIN: 'true'
```

重启后检查 `/api/auth/me` 返回：

```json
{
  "loginMethods": {
    "password": false,
    "oidc": {
      "enabled": true,
      "displayName": "Authelia"
    }
  },
  "registrationMode": "closed"
}
```

## 反向代理与子路径

反向代理必须保留 `BASE_PATH`，并将所有 callback 请求转发给同一个 NowenReader 实例：

```nginx
location /reader/ {
    proxy_pass http://127.0.0.1:3000;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

OIDC callback 不从请求头推导外部地址，而是只使用 `PUBLIC_URL` 和 `BASE_PATH`，因此代理层重写 Host 不会改变注册的 callback。Provider 中仍必须注册外部 HTTPS 地址。

如果启用 `TRUST_PROXY_HEADERS=true`，还必须用 `TRUSTED_PROXIES` 明确限制可信代理 IP/CIDR；不要对任意来源信任转发头。

## 运维

### 验证配置

检查 Provider Discovery：

```bash
curl --fail --show-error \
  https://auth.example.com/.well-known/openid-configuration
```

检查 NowenReader 对外能力：

```bash
curl --fail --show-error \
  https://reader.example.com/reader/api/auth/me
```

OIDC 使用延迟 Discovery；Provider 暂时不可用不会阻止 NowenReader 启动，但登录请求会返回 `503`。

### 轮换 Client Secret

NowenReader 当前不支持同时配置两个 secret。轮换时：

1. 在 Provider 维护窗口更新 client secret；
2. 同步更新 NowenReader 的 `OIDC_CLIENT_SECRET`；
3. 重启 NowenReader；
4. 使用新浏览器 Session 验证登录。

已有本地 Session 不依赖 client secret，可以继续使用到自身过期或被撤销。

### 更换 Provider 或 Issuer

Issuer 是身份主键的一部分。安全迁移顺序：

1. 保持或重新启用密码登录；
2. 确保需要迁移的用户拥有本地密码；
3. 修改 Provider/issuer 配置并重启；
4. 用户通过本地账号登录并绑定新 Provider；
5. 验证管理员和普通用户的新身份；
6. 再次关闭密码登录。

旧 issuer 记录不会自动合并，也不能证明用户仍有可用登录方式。

### 紧急恢复

Provider 故障或配置错误造成无法登录时：

```yaml
OIDC_FORCE_PASSWORD_LOGIN: 'true'
```

重启 NowenReader，然后使用保留的本地管理员密码登录。修复 Provider 或配置后，删除该恢复变量或改为 `false`。明确使用环境托管模式时，也可以把 `OIDC_DISABLE_PASSWORD_LOGIN` 改回 `false`；不要通过修改数据库删除外部身份或伪造 Session。

## 故障排查

| 现象 | 常见原因 | 处理方式 |
|:---|:---|:---|
| 登录按钮不显示 | OIDC 配置校验失败 | 查看启动日志并检查四个必填变量 |
| Provider 报 callback/redirect mismatch | 注册地址与计算地址不完全一致 | 核对 scheme、host、端口、`BASE_PATH` 和尾部路径 |
| 登录返回 `account_not_authorized` | 身份未绑定且自动创建关闭，或首管理员不在 allowlist | 先绑定本地账号，或检查 provisioning/bootstrap 配置 |
| 登录返回 `identity_validation_failed` | issuer、audience、`azp`、nonce、签名或 `at_hash` 验证失败 | 核对 client registration 和 Provider token 配置 |
| 登录返回 `503` | Discovery、token endpoint、JWKS 超时或 Provider 5xx | 检查 Provider、DNS、TLS 和容器网络 |
| 用户登录后出现新账号 | Provider 的 `sub` 或 issuer 发生变化，或未先绑定 | 重新启用本地登录并显式绑定正确身份，不要按 email 合并数据库 |
| 开关写错后密码登录仍关闭 | fail-closed 安全策略 | 修正布尔值；需要恢复时显式设为 `false` 并重启 |
| 不能解绑 OIDC | 密码登录关闭或用户没有本地密码 | 先启用密码登录并设置本地密码 |

## 当前限制

- 只支持一个 Web OIDC issuer/client；
- 只实现 Authorization Code Flow，不实现 Implicit、Password Grant 或 Device Flow；
- 不保存 Provider access token/refresh token，不调用 UserInfo endpoint；
- 不实现 group/role claim 映射；
- 退出只撤销 NowenReader 本地 Session，尚未实现 RP-Initiated Logout；
- Flutter 原生应用尚未接入 OIDC。

## 参考资料

- [Authelia OpenID Connect Clients](https://www.authelia.com/configuration/identity-providers/openid-connect/clients/)
- [Authelia 生成 Client ID / Client Secret](https://www.authelia.com/integration/openid-connect/frequently-asked-questions/#client-id--secret)
- [OpenID Connect Core 1.0](https://openid.net/specs/openid-connect-core-1_0.html)
- [OAuth 2.0 Authorization Server Metadata](https://www.rfc-editor.org/rfc/rfc8414.html)
- [Proof Key for Code Exchange](https://www.rfc-editor.org/rfc/rfc7636.html)
- [OAuth 2.0 Security Best Current Practice](https://www.rfc-editor.org/rfc/rfc9700.html)
