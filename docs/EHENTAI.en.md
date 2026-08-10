# E-Hentai / ExHentai Metadata Plugin

English · [简体中文](./EHENTAI.md)

## Overview

Nowen Reader can use E-Hentai or ExHentai as an **explicitly selected comic metadata source**. It imports gallery titles, artists, groups, year, language, category, namespaced tags, cover, and rating.

The plugin is disabled by default and is not included in default automatic or batch scraping sources. It is used only after an administrator configures it on the server and a user manually selects **E-Hentai / ExHentai** in the metadata source filter.

> [!IMPORTANT]
> This feature reads metadata only. It does not download galleries, images, archives, or torrents; it does not call the archiver or spend GP. Reverse-image search is not included in this version.

The implementation references LANraragi's official [`Metadata/EHentai.pm`](https://github.com/Difegue/LANraragi/blob/5dd0a75ec6a96fe596090c1ddce6ca682a137b0c/lib/LANraragi/Plugin/Metadata/EHentai.pm) and [`Login/EHentai.pm`](https://github.com/Difegue/LANraragi/blob/5dd0a75ec6a96fe596090c1ddce6ca682a137b0c/lib/LANraragi/Plugin/Login/EHentai.pm), pinned to commit [`5dd0a75`](https://github.com/Difegue/LANraragi/commit/5dd0a75ec6a96fe596090c1ddce6ca682a137b0c).

## Supported queries

| Input | Behavior |
|:---|:---|
| Normal title | Search the configured EH or EX site, then fetch metadata for the first 10 results in one batch |
| Title containing `[gid]` | Search with `gid:{id}` to resolve the gallery token |
| Full gallery URL | Fetch that `gid/token` directly without a search-page request |
| Existing `source:` gallery tag | Use its `gid/token` directly and skip title search |
| Existing ASCII `artist:` tag | Add an artist restriction to the normal title search |

Only these HTTPS URL forms are accepted:

```text
https://e-hentai.org/g/{gid}/{token}/
https://exhentai.org/g/{gid}/{token}/
```

Lookalike hosts, HTTP URLs, userinfo, queries, and fragments are rejected.
Legacy HTTP addresses found in existing `source:` tags are upgraded to HTTPS and then checked against the same fixed-host rules.

## WebUI configuration

Sign in as an administrator and open **Settings → Site settings → E-Hentai / ExHentai**:

1. Select E-Hentai or ExHentai.
2. Configure original-title preference, expunged search, and a forced search language as needed.
3. Enable the EH/EX source and save.
4. Manually select **E-Hentai / ExHentai** in a comic or series metadata search.

The source switch and non-sensitive options are written to `{DATA_DIR}/site-config.json`. The administrator-only endpoint returns only a configured/not-configured cookie status; cookie contents never enter the WebUI, site configuration, or API responses.

The global **Enable Content Scraping** setting remains the master switch. When it is off, the backend rejects every metadata source, including EH/EX.

## Cookie configuration

### Public E-Hentai

Public search does not require account cookies or any EH environment variables. Select E-Hentai and enable it in the WebUI.

### ExHentai

ExHentai requires a matching `ipb_member_id` and `ipb_pass_hash` pair:

```yaml
services:
  nowen-reader:
    environment:
      EHENTAI_IPB_MEMBER_ID: ${EHENTAI_IPB_MEMBER_ID:?set in a protected .env file}
      EHENTAI_IPB_PASS_HASH: ${EHENTAI_IPB_PASS_HASH:?set in a protected .env file}
      EHENTAI_STAR: ${EHENTAI_STAR:-}
      EHENTAI_IGNEOUS: ${EHENTAI_IGNEOUS:-}
```

Restart after setting the environment, then select ExHentai and enable it in the WebUI. The backend rejects an enabled ExHentai configuration unless a valid member ID/pass-hash pair is present.

> [!CAUTION]
> These cookies are account session credentials. Never commit them, paste them into issues, include them in screenshots or logs, or share them with untrusted people. Prefer a dedicated low-privilege account and rotate cookies regularly. Nowen Reader neither needs nor accepts the EH/EX username or password.

## WebUI option reference

### Enable EH/EX

Type: boolean

Default: `false`

Controls whether this source appears in metadata source selectors. It is off by default and remains excluded from automatic and batch scraping defaults even when enabled.

### Search site

Type: string

Default: `ehentai`

Allowed: `ehentai`, `exhentai`

Selects the site used for title search. ExHentai requires complete login cookies and fails closed instead of silently falling back to public E-Hentai.

### Prefer the original title

Default: off

Prefer API `title_jpn` when available, falling back to the English/romanized title.

### Search expunged galleries

Default: off

Include expunged galleries in title searches. Exact gallery URL and `source:` tag lookups are unaffected.

### Forced search language

Default: unrestricted

Adds a `language:{name}` restriction to normal title searches. The WebUI supplies fixed values; the backend rejects control characters, non-ASCII text, and injected search expressions. E-Hentai itself has limited support for some language restrictions, particularly Japanese.

## Environment variable reference

### `EHENTAI_IPB_MEMBER_ID`

Type: secret string

Default: empty

The account's `ipb_member_id` cookie. It must contain only ASCII digits and be configured together with `EHENTAI_IPB_PASS_HASH`.

### `EHENTAI_IPB_PASS_HASH`

Type: secret string

Default: empty

The account's `ipb_pass_hash` cookie. Newlines, separators, quotes, backslashes, and oversized values are rejected to prevent Cookie header injection.

### `EHENTAI_STAR`

Type: secret string

Default: empty

Optional `star` cookie. Configure it only when the account session has one.

### `EHENTAI_IGNEOUS`

Type: secret string

Default: empty

Optional `igneous` cookie for ExHentai sessions that require it.

## Cookie acquisition and storage

1. Sign in normally through a browser you trust.
2. Copy the required values from the current EH/EX site cookies. Never provide the EH/EX password to Nowen Reader.
3. Put the values in a protected deployment environment or `.env` file and keep that file out of Git.
4. Restart the process or container. The plugin never writes cookies to `site-config.json`, the database, or Web responses.
5. Update the environment and restart after rotating cookies.

For Docker Compose, limit `.env` access to the deployment account:

```bash
chmod 600 .env
```

## Metadata mapping

| EH field | Nowen Reader field |
|:---|:---|
| `title` / `title_jpn` | Title |
| `artist:*` | Author |
| `group:*` | Publisher/group |
| `posted` | Year in UTC |
| `language:*` | ISO-style language code |
| `tags` + `category` | Genre and comic tags |
| `thumb` | Cover URL |
| `rating` | External rating out of 5 |

The plugin also adds a `source:https://.../g/{gid}/{token}` tag to preserve the exact source. Covers are accepted only from official EH HTTPS image hosts.

## Rate limiting and network security

[EHWiki's search documentation](https://ehwiki.org/wiki/searches) requires at least three seconds between gallery searches; the plugin uses a more conservative process-wide four-second interval. The [EHWiki API documentation](https://ehwiki.org/wiki/API) permits up to 25 `gdata` entries per batch; the plugin takes at most 10 search results and sends them in one API request.

Independent `gdata` calls also pass through a process-wide one-request-per-second gate, preventing repeated exact gallery URLs from bypassing search throttling.

Additional controls:

- 15-second overall request timeout;
- 2 MiB HTML and 4 MiB API JSON limits;
- at most 256 tags per gallery, with title, tag, and cover URL length limits;
- no more than five redirects;
- fixed production HTTPS origins for EH, EX, and the EH API;
- no logging of cookies, full responses, gallery queries, or low-level errors containing remote URLs;
- immediate failure on 429, login pages, Sad Panda, temporary bans, invalid tokens, and malformed responses.

## Troubleshooting

### Selecting the source returns no results

Check that:

1. both the global Content Scraping switch and the EH/EX source switch are enabled;
2. EH/EX was manually selected in the source filter;
3. the title is no longer than 200 search characters;
4. logs do not report invalid configuration, rate limiting, or rejected authentication.

### ExHentai authentication always fails

- Verify that ExHentai is selected in the WebUI.
- `ipb_member_id` and `ipb_pass_hash` must come from the same active session.
- Refresh `igneous` when the session requires it.
- Enter only cookie values, without cookie names, embedded quotes, or newlines.
- Restart after signing in again and rotating the cookies.

### Searches are slow

EH/EX is an explicit low-frequency source. Concurrent searches queue behind the four-second interval to avoid triggering site limits. Do not enable it by default for large batch operations.

### Why automatic scraping does not use EH/EX

This is a privacy and rate-limit safeguard. The application sends titles to an adult site only after explicit user selection.

## Test policy

EH/EX tests use sanitized versions of LANraragi's official static fixtures and local `httptest` servers. The test transport rejects every non-local request, so development tests do not access the real EH/EX API. See the [implementation plan](./EHENTAI_PLUGIN_PLAN.md) for the design and test matrix.
