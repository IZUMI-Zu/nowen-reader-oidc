# 配置说明

[English](./CONFIGURATION.en.md) · 简体中文

## 环境变量

| 变量 | 默认值 | 说明 |
|:---|:---|:---|
| `PORT` | `3000` | HTTP 服务监听端口 |
| `BASE_PATH` | `/` | 子路径部署前缀（例如 `/reader` 或 `/reader/`，留空或 `/` 为根部署） |
| `TRUST_PROXY_HEADERS` | `false` | 是否信任 `X-Forwarded-Proto`、`X-Forwarded-Host` 和 `X-Forwarded-Prefix`；仅在可信反向代理后启用 |
| `TRUSTED_PROXIES` | — | 明确信任的反向代理 IP/CIDR，逗号分隔；默认不信任任何代理 |
| `PUBLIC_URL` | — | OIDC 对外 origin，例如 `https://reader.example.com`；不包含 `BASE_PATH`、query 或 fragment |
| `OIDC_ENABLED` | `false` | 显式启用 OpenID Connect 登录 |
| `OIDC_ISSUER_URL` | — | Provider 的精确 HTTPS issuer URL |
| `OIDC_CLIENT_ID` | — | NowenReader confidential client ID |
| `OIDC_CLIENT_SECRET` | — | NowenReader confidential client secret |
| `OIDC_DISPLAY_NAME` | `OpenID Connect` | 登录页显示的 Provider 名称 |
| `OIDC_SCOPES` | `openid profile email` | 空格分隔的 OAuth scopes；必须包含 `openid` |
| `OIDC_DISABLE_PASSWORD_LOGIN` | `false` | 启用 OIDC 后关闭用户名/密码登录和自助注册；配置错误时保持关闭（fail-closed） |
| `OIDC_AUTO_PROVISION` | `false` | 是否为未知 `(issuer, subject)` 自动创建本地普通用户 |
| `OIDC_BOOTSTRAP_ADMIN_SUBJECTS` | — | 可成为首个管理员的精确 OIDC subject，逗号分隔；推荐留空并先创建本地管理员 |
| `OIDC_SESSION_MAX_AGE` | `12h` | OIDC 本地 Session 不可续期突破的绝对时限，范围 `5m`–`720h` |
| `EHENTAI_ENABLED` | `false` | 显式启用可选的 E-Hentai / ExHentai 元数据源 |
| `EHENTAI_SITE` | `ehentai` | 元数据搜索站点：`ehentai` 或 `exhentai` |
| `EHENTAI_IPB_MEMBER_ID` | — | 可选账号 Cookie；ExHentai 必填，必须与 pass hash 成对配置 |
| `EHENTAI_IPB_PASS_HASH` | — | 可选账号 Cookie；仅从环境读取，禁止提交到 Git |
| `EHENTAI_STAR` | — | 可选 `star` Cookie |
| `EHENTAI_IGNEOUS` | — | 可选 `igneous` Cookie |
| `EHENTAI_PREFER_ORIGINAL_TITLE` | `false` | 优先使用 gallery 原始标题 |
| `EHENTAI_SEARCH_EXPUNGED` | `false` | 标题搜索时包含 expunged galleries |
| `DATABASE_URL` | `./data/nowen-reader.db` | SQLite 数据库文件路径 |
| `COMICS_DIR` | `./comics` | 漫画主目录 |
| `NOVELS_DIR` | `./novels` | 电子书主目录 |
| `DATA_DIR` | `./.cache` | 数据/缓存目录（缩略图、页面缓存、`site-config.json`、`ai-config.json`） |
| `FRONTEND_DIR` | — | 开发模式下指向独立前端构建产物；生产环境留空以使用嵌入前端 |
| `GIN_MODE` | `debug` | Gin 运行模式（`debug` 详细日志 / `release` 静默） |
| `TZ` | `Asia/Shanghai` | 时区 |
| `PUID` / `PGID` | `1001` / `1001` | Docker 内进程的 UID / GID（用于解决 bind-mount 权限问题） |
| `UMASK` | `0002` | Docker 内新建文件/目录的权限掩码；`0002` 适合 NAS/共享目录的同组写入 |
| `PERMISSION_FIX_MODE` | `auto` | Docker 启动时的权限修复模式：`auto` 自动修复，`relaxed` 在 NAS/SMB/NFS 无法 `chown` 时回退到更宽松权限，`off` 只检测不修复 |

## 子路径部署

Docker 中设置 `BASE_PATH=/reader` 后，Web、API、PWA 和 OPDS 都会挂载到 `/reader`：

```yaml
environment:
  - BASE_PATH=/reader
  - TRUST_PROXY_HEADERS=true
  - TRUSTED_PROXIES=172.18.0.1
```

`TRUST_PROXY_HEADERS` 只有同时配置 `TRUSTED_PROXIES` 才会生效。Nginx 示例：

```nginx
location /reader/ {
    proxy_pass http://127.0.0.1:3000;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

代理必须保留 `/reader` 前缀，不要在转发时将其剥离。配置完成后可通过 `/reader/api/health` 检查服务状态。

## OpenID Connect

完整的 Provider 注册、所有选项、账号迁移、安全关闭密码登录和故障恢复说明见 [OpenID Connect 配置](./OIDC.md)。

先在 Provider 注册固定回调地址：

```text
PUBLIC_URL + BASE_PATH + /api/auth/oidc/callback
```

示例：

```yaml
environment:
  - PUBLIC_URL=https://reader.example.com
  - BASE_PATH=/reader
  - OIDC_ENABLED=true
  - OIDC_ISSUER_URL=https://identity.example.com/realms/readers
  - OIDC_CLIENT_ID=nowen-reader
  - OIDC_CLIENT_SECRET=replace-with-a-secret
  - OIDC_DISPLAY_NAME=公司账号
  - OIDC_SCOPES=openid profile email
  - OIDC_DISABLE_PASSWORD_LOGIN=false
  - OIDC_AUTO_PROVISION=false
  - OIDC_BOOTSTRAP_ADMIN_SUBJECTS=
  - OIDC_SESSION_MAX_AGE=12h
```

推荐先用本地首次设置创建 break-glass 管理员，再从账户设置显式绑定 OIDC。若必须直接用 OIDC 创建首个管理员，需要同时启用 `OIDC_AUTO_PROVISION=true`，并在 `OIDC_BOOTSTRAP_ADMIN_SUBJECTS` 中填写该 Provider 的精确 `sub`；启用这套安全 bootstrap 后，本地首次管理员注册会关闭，未命中 allowlist 的首个登录也会被拒绝。如需改回本地首次设置，应先关闭 OIDC bootstrap 配置。系统只用已验证的 `(issuer, subject)` 识别账号，不会按 email 或 username 自动合并。

只有在真实 OIDC 登录和管理员身份绑定已经验收后，才应设置 `OIDC_DISABLE_PASSWORD_LOGIN=true`。它会同时关闭 `/api/auth/login`、自助注册和 Web 密码表单，但不会删除本地密码、已有 Session 或 API Key；Session 内的密码 reauth 仍可用于敏感操作。关闭密码登录时不能解除最后一个 OIDC 身份。Provider 故障时将无法创建新登录，恢复方法是把该变量改回 `false` 并重启服务。若 OIDC 配置本身无效，关闭请求仍然保持生效，避免密码入口因配置错误意外重新开放。

## E-Hentai / ExHentai 元数据

完整的公开 EH、登录 EX、Cookie 安全、字段映射、限流和故障排查说明见 [E-Hentai / ExHentai 元数据插件](./EHENTAI.md)。该来源默认关闭且不会自动选中，只提供元数据，不下载画廊内容。

## 站点设置

可通过 Web UI 的 **设置** 面板修改，或直接编辑 `{DATA_DIR}/site-config.json`：

```json
{
  "siteName": "NowenReader",
  "comicsDir": "/app/comics",
  "extraComicsDirs": ["/mnt/manga", "/mnt/comics2"],
  "novelsDir": "/app/novels",
  "extraNovelsDirs": ["/mnt/novels2"],
  "thumbnailWidth": 400,
  "thumbnailHeight": 560,
  "pageSize": 24,
  "language": "zh-CN",
  "theme": "dark",
  "registrationMode": "open",
  "scannerConfig": {
    "syncCooldownSec": 30,
    "fsDebounceMs": 2000,
    "fullSyncBatchSize": 50,
    "quickSyncIntervalSec": 60,
    "fullSyncIntervalSec": 120,
    "md5Workers": 2
  }
}
```

### 扫描器参数详解

| 参数 | 默认值 | 说明 |
|:---|:---|:---|
| `syncCooldownSec` | 30 | 两次同步之间的最小冷却时间（秒） |
| `fsDebounceMs` | 2000 | 文件变更后延迟触发同步的防抖时间（毫秒） |
| `fullSyncBatchSize` | 50 | 完整同步每批处理的漫画数量 |
| `quickSyncIntervalSec` | 60 | 快速同步轮询间隔（秒），作为 fsnotify 兜底 |
| `fullSyncIntervalSec` | 120 | 完整同步间隔（秒），处理页数统计与 MD5 计算 |
| `md5Workers` | 2 | MD5 计算的并发数；网盘挂载场景建议设为 1–2 |

### 注册模式（registrationMode）

| 取值 | 说明 |
|:---|:---|
| `open` | 开放注册（默认），任何人可自行注册 |
| `invite` | 仅限邀请，管理员生成邀请码后方可注册 |
| `closed` | 关闭注册，仅管理员可创建账号 |

## AI 配置

通过 Web UI 的 **设置 → AI 面板** 配置，或编辑 `{DATA_DIR}/ai-config.json`。AI 功能完全可选，不配置不影响任何核心功能。

**国际供应商**：OpenAI / Anthropic / Google Gemini / Groq / Mistral / Cohere / Together AI / Perplexity / Fireworks 等

**国内供应商**：通义千问 / DeepSeek / 智谱 GLM / 百川 / 月之暗面 Kimi / 零一万物 / MiniMax / 讯飞星火 等

进入 **设置 → AI 面板**，选择供应商、填入 API Key、选择模型，点击「测试连接」验证后保存即可。

## 支持的文件格式

| 类型 | 格式 |
|:---|:---|
| 漫画 / 压缩包 | `.zip` `.cbz` `.cbr` `.rar` `.7z` `.cb7` `.pdf` `.azw3` |
| 小说 / 电子书 | `.txt` `.epub` `.mobi` `.azw3` `.html` `.htm` |
| 图片（压缩包内） | `.jpg` `.jpeg` `.png` `.gif` `.webp` `.bmp` `.avif` |

## 外部依赖（Docker 已内置）

| 工具 | 用途 | 是否必须 |
|:---|:---|:---|
| `p7zip` | 解压 .7z / .cb7 文件 | 可选 |
| `mupdf-tools` (mutool) | PDF 页面渲染 | 可选 |
| `libwebp-tools` (cwebp) | WebP 缩略图生成 | 可选（降级为 JPEG） |

> Docker 镜像已内置所有依赖，手动安装二进制时按需安装即可。

## 书库管理与多目录配置（推荐）

新版支持在**管理后台 → 书库管理**中创建独立书库（漫画库、小说库、混合库），每个书库可配置：

| 设置 | 说明 |
|:---|:---|
| `rootPath` | 书库根目录（支持目录浏览选择） |
| `defaultAccess` | 访问控制：`public`（所有登录用户可访问）/ `private`（仅授权用户可访问） |
| `scanEnabled` | 是否参与自动扫描 |

管理员还可以为每个用户或用户组分配书库访问权限，实现**多用户资源隔离**。

### 旧版目录配置

旧版的 `ComicsDir`、`ExtraComicsDirs`、`NovelsDir`、`ExtraNovelsDirs` 环境变量和"站点设置 → 额外漫画目录"仍然生效，但推荐使用书库管理统一管理。

1. **Docker 环境**：先在 `docker-compose.yml` 中挂载对应宿主机目录到容器内路径

   ```yaml
   volumes:
     - /your/manga/path1:/mnt/manga
     - /your/manga/path2:/mnt/comics2
   ```

2. 在**管理后台 → 书库管理**中创建书库，选择对应的**容器内路径**，例如 `/mnt/manga`，不要填写宿主机路径 `/your/manga/path1`
3. 系统会自动扫描所有已启用且 scanEnabled=true 的书库

### 上传目标书库

管理员可在首页上传区域选择目标书库：

- **选择具体书库**：文件写入该书库的 `rootPath`，并按书库类型校验文件格式
- **选择"默认目录"**（不选书库）：文件写入旧 `comicsDir` / `novelsDir`，兼容旧配置

只有满足以下条件的书库才会出现在选择列表中：
- `enabled = true`
- `rootPath` 非空
- 书库类型与当前页面内容类型匹配（漫画页显示 comic/mixed，小说页显示 novel/mixed）

**推荐**：新用户优先使用书库管理创建 `rootPath` 明确的书库，上传后系统会通过自动扫描将文件入库。

## 相关文档

- 📦 [安装指南](./INSTALL.md)
- 🔐 [OpenID Connect 配置](./OIDC.md)
- 🔞 [E-Hentai / ExHentai 元数据插件](./EHENTAI.md)
- 📚 [常见问题](./FAQ.md)
- 🛠️ [开发指南](./DEVELOPMENT.md)
