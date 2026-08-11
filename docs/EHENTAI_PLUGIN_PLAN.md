# E-Hentai / ExHentai Metadata Plugin Plan

## Status

Implemented on branch `feat/ehentai-metadata-plugin`, based on commit
`d284683ed44216e069e114e591836210960676dc`.

This plan covers metadata lookup only. It does not add gallery downloading,
archive purchasing, torrent handling, or reverse-image search.

## Reference implementation

The behavior is based on LANraragi commit
[`5dd0a75ec6a96fe596090c1ddce6ca682a137b0c`](https://github.com/Difegue/LANraragi/commit/5dd0a75ec6a96fe596090c1ddce6ca682a137b0c),
in particular:

- [`Metadata/EHentai.pm`](https://github.com/Difegue/LANraragi/blob/5dd0a75ec6a96fe596090c1ddce6ca682a137b0c/lib/LANraragi/Plugin/Metadata/EHentai.pm)
- [`Login/EHentai.pm`](https://github.com/Difegue/LANraragi/blob/5dd0a75ec6a96fe596090c1ddce6ca682a137b0c/lib/LANraragi/Plugin/Login/EHentai.pm)
- [`Metadata/EHentai.t`](https://github.com/Difegue/LANraragi/blob/5dd0a75ec6a96fe596090c1ddce6ca682a137b0c/tests/LANraragi/Plugin/Metadata/EHentai.t)

LANraragi is used as a behavioral reference, not copied as a runtime
dependency. The Go implementation will use the existing `golang.org/x/net/html`
dependency for HTML parsing and the standard `net/http` and `encoding/json`
packages.

## User-visible behavior

1. Add an opt-in `ehentai` metadata source for comics.
2. Support three lookup inputs:
   - an exact `e-hentai.org/g/{gid}/{token}` or
     `exhentai.org/g/{gid}/{token}` gallery URL;
   - a title containing `[gid]`, resolved through a `gid:` search;
   - a normal title search.
   Existing `source:` tags bypass title search and existing ASCII `artist:`
   tags narrow normal searches.
3. Search either E-Hentai or ExHentai according to server configuration.
4. Map gallery metadata to Nowen Reader fields:
   - English/romanized or original title;
   - `artist:` tags to author;
   - `group:` tags to publisher;
   - posted timestamp to year;
   - `language:` tag to language;
   - all namespaced gallery tags plus `category:` to genre/tags;
   - thumbnail URL to cover;
   - gallery rating as a 5-point external rating.
5. Keep the source out of default automatic and batch source lists. Users must
   explicitly select it, preventing accidental adult-site traffic.

## Module design

The module interface presented to the existing metadata search dispatcher is:

```go
func SearchEHentai(query, lang string) []ComicMetadata
```

The implementation hides:

- configuration validation;
- cookie creation and scoping;
- exact URL and gallery identifier parsing;
- HTML search-result parsing;
- batched `gdata` requests;
- rate limiting and response-size limits;
- conversion into `ComicMetadata`;
- sanitized error reporting.

Tests use an internal seam that accepts a local HTTP client, local base URLs,
and a no-wait limiter. That seam is not exposed to handlers or other packages.

## Configuration

The plugin is disabled by default. Administrators manage non-sensitive options
through a dedicated WebUI and admin-only endpoint. Account cookies remain
environment-only and are never written to `site-config.json`, returned by an
API, or stored in the database.

WebUI-managed settings:

| Setting | Default | Purpose |
| --- | --- | --- |
| Enabled | `false` | Makes the source available for explicit selection. |
| Site | `ehentai` | `ehentai` or `exhentai`. |
| Prefer original title | `false` | Prefer `title_jpn` when present. |
| Search expunged | `false` | Include expunged galleries in search. |
| Forced language | empty | Add an EH `language:` search restriction. |

Environment-only secrets:

| Variable | Default | Purpose |
| --- | --- | --- |
| `EHENTAI_IPB_MEMBER_ID` | empty | Account member cookie. |
| `EHENTAI_IPB_PASS_HASH` | empty | Account pass-hash cookie. |
| `EHENTAI_STAR` | empty | Optional account star cookie. |
| `EHENTAI_IGNEOUS` | empty | Optional ExHentai access cookie. |

For ExHentai, member ID and pass hash are mandatory. Incomplete or malformed
credentials make the source unavailable instead of falling back silently.

## Security controls

- Never log Cookie headers, cookie values, full response bodies, or gallery
  search queries.
- Accept only fixed HTTPS origins for production requests.
- Refuse redirects outside the E-Hentai family of hosts and cap redirects.
- Revalidate EH cover hosts on the initial URL and every redirect before any
  image response is downloaded.
- Validate cookie length and characters before creating a request.
- Apply timeouts and bounded response readers for both HTML and JSON.
- Enforce a process-wide search-page interval of at least four seconds.
- Use a single batched `gdata` request for the first search results, with a
  maximum of 25 galleries.
- Treat login pages, the ExHentai sad-panda response, temporary bans, rate
  limits, invalid gallery tokens, and malformed responses as explicit errors.
- Keep the source opt-in and admin-gated through the existing scraper routes.
- Propagate request cancellation through EH rate-limit waits and HTTP calls
  without pre-reserving an unbounded queue of future limiter slots.

## No-live-request test policy

No development or test step may call `e-hentai.org`, `exhentai.org`,
`api.e-hentai.org`, or their image hosts.

Tests will use:

- sanitized static fixtures derived from LANraragi's MIT-licensed EH fixtures;
- `httptest.Server` for search and `gdata` behavior;
- a test-only transport that rejects any request not targeting the local test
  server;
- injected no-wait rate limiting.

The test matrix covers:

- exact EH and EX URL parsing and rejection of lookalike hosts;
- title and `[gid]` query construction;
- compact/minimal HTML gallery link parsing and deduplication;
- Cookie scoping without secret leakage;
- English/original title fallback;
- tags, category, author, publisher, language, year, cover, and rating mapping;
- empty results, malformed HTML/JSON, oversized bodies, API error payloads,
  invalid tokens, login pages, sad-panda pages, temporary bans, 429 handling,
  and forbidden redirects;
- disabled and incomplete configuration behavior;
- explicit source selection without changing existing default sources.

## Delivery and verification

1. Add backend configuration and the EH metadata module.
2. Register `ehentai` in the existing metadata dispatcher.
3. Add the source to comic source selectors, unchecked by default.
4. Add bilingual configuration and security documentation.
5. Run focused tests, `go test ./... -race`, `go vet ./...`, frontend lint and
   production builds, secret scanning, and a final diff audit.
