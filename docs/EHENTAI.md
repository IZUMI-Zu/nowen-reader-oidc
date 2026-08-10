# E-Hentai / ExHentai 元数据插件

[English](./EHENTAI.en.md) · 简体中文

## 概述

Nowen Reader 可以把 E-Hentai 或 ExHentai 作为一个**显式选择的漫画元数据源**，读取画廊标题、作者、社团、年份、语言、分类、命名空间标签、封面和评分。

插件默认关闭，也不会加入自动刮削或批量刮削的默认数据源。只有管理员完成服务端配置，并在元数据搜索界面手动勾选 **E-Hentai / ExHentai** 时才会使用它。

> [!IMPORTANT]
> 本功能只读取元数据，不下载画廊、图片、压缩包或种子，不调用 archiver，也不消耗 GP。当前版本不提供反向图片搜索。

实现参考了 LANraragi 官方 [`Metadata/EHentai.pm`](https://github.com/Difegue/LANraragi/blob/5dd0a75ec6a96fe596090c1ddce6ca682a137b0c/lib/LANraragi/Plugin/Metadata/EHentai.pm) 和 [`Login/EHentai.pm`](https://github.com/Difegue/LANraragi/blob/5dd0a75ec6a96fe596090c1ddce6ca682a137b0c/lib/LANraragi/Plugin/Login/EHentai.pm)，参考版本固定为 [`5dd0a75`](https://github.com/Difegue/LANraragi/commit/5dd0a75ec6a96fe596090c1ddce6ca682a137b0c)。

## 支持的查询方式

| 输入 | 行为 |
|:---|:---|
| 普通标题 | 在配置的 EH 或 EX 站点进行标题搜索，再批量读取前 10 个结果的元数据 |
| 包含 `[gid]` 的标题 | 使用 `gid:{id}` 搜索找到 gallery token，再读取元数据 |
| 完整 gallery URL | 直接读取该 `gid/token`，跳过搜索页面 |
| 漫画已有 `source:` gallery 标签 | 自动使用标签中的 `gid/token`，跳过标题搜索 |
| 漫画已有 ASCII `artist:` 标签 | 把作者限定条件加入普通标题搜索 |

完整 URL 只接受以下 HTTPS 形式：

```text
https://e-hentai.org/g/{gid}/{token}/
https://exhentai.org/g/{gid}/{token}/
```

相似域名、HTTP URL、带 userinfo、query 或 fragment 的 URL 会被拒绝。
已有 `source:` 标签中的历史 HTTP 地址会先升级为 HTTPS，再按同样的固定域名规则验证。

## WebUI 配置

使用管理员账号打开 **设置 → 站点设置 → E-Hentai / ExHentai**：

1. 选择 E-Hentai 或 ExHentai；
2. 按需设置原始标题、expunged gallery 和强制搜索语言；
3. 开启 EH/EX 来源并保存；
4. 在漫画或系列元数据搜索中手动勾选 **E-Hentai / ExHentai**。

EH/EX 总开关和非敏感选项写入 `{DATA_DIR}/site-config.json`。管理员专用接口只返回 Cookie 的“已配置/未配置”状态，Cookie 内容不会进入 WebUI、站点配置或 API 响应。

全局 **启用内容刮削** 仍是上层总开关；关闭后所有元数据来源（包括 EH/EX）都会被后端拒绝。

## Cookie 配置

### 公开 E-Hentai

公开搜索不强制要求账号 Cookie，不需要为 EH 设置任何环境变量。直接在 WebUI 选择 E-Hentai 并启用即可。

### ExHentai

ExHentai 必须提供成对的 `ipb_member_id` 与 `ipb_pass_hash`：

```yaml
services:
  nowen-reader:
    environment:
      EHENTAI_IPB_MEMBER_ID: ${EHENTAI_IPB_MEMBER_ID:?set in a protected .env file}
      EHENTAI_IPB_PASS_HASH: ${EHENTAI_IPB_PASS_HASH:?set in a protected .env file}
      EHENTAI_STAR: ${EHENTAI_STAR:-}
      EHENTAI_IGNEOUS: ${EHENTAI_IGNEOUS:-}
```

配置环境变量并重启后，在 WebUI 选择 ExHentai 并启用。没有有效的 member ID/pass hash 时，后端会拒绝保存“已启用的 ExHentai”配置。

> [!CAUTION]
> Cookie 等同于账号会话凭据。不要提交到 Git、粘贴到 issue、截图或日志，也不要与不受信任的人共享。建议使用专用低权限账号并定期轮换 Cookie。Nowen Reader 不需要也不会接收 EH/EX 的用户名和密码。

## WebUI 选项参考

### 启用 EH/EX

类型：boolean

默认值：`false`

控制来源是否显示在元数据来源选择器中。默认关闭；即使开启也不会自动加入普通或批量刮削的默认来源。

### 检索站点

类型：string

默认值：`ehentai`

允许值：`ehentai`、`exhentai`

选择标题搜索使用的站点。ExHentai 要求完整登录 Cookie，缺失时 fail closed，不会自动回退到公开 E-Hentai。

### 优先使用原始标题

默认值：关闭

开启时优先使用 API 的 `title_jpn`；原始标题为空时回退到英文/罗马字标题。

### 搜索已删除的画廊

默认值：关闭

开启时在标题搜索中加入 expunged gallery 搜索选项。精确 gallery URL 和 `source:` 标签直达不受此选项影响。

### 强制搜索语言

默认值：不限制

为普通标题搜索追加 `language:{name}` 限定。该值由 WebUI 的固定选项生成；含控制字符、非 ASCII 内容或搜索表达式的值会被后端拒绝。E-Hentai 本身对部分语言限定（尤其日语）的支持有限。

## 环境变量参考

### `EHENTAI_IPB_MEMBER_ID`

类型：secret string

默认值：空

EH 账号的 `ipb_member_id` Cookie，只允许 ASCII 数字。必须与 `EHENTAI_IPB_PASS_HASH` 同时配置。

### `EHENTAI_IPB_PASS_HASH`

类型：secret string

默认值：空

EH 账号的 `ipb_pass_hash` Cookie。换行、分号、逗号、引号、反斜线和超长值会被拒绝，避免 Cookie header 注入。

### `EHENTAI_STAR`

类型：secret string

默认值：空

可选 `star` Cookie。只有账号确实具有该 Cookie 时才配置。

### `EHENTAI_IGNEOUS`

类型：secret string

默认值：空

可选 `igneous` Cookie，用于需要它的 ExHentai 会话。

## Cookie 获取与存储

1. 只在你信任的浏览器中正常登录 EH/EX。
2. 从浏览器当前站点 Cookie 中复制需要的值；不要把 EH/EX 密码提供给 Nowen Reader。
3. 把 Cookie 放到权限受限的部署环境或 `.env` 文件中，确保该文件不进入 Git。
4. 重启容器或进程。插件不会把 Cookie 写进 `site-config.json`、数据库或 Web 响应。
5. Cookie 轮换后更新环境变量并重启。

如果使用 Docker Compose，建议把 `.env` 权限限制为仅部署账号可读：

```bash
chmod 600 .env
```

## 元数据映射

| EH 字段 | Nowen Reader 字段 |
|:---|:---|
| `title` / `title_jpn` | 标题 |
| `artist:*` | 作者 |
| `group:*` | 出版商/社团 |
| `posted` | 年份（UTC） |
| `language:*` | ISO 风格语言代码 |
| `tags` + `category` | 类型和漫画标签 |
| `thumb` | 封面 URL |
| `rating` | 5 分制外部评分 |

插件还会附加一个 `source:https://.../g/{gid}/{token}` 标签，保留精确来源。再次应用其他 EH/EX 画廊时，旧的 EH/EX `source:` 标签会被替换，避免下次搜索继续命中过期画廊。封面只接受 EH 官方 HTTPS 图片域名；其他主机的 URL 会被丢弃。

合集或目录作品把标签同步到成员漫画时，会同步 `artist:*`、`female:*`、`male:*`、`parody:*` 等内容标签，但不会下发父级的 EH/EX `source:`。每本漫画会保留自己的精确 gallery 来源，避免后续直达查询命中父级或其他卷的 gallery。

## 限流与网络安全

根据 [EHWiki 搜索限制](https://ehwiki.org/wiki/searches)，gallery 搜索最短间隔为 3 秒；本插件采用更保守的进程级 4 秒间隔。根据 [EHWiki API 文档](https://ehwiki.org/wiki/API)，`gdata` 最多可批量请求 25 个条目；本插件一次搜索最多取前 10 个结果，并合并为一个 API 请求。

独立的 `gdata` 请求还会经过每秒最多一次的进程级 gate，防止连续粘贴 gallery URL 绕过搜索限流。

其他限制：

- 请求总超时 15 秒；
- HTML 最大 2 MiB，API JSON 最大 4 MiB；
- 每个 gallery 最多接收 256 个标签，并限制标题、标签和封面 URL 长度；
- 最多跟随 5 次重定向；
- 生产请求只允许 EH、EX 和 EH API 的固定 HTTPS origin；
- EH 封面在写入前以及每次重定向后都会重新校验官方 HTTPS 图片域名，不允许跳转到内网或其他主机；
- 浏览器请求取消后，排队中的 EH/EX 限流等待和进行中的 EH/EX HTTP 请求也会取消，不会继续占用未来请求槽；
- 不记录 Cookie、完整响应、gallery 搜索词或包含远端 URL 的底层错误；
- 429、登录页、Sad Panda、临时封禁、无效 token 和异常响应都会停止本次查询。

## 故障排查

### 勾选后没有结果

确认：

1. 全局“启用内容刮削”和 EH/EX 来源开关均已开启；
2. 在数据源筛选中手动勾选了 EH/EX；
3. 标题没有超过 200 个搜索字符；
4. 服务日志中没有显示配置无效、限流或认证被拒绝。

### ExHentai 一直认证失败

- 确认 WebUI 中选择的是 ExHentai；
- `ipb_member_id` 与 `ipb_pass_hash` 必须来自同一个仍有效的会话；
- 必要时同时更新 `igneous`；
- 不要在变量值两侧加入引号内容、换行或 Cookie 名；只填写值；
- 重新登录并轮换 Cookie 后重启服务。

### 搜索变慢

EH/EX 是显式低频源。并发请求会按 4 秒间隔排队，以避免触发站点限制。批量刮削时不建议默认选中该源。

### 为什么自动刮削不使用 EH/EX

这是隐私和限流上的安全默认值。EH/EX 只在用户主动选择时使用，避免扫描新文件时自动向成人站点发送标题。

## 测试政策

仓库内 EH/EX 测试使用 LANraragi 官方静态 fixture 的脱敏版本和本地 `httptest` server。测试 transport 会拒绝所有非本地请求，因此开发测试不会访问真实 EH/EX API。详细设计与测试矩阵见 [实现计划](./EHENTAI_PLUGIN_PLAN.md)。
