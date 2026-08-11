# Configuration

English · [简体中文](./CONFIGURATION.md)

## Environment Variables

| Variable | Default | Description |
|:---|:---|:---|
| `PORT` | `3000` | HTTP listen port |
| `BASE_PATH` | `/` | Subpath deployment prefix (e.g., `/reader` or `/reader/`, default empty/`/` for root) |
| `TRUST_PROXY_HEADERS` | `false` | Trust `X-Forwarded-Proto`, `X-Forwarded-Host`, and `X-Forwarded-Prefix`; enable only behind a trusted reverse proxy |
| `TRUSTED_PROXIES` | — | Explicit trusted proxy IPs/CIDRs, comma-separated; no proxy is trusted by default |
| `PUBLIC_URL` | — | External OIDC origin such as `https://reader.example.com`; excludes `BASE_PATH`, query, and fragment |
| `OIDC_CONFIG_MODE` | `auto` | `auto`, `environment`, or `database`; auto preserves environment mode when `OIDC_ENABLED` is explicitly set, otherwise it uses Web configuration |
| `OIDC_CONFIG_KEY_FILE` | — | External 32-byte encryption key file for database-managed Client Secrets; defaults to `{DATA_DIR}/secrets/oidc-config.key` |
| `OIDC_FORCE_PASSWORD_LOGIN` | `false` | Deployment recovery switch that forces password login open over the Web setting |
| `OIDC_ENABLED` | `false` | Explicitly enable OpenID Connect login |
| `OIDC_ISSUER_URL` | — | Provider's exact HTTPS issuer URL |
| `OIDC_CLIENT_ID` | — | NowenReader confidential client ID |
| `OIDC_CLIENT_SECRET` | — | NowenReader confidential client secret |
| `OIDC_CLIENT_SECRET_FILE` | — | Read the Client Secret from a file in environment mode; mutually exclusive with `OIDC_CLIENT_SECRET` |
| `OIDC_DISPLAY_NAME` | `OpenID Connect` | Provider label shown on the login page |
| `OIDC_SCOPES` | `openid profile email` | Space-delimited OAuth scopes; must include `openid` |
| `OIDC_DISABLE_PASSWORD_LOGIN` | `false` | Disable username/password login and self-registration after enabling OIDC; remains disabled on configuration errors (fail-closed) |
| `OIDC_AUTO_PROVISION` | `false` | Automatically create a local regular user for an unknown `(issuer, subject)` |
| `OIDC_BOOTSTRAP_ADMIN_SUBJECTS` | — | Exact OIDC subjects allowed to become the first admin; prefer a local break-glass admin instead |
| `OIDC_SESSION_MAX_AGE` | `12h` | Non-renewable absolute lifetime for local OIDC sessions, from `5m` to `720h` |
| `EHENTAI_IPB_MEMBER_ID` | — | Optional account cookie; required for ExHentai and paired with the pass hash |
| `EHENTAI_IPB_PASS_HASH` | — | Optional account cookie; environment-only and never committed to Git |
| `EHENTAI_STAR` | — | Optional `star` cookie |
| `EHENTAI_IGNEOUS` | — | Optional `igneous` cookie |
| `DATABASE_URL` | `./data/nowen-reader.db` | SQLite database file path |
| `COMICS_DIR` | `./comics` | Manga main directory |
| `NOVELS_DIR` | `./novels` | Novels main directory |
| `DATA_DIR` | `./.cache` | Data/cache directory (thumbnails, page cache, `site-config.json`, `ai-config.json`) |
| `FRONTEND_DIR` | — | Path to standalone frontend build output (dev only); leave empty in production to use the embedded frontend |
| `GIN_MODE` | `debug` | Gin mode (`debug` for verbose logs / `release` for silent) |
| `TZ` | `Asia/Shanghai` | Timezone |
| `PUID` / `PGID` | `1001` / `1001` | UID / GID of the in-container process (for bind-mount permission) |
| `UMASK` | `0002` | Permission mask for files/directories created in Docker; `0002` is suitable for group-writable NAS/shared folders |
| `PERMISSION_FIX_MODE` | `auto` | Docker startup permission repair mode: `auto` repairs automatically, `relaxed` falls back to broader permissions when NAS/SMB/NFS cannot `chown`, `off` only checks writability |

## Subpath Deployment

Set `BASE_PATH=/reader` in Docker to mount the Web UI, API, PWA, and OPDS under `/reader`:

```yaml
environment:
  - BASE_PATH=/reader
  - TRUST_PROXY_HEADERS=true
  - TRUSTED_PROXIES=172.18.0.1
```

`TRUST_PROXY_HEADERS` takes effect only when `TRUSTED_PROXIES` is also configured. Nginx example:

```nginx
location /reader/ {
    proxy_pass http://127.0.0.1:3000;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

The proxy must preserve the `/reader` prefix instead of stripping it. After deployment, use `/reader/api/health` to verify the service.

## OpenID Connect

See [OpenID Connect Configuration](./OIDC.en.md) for Provider registration, every option, account migration, safely disabling password login, and recovery.

The recommended flow is to create a local administrator, then use **Settings → Single sign-on** to save a draft, check Discovery, complete a real test login, and enable OIDC without restarting. The environment example below remains available for Docker secrets, Kubernetes, and GitOps; it makes the Web page read-only.

Register this fixed redirect URI with the Provider:

```text
PUBLIC_URL + BASE_PATH + /api/auth/oidc/callback
```

Example:

```yaml
environment:
  - OIDC_CONFIG_MODE=environment
  - PUBLIC_URL=https://reader.example.com
  - BASE_PATH=/reader
  - OIDC_ENABLED=true
  - OIDC_ISSUER_URL=https://identity.example.com/realms/readers
  - OIDC_CLIENT_ID=nowen-reader
  - OIDC_CLIENT_SECRET=replace-with-a-secret
  - OIDC_DISPLAY_NAME=Company Login
  - OIDC_SCOPES=openid profile email
  - OIDC_DISABLE_PASSWORD_LOGIN=false
  - OIDC_AUTO_PROVISION=false
  - OIDC_BOOTSTRAP_ADMIN_SUBJECTS=
  - OIDC_SESSION_MAX_AGE=12h
```

Create a local break-glass administrator first, then explicitly link OIDC from account settings. To bootstrap the first administrator directly through OIDC, set `OIDC_AUTO_PROVISION=true` and put that Provider's exact `sub` in `OIDC_BOOTSTRAP_ADMIN_SUBJECTS`; this safe bootstrap closes local first-admin registration, and any first login outside the allowlist is denied. Disable the OIDC bootstrap configuration before returning to local first-time setup. Accounts are resolved only by the verified `(issuer, subject)` pair; email and username never trigger an automatic merge.

Set `OIDC_DISABLE_PASSWORD_LOGIN=true` only after validating a real OIDC login and the administrator identity binding. It disables `/api/auth/login`, self-registration, and the Web password form, but does not delete local password hashes, existing sessions, or API keys; session-bound password reauthentication remains available for sensitive actions. The last OIDC identity cannot be unlinked while password login is disabled. A Provider outage then prevents new logins; recover by setting this variable back to `false` and restarting the service. If the OIDC configuration itself is invalid, the disable request remains effective so a configuration mistake cannot silently reopen password login.

## E-Hentai / ExHentai metadata

See [E-Hentai / ExHentai Metadata Plugin](./EHENTAI.en.md) for the WebUI switch, public EH, authenticated EX, cookie security, field mapping, rate limits, and troubleshooting. Administrators manage enablement, site, title preference, expunged search, and language restriction in the WebUI; environment variables store cookies only. The source is disabled and unselected by default and reads metadata only.

## Site Settings

Modify via the **Settings** panel in the web UI, or edit `{DATA_DIR}/site-config.json` directly:

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
  "ehentai": {
    "enabled": false,
    "site": "ehentai",
    "preferOriginalTitle": false,
    "searchExpunged": false,
    "forcedLanguage": ""
  },
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

### Scanner Parameters

| Parameter | Default | Description |
|:---|:---|:---|
| `syncCooldownSec` | 30 | Minimum cooldown between two syncs (seconds) |
| `fsDebounceMs` | 2000 | Debounce delay after file changes before triggering a sync (ms) |
| `fullSyncBatchSize` | 50 | Number of items per batch in full sync |
| `quickSyncIntervalSec` | 60 | Quick sync polling interval (seconds), as a fallback for fsnotify |
| `fullSyncIntervalSec` | 120 | Full sync interval (seconds); handles page counting and MD5 |
| `md5Workers` | 2 | Concurrency for MD5 computation; recommended 1–2 for network mounts |

### Registration Mode

| Value | Description |
|:---|:---|
| `open` | Open registration (default) — anyone can register |
| `invite` | Invite-only — admin must generate invite codes |
| `closed` | Closed — only admins can create accounts |

## AI Configuration

Configure via the **Settings → AI** panel in the web UI, or edit `{DATA_DIR}/ai-config.json`. AI features are completely optional; not configuring them does not affect any core functionality.

**International providers**: OpenAI / Anthropic / Google Gemini / Groq / Mistral / Cohere / Together AI / Perplexity / Fireworks, etc.

**Chinese providers**: Tongyi Qianwen / DeepSeek / Zhipu GLM / Baichuan / Moonshot Kimi / 01.AI / MiniMax / iFlytek Spark, etc.

Open **Settings → AI**, select a provider, enter API Key, choose a model, click "Test Connection" to verify, then save.

## Supported File Formats

| Type | Formats |
|:---|:---|
| Manga / Archive | `.zip` `.cbz` `.cbr` `.rar` `.7z` `.cb7` `.pdf` `.azw3` |
| Novel / E-book | `.txt` `.epub` `.mobi` `.azw3` `.html` `.htm` |
| Images (in archives) | `.jpg` `.jpeg` `.png` `.gif` `.webp` `.bmp` `.avif` |

## External Dependencies (Bundled in Docker)

| Tool | Purpose | Required |
|:---|:---|:---|
| `p7zip` | Extracting .7z / .cb7 files | Optional |
| `mupdf-tools` (mutool) | PDF page rendering | Optional |
| `libwebp-tools` (cwebp) | WebP thumbnail generation | Optional (falls back to JPEG) |

> The Docker image bundles all dependencies. When installing manually, install them as needed.

## Library Management & Multi-Directory Setup (Recommended)

The new version supports creating independent libraries (manga, novel, mixed) in **Admin Panel → Library Management**, each with:

| Setting | Description |
|:---|:---|
| `rootPath` | Library root directory (with directory browser) |
| `defaultAccess` | Access control: `public` (all logged-in users) / `private` (authorized users only) |
| `scanEnabled` | Whether to include in automatic scanning |

Admins can also assign per-user or per-group library access for **multi-user resource isolation**.

### Legacy Directory Configuration

The legacy `ComicsDir`, `ExtraComicsDirs`, `NovelsDir`, `ExtraNovelsDirs` environment variables and "Site Settings → Extra Manga Directories" still work, but Library Management is recommended.

1. **Docker**: Mount additional host directories into the container in `docker-compose.yml`:

   ```yaml
   volumes:
     - /your/manga/path1:/mnt/manga
     - /your/manga/path2:/mnt/comics2
   ```

2. Create a library in **Admin Panel → Library Management** and select the matching **container path**, such as `/mnt/manga`; do not enter the host path `/your/manga/path1`
3. The system will scan all enabled libraries with scanEnabled=true

## Related Documents

- 📦 [Installation Guide](./INSTALL.en.md)
- 🔐 [OpenID Connect Configuration](./OIDC.en.md)
- 🔞 [E-Hentai / ExHentai Metadata Plugin](./EHENTAI.en.md)
- 📚 [FAQ](./FAQ.md)
- 🛠️ [Development Guide](./DEVELOPMENT.md)
