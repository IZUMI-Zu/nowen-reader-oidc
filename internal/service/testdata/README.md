# E-Hentai test fixtures

`ehentai_search.html` and `ehentai_gdata.json` are reduced, sanitized fixtures
derived from LANraragi's MIT-licensed test samples at commit
[`5dd0a75ec6a96fe596090c1ddce6ca682a137b0c`](https://github.com/Difegue/LANraragi/commit/5dd0a75ec6a96fe596090c1ddce6ca682a137b0c):

- [`tests/samples/eh/001_gid-1866546.json`](https://github.com/Difegue/LANraragi/blob/5dd0a75ec6a96fe596090c1ddce6ca682a137b0c/tests/samples/eh/001_gid-1866546.json)
- [`tests/samples/eh/002_search_results.html`](https://github.com/Difegue/LANraragi/blob/5dd0a75ec6a96fe596090c1ddce6ca682a137b0c/tests/samples/eh/002_search_results.html)
- [LANraragi MIT license](https://github.com/Difegue/LANraragi/blob/5dd0a75ec6a96fe596090c1ddce6ca682a137b0c/LICENSE)

Tests serve these files only from local `httptest` servers. They do not query
E-Hentai, ExHentai, or the live E-Hentai API.
