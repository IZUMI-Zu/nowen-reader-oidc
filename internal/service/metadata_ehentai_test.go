package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nowen-reader/nowen-reader/internal/config"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

type ehRoundTripFunc func(*http.Request) (*http.Response, error)

func (f ehRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestParseEHGalleryURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  ehGalleryRef
		ok    bool
	}{
		{
			name:  "public",
			input: "https://e-hentai.org/g/1866546/2e521d4407/",
			want:  ehGalleryRef{GID: 1866546, Token: "2e521d4407", Site: config.EHentaiSitePublic},
			ok:    true,
		},
		{
			name:  "restricted",
			input: "https://exhentai.org/g/618395/0439FA3666",
			want:  ehGalleryRef{GID: 618395, Token: "0439fa3666", Site: config.EHentaiSiteRestricted},
			ok:    true,
		},
		{name: "lookalike host", input: "https://e-hentai.org.example/g/1/0123456789/"},
		{name: "userinfo", input: "https://e-hentai.org@evil.example/g/1/0123456789/"},
		{name: "insecure scheme", input: "http://e-hentai.org/g/1/0123456789/"},
		{name: "query", input: "https://e-hentai.org/g/1/0123456789/?next=evil"},
		{name: "invalid token", input: "https://e-hentai.org/g/1/not-a-token/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseEHGalleryURL(tt.input)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("parseEHGalleryURL(%q) = (%+v, %v), want (%+v, %v)", tt.input, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestParseEHGalleryRefsUsesStaticFixtureAndDeduplicates(t *testing.T) {
	body, err := os.ReadFile("testdata/ehentai_search.html")
	if err != nil {
		t.Fatal(err)
	}
	refs, err := parseEHGalleryRefs(body)
	if err != nil {
		t.Fatalf("parseEHGalleryRefs() error = %v", err)
	}
	if len(refs) != 1 || refs[0].GID != 1866546 || refs[0].Token != "2e521d4407" {
		t.Fatalf("parseEHGalleryRefs() = %+v", refs)
	}
}

func TestEHentaiBuildSearchURL(t *testing.T) {
	base, _ := url.Parse("http://127.0.0.1:12345/")
	apiURL, _ := url.Parse("http://127.0.0.1:12345/api.php")
	provider, err := newEHentaiProviderWithEndpoints(
		config.EHentaiConfig{Enabled: true, Site: config.EHentaiSitePublic, SearchExpunged: true, ForcedLanguage: "english"},
		&http.Client{},
		ehEndpoints{searchBase: base, apiURL: apiURL},
		newEHIntervalLimiter(0),
		newEHIntervalLimiter(0),
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("quoted title", func(t *testing.T) {
		u, err := provider.buildSearchURL(`  A "quoted" title  `)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := u.Query().Get("f_search"), `"A quoted title" language:english`; got != want {
			t.Fatalf("f_search = %q, want %q", got, want)
		}
		if u.Query().Get("f_sh") != "on" {
			t.Fatal("expunged search flag missing")
		}
		if !strings.Contains(u.Query().Get("f_search"), "language:english") {
			t.Fatalf("forced language missing from %q", u.Query().Get("f_search"))
		}
	})

	t.Run("existing artist tag", func(t *testing.T) {
		u, err := provider.buildSearchURLWithArtist("Archive title", "fixture artist")
		if err != nil {
			t.Fatal(err)
		}
		if got, want := u.Query().Get("f_search"), `"Archive title" artist:"fixture artist"$ language:english`; got != want {
			t.Fatalf("f_search = %q, want %q", got, want)
		}
	})

	t.Run("unsafe artist hint is ignored", func(t *testing.T) {
		for _, artist := range []string{`fixture" uploader:someone`, "fixture*", "fixture%", "fixture$"} {
			u, err := provider.buildSearchURLWithArtist("Archive title", artist)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := u.Query().Get("f_search"), `"Archive title" language:english`; got != want {
				t.Fatalf("artist %q produced f_search = %q, want %q", artist, got, want)
			}
		}
	})

	t.Run("gid from title", func(t *testing.T) {
		u, err := provider.buildSearchURL("[618395] archive name")
		if err != nil {
			t.Fatal(err)
		}
		if got, want := u.Query().Get("f_search"), "gid:618395"; got != want {
			t.Fatalf("f_search = %q, want %q", got, want)
		}
	})

	t.Run("year and volume brackets stay in the title search", func(t *testing.T) {
		for _, query := range []string{"(C99) [Some Circle] Real Title [2021]", "Volume [12] special"} {
			u, err := provider.buildSearchURL(query)
			if err != nil {
				t.Fatal(err)
			}
			if search := u.Query().Get("f_search"); strings.Contains(search, "gid:") {
				t.Fatalf("query %q searched by gallery ID: %q", query, search)
			}
		}
	})

	t.Run("reject controls and oversized query", func(t *testing.T) {
		if _, err := provider.buildSearchURL("bad\nquery"); !errors.Is(err, errEHInvalidQuery) {
			t.Fatalf("control query error = %v", err)
		}
		if _, err := provider.buildSearchURL(strings.Repeat("x", 201)); !errors.Is(err, errEHInvalidQuery) {
			t.Fatalf("oversized query error = %v", err)
		}
	})
}

func TestEHentaiProviderSearchUsesOnlyLocalFixtureServer(t *testing.T) {
	searchFixture, err := os.ReadFile("testdata/ehentai_search.html")
	if err != nil {
		t.Fatal(err)
	}
	apiFixture, err := os.ReadFile("testdata/ehentai_gdata.json")
	if err != nil {
		t.Fatal(err)
	}

	var searchCalls, apiCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/":
			searchCalls.Add(1)
			if req.URL.Query().Get("f_search") != `"Choro Sugi"` {
				t.Errorf("unexpected search term %q", req.URL.Query().Get("f_search"))
			}
			assertEHCookie(t, req, "ipb_member_id", "123456")
			assertEHCookie(t, req, "ipb_pass_hash", "fixture-pass-hash")
			assertEHCookie(t, req, "nw", "1")
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(searchFixture)
		case "/api.php":
			apiCalls.Add(1)
			assertEHCookie(t, req, "ipb_member_id", "123456")
			var payload struct {
				Method    string          `json:"method"`
				GIDList   [][]interface{} `json:"gidlist"`
				Namespace int             `json:"namespace"`
			}
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Errorf("decode API request: %v", err)
			}
			if payload.Method != "gdata" || payload.Namespace != 1 || len(payload.GIDList) != 1 {
				t.Errorf("unexpected API payload: %+v", payload)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(apiFixture)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	provider := newLocalEHProvider(t, server, config.EHentaiConfig{
		Enabled:     true,
		Site:        config.EHentaiSitePublic,
		IPBMemberID: "123456",
		IPBPassHash: "fixture-pass-hash",
	})
	results, err := provider.search(context.Background(), "Choro Sugi", "en")
	if err != nil {
		t.Fatalf("search() error = %v", err)
	}
	if searchCalls.Load() != 1 || apiCalls.Load() != 1 {
		t.Fatalf("calls = search:%d api:%d", searchCalls.Load(), apiCalls.Load())
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	got := results[0]
	if got.Title != "[Yatsuki Hiyori] Choro Sugi! & More [Digital]" {
		t.Fatalf("Title = %q", got.Title)
	}
	if got.Author != "yatsuki hiyori, second artist" || got.Publisher != "fixture circle, second circle" || got.Language != "en" {
		t.Fatalf("unexpected mapped identity fields: %+v", got)
	}
	if got.Year == nil || *got.Year != 2021 {
		t.Fatalf("Year = %v", got.Year)
	}
	wantTags := strings.Join([]string{
		"artist:yatsuki hiyori",
		"artist:second artist",
		"group:fixture circle",
		"group:second circle",
		"language:english",
		"language:translated",
		"female:example tag",
		"female:second tag",
		"male:example male tag",
		"parody:fixture work",
		"character:fixture heroine",
		"other:full color",
		"tankoubon",
		"category:manga",
		"source:https://e-hentai.org/g/1866546/2e521d4407",
	}, ", ")
	if got.Genre != wantTags {
		t.Fatalf("Genre tags\n got: %q\nwant: %q", got.Genre, wantTags)
	}
	if got.ExternalRating == nil || *got.ExternalRating != 4.74 || got.ExternalRatingMax == nil || *got.ExternalRatingMax != 5 {
		t.Fatalf("rating = %v/%v", got.ExternalRating, got.ExternalRatingMax)
	}
}

func TestEHentaiProviderExactURLSkipsSearchAndPrefersOriginalTitle(t *testing.T) {
	apiFixture, err := os.ReadFile("testdata/ehentai_gdata.json")
	if err != nil {
		t.Fatal(err)
	}
	var searchCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/api.php" {
			_, _ = w.Write(apiFixture)
			return
		}
		searchCalls.Add(1)
		http.Error(w, "unexpected search", http.StatusInternalServerError)
	}))
	defer server.Close()

	provider := newLocalEHProvider(t, server, config.EHentaiConfig{
		Enabled:             true,
		Site:                config.EHentaiSitePublic,
		PreferOriginalTitle: true,
	})
	results, err := provider.search(context.Background(), "https://e-hentai.org/g/1866546/2e521d4407/", "ja")
	if err != nil {
		t.Fatal(err)
	}
	if searchCalls.Load() != 0 || len(results) != 1 || results[0].Title != "[八樹ひより] ちょろすぎっ! [DL版]" {
		t.Fatalf("unexpected exact result: calls=%d results=%+v", searchCalls.Load(), results)
	}
}

func TestEHentaiProviderSourceTagSkipsSearch(t *testing.T) {
	apiFixture, err := os.ReadFile("testdata/ehentai_gdata.json")
	if err != nil {
		t.Fatal(err)
	}
	var searchCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/api.php" {
			_, _ = w.Write(apiFixture)
			return
		}
		searchCalls.Add(1)
		http.Error(w, "unexpected search", http.StatusInternalServerError)
	}))
	defer server.Close()

	provider := newLocalEHProvider(t, server, config.EHentaiConfig{Enabled: true, Site: config.EHentaiSitePublic})
	results, err := provider.searchWithTags(context.Background(), "unrelated archive title", "en", []string{
		"artist:fixture artist",
		"source:http://e-hentai.org/g/1866546/2e521d4407/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if searchCalls.Load() != 0 || len(results) != 1 {
		t.Fatalf("unexpected source-tag result: calls=%d results=%+v", searchCalls.Load(), results)
	}
}

func TestEHentaiProviderRejectsUnsafeRedirect(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalls.Add(1)
		_, _ = io.WriteString(w, "must not be reached")
	}))
	defer target.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, target.URL, http.StatusFound)
	}))
	defer server.Close()
	provider := newLocalEHProvider(t, server, config.EHentaiConfig{Enabled: true, Site: config.EHentaiSitePublic})

	_, err := provider.searchGalleryRefs(context.Background(), "query")
	if !errors.Is(err, errEHForbiddenRedirect) {
		t.Fatalf("searchGalleryRefs() error = %v, want forbidden redirect", err)
	}
	if targetCalls.Load() != 0 {
		t.Fatalf("unsafe redirect target received %d requests", targetCalls.Load())
	}
}

func TestEHentaiProviderClassifiesFailuresWithoutResponseContent(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		want error
	}{
		{name: "rate limit", code: http.StatusTooManyRequests, want: errEHRateLimited},
		{name: "login", code: http.StatusOK, body: "This page requires you to log on.", want: errEHAuthentication},
		{name: "sad panda", code: http.StatusOK, body: "<title>Sad Panda</title>", want: errEHAuthentication},
		{name: "temporary ban", code: http.StatusOK, body: "Your IP address has been temporarily banned", want: errEHTemporarilyBanned},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.code)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			provider := newLocalEHProvider(t, server, config.EHentaiConfig{Enabled: true, Site: config.EHentaiSitePublic})
			_, err := provider.searchGalleryRefs(context.Background(), "private-query-value")
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if strings.Contains(safeEHError(err), "private-query-value") || tt.body != "" && strings.Contains(safeEHError(err), tt.body) {
				t.Fatalf("safe error leaked request/response content: %q", safeEHError(err))
			}
		})
	}
}

func TestSearchEHentaiDisabledDoesNotUseNetwork(t *testing.T) {
	originalTransport := http.DefaultTransport
	var networkAttempts atomic.Int32
	http.DefaultTransport = ehRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		networkAttempts.Add(1)
		return nil, fmt.Errorf("disabled-source test rejected network request to %s", req.URL.Host)
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	t.Setenv("DATA_DIR", t.TempDir())
	if err := config.SaveSiteConfig(&config.SiteConfig{}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"EHENTAI_ENABLED", "EHENTAI_SITE", "EHENTAI_IPB_MEMBER_ID", "EHENTAI_IPB_PASS_HASH",
		"EHENTAI_STAR", "EHENTAI_IGNEOUS", "EHENTAI_PREFER_ORIGINAL_TITLE", "EHENTAI_SEARCH_EXPUNGED",
	} {
		t.Setenv(key, "")
	}
	if results := SearchEHentai("must not leave the process", "en"); len(results) != 0 {
		t.Fatalf("disabled search returned %+v", results)
	}
	if got := networkAttempts.Load(); got != 0 {
		t.Fatalf("disabled source attempted %d network requests", got)
	}
}

func TestEHentaiIsNeverADefaultMetadataSource(t *testing.T) {
	for _, source := range append(append([]string(nil), defaultComicMetadataSources...), defaultNovelMetadataSources...) {
		if source == "ehentai" || source == "exhentai" {
			t.Fatalf("EH/EX source %q must require explicit selection", source)
		}
	}
}

func TestEHentaiProviderRejectsOversizedSearchResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(ehMaxSearchBodyBytes+1))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	provider := newLocalEHProvider(t, server, config.EHentaiConfig{Enabled: true, Site: config.EHentaiSitePublic})
	_, err := provider.searchGalleryRefs(context.Background(), "query")
	if !errors.Is(err, errEHResponseTooLarge) {
		t.Fatalf("searchGalleryRefs() error = %v, want response-too-large", err)
	}
}

func TestEHentaiProviderRejectsGalleryAndAPIErrorPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
		want error
	}{
		{
			name: "gallery token error",
			body: `{"gmetadata":[{"gid":1866546,"token":"2e521d4407","error":"Key missing"}]}`,
			want: errEHInvalidGallery,
		},
		{
			name: "top-level API error",
			body: `{"error":"request rejected"}`,
			want: errEHRemote,
		},
		{
			name: "malformed JSON",
			body: `{"gmetadata":`,
			want: errEHInvalidResponse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/api.php" {
					http.NotFound(w, req)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			provider := newLocalEHProvider(t, server, config.EHentaiConfig{Enabled: true, Site: config.EHentaiSitePublic})
			_, err := provider.search(context.Background(), "https://e-hentai.org/g/1866546/2e521d4407/", "en")
			if !errors.Is(err, tt.want) {
				t.Fatalf("search() error = %v, want %v", err, tt.want)
			}
			if strings.Contains(safeEHError(err), tt.body) {
				t.Fatalf("safe error leaked API payload: %q", safeEHError(err))
			}
		})
	}
}

func TestEHentaiMetadataRejectsUntrustedCoverHost(t *testing.T) {
	provider := &ehentaiProvider{cfg: config.EHentaiConfig{}}
	meta, ok := provider.mapGallery(ehGallery{
		GID:    1,
		Token:  "0123456789",
		Title:  "Fixture",
		Thumb:  "https://127.0.0.1/admin",
		Rating: "4.5",
	}, ehGalleryRef{GID: 1, Token: "0123456789", Site: config.EHentaiSitePublic})
	if !ok {
		t.Fatal("valid gallery fixture was rejected")
	}
	if meta.CoverURL != "" {
		t.Fatalf("untrusted cover URL was retained: %q", meta.CoverURL)
	}
}

func TestEHentaiRateLimiterCancellationDoesNotReserveAnotherSlot(t *testing.T) {
	limiter := newEHIntervalLimiter(time.Hour)
	canceled, cancelImmediately := context.WithCancel(context.Background())
	cancelImmediately()
	if err := limiter.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("free limiter accepted canceled context: %v", err)
	}
	if !limiter.next.IsZero() {
		t.Fatalf("canceled free-slot waiter reserved %v", limiter.next)
	}
	if err := limiter.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	reserved := limiter.next
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := limiter.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want context canceled", err)
	}
	if !limiter.next.Equal(reserved) {
		t.Fatalf("canceled waiter moved next slot from %v to %v", reserved, limiter.next)
	}
}

func TestEHentaiCoverRedirectPolicyRejectsLeavingOfficialImageHosts(t *testing.T) {
	if err := ValidateMetadataCoverURL(config.EHentaiSitePublic, "https://127.0.0.1/admin"); err == nil {
		t.Fatal("untrusted initial EH cover URL was accepted")
	}
	policy := metadataCoverRedirectPolicy(config.EHentaiSitePublic)
	trusted, _ := http.NewRequest(http.MethodGet, "https://ul.ehgt.org/g/fixture.jpg", nil)
	if err := policy(trusted, []*http.Request{{}}); err != nil {
		t.Fatalf("trusted redirect rejected: %v", err)
	}
	untrusted, _ := http.NewRequest(http.MethodGet, "https://127.0.0.1/admin", nil)
	if err := policy(untrusted, []*http.Request{{}}); err == nil {
		t.Fatal("redirect outside EH image hosts was accepted")
	}
	port, _ := http.NewRequest(http.MethodGet, "https://ehgt.org:8443/fixture.jpg", nil)
	if err := policy(port, []*http.Request{{}}); err == nil {
		t.Fatal("non-standard EH image port was accepted")
	}
	if got := metadataCoverPolicySource("", "https://ul.ehgt.org/g/persisted.jpg"); got != config.EHentaiSitePublic {
		t.Fatalf("persisted EH cover did not restore strict redirect policy: %q", got)
	}
}

func TestEHentaiCoverDownloadLogRedactsTransportURLs(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())

	originalTransport := http.DefaultTransport
	http.DefaultTransport = ehRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &url.Error{
			Op:  "Get",
			URL: "https://redirect.example/private-token",
			Err: errors.New("dial included remote details"),
		}
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	var output bytes.Buffer
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
	})

	downloadCoverAsThumbnail("eh-log-redaction", "https://ul.ehgt.org/secret-cover.jpg")
	logged := output.String()
	if !strings.Contains(logged, errEHCoverRequest.Error()) {
		t.Fatalf("sanitized cover error missing from log: %q", logged)
	}
	for _, secret := range []string{"secret-cover.jpg", "redirect.example", "private-token", "dial included remote details"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("cover log leaked %q: %q", secret, logged)
		}
	}

	otherErr := cacheCoverAsThumbnailForSource("other-provider", "https://example.com/cover.jpg", "googlebooks")
	if otherErr == nil || !strings.Contains(otherErr.Error(), "dial included remote details") {
		t.Fatalf("non-EH transport error was unexpectedly changed: %v", otherErr)
	}
}

func TestApplyEHentaiMetadataReplacesStaleGallerySourceTag(t *testing.T) {
	setupTestDB(t)
	if _, err := store.DB().Exec(`
		INSERT INTO "Comic" ("id", "filename", "title")
		VALUES ('eh-source-replace', 'fixture.cbz', 'Fixture')
	`); err != nil {
		t.Fatal(err)
	}
	oldSource := "source:http://e-hentai.org/g/1/0123456789"
	otherProviderSource := "source:https://metadata.example/items/42"
	if err := store.AddTagsToComic("eh-source-replace", []string{
		oldSource,
		otherProviderSource,
		"artist:existing",
		"user:favorite",
	}); err != nil {
		t.Fatal(err)
	}
	newSource := "source:https://exhentai.org/g/2/abcdef0123"
	incomingTags := []string{
		"artist:updated",
		"artist:second artist",
		"female:first tag",
		"female:second tag",
		"male:example tag",
		"parody:fixture work",
		"language:english",
		"category:manga",
		newSource,
	}
	if _, err := ApplyMetadata("eh-source-replace", ComicMetadata{
		Title:  "Updated",
		Genre:  strings.Join(incomingTags, ", "),
		Source: config.EHentaiSiteRestricted,
	}, "en", true); err != nil {
		t.Fatal(err)
	}
	comic, err := store.GetComicByID("eh-source-replace")
	if err != nil || comic == nil {
		t.Fatalf("GetComicByID() error = %v", err)
	}
	gotTags := make(map[string]bool, len(comic.Tags))
	for _, tag := range comic.Tags {
		gotTags[tag.Name] = true
	}
	wantAll := append([]string{otherProviderSource, "artist:existing", "user:favorite"}, incomingTags...)
	if len(gotTags) != len(wantAll) {
		t.Fatalf("persisted tag count = %d, want %d: %#v", len(gotTags), len(wantAll), gotTags)
	}
	for _, tag := range wantAll {
		if !gotTags[tag] {
			t.Fatalf("persisted tags missing %q: %#v", tag, gotTags)
		}
	}
	if gotTags[oldSource] {
		t.Fatalf("stale EH source tag remained: %#v", gotTags)
	}
}

func TestApplyEHentaiMetadataRollsBackFieldsWhenSourceTagWriteFails(t *testing.T) {
	setupTestDB(t)
	oldSource := "source:https://e-hentai.org/g/61/aaaaaaaaaa"
	newSource := "source:https://exhentai.org/g/62/bbbbbbbbbb"
	if _, err := store.DB().Exec(`
		INSERT INTO "Comic" ("id", "filename", "title", "genre", "metadataSource")
		VALUES ('eh-atomic-apply', 'atomic.cbz', 'Old title', ?, 'ehentai')
	`, oldSource); err != nil {
		t.Fatal(err)
	}
	if err := store.AddTagsToComic("eh-atomic-apply", []string{oldSource, "user:favorite"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		CREATE TRIGGER "fail_atomic_comic_source_tag"
		BEFORE INSERT ON "ComicTag"
		WHEN (SELECT "name" FROM "Tag" WHERE "id" = NEW."tagId") = 'source:https://exhentai.org/g/62/bbbbbbbbbb'
		BEGIN
			SELECT RAISE(ABORT, 'forced comic source association failure');
		END
	`); err != nil {
		t.Fatal(err)
	}

	if _, err := ApplyMetadata("eh-atomic-apply", ComicMetadata{
		Title:  "New title",
		Genre:  "artist:new artist, " + newSource,
		Source: config.EHentaiSiteRestricted,
	}, "en", true); err == nil {
		t.Fatal("ApplyMetadata unexpectedly succeeded")
	}
	comic, err := store.GetComicByID("eh-atomic-apply")
	if err != nil || comic == nil {
		t.Fatalf("load comic after rollback: %v", err)
	}
	if comic.Title != "Old title" || comic.Genre != oldSource || comic.MetadataSource != config.EHentaiSitePublic {
		t.Fatalf("comic fields escaped rollback: %#v", comic)
	}
	gotTags := make([]string, 0, len(comic.Tags))
	for _, tag := range comic.Tags {
		gotTags = append(gotTags, tag.Name)
	}
	wantTags := map[string]bool{oldSource: true, "user:favorite": true}
	if len(gotTags) != len(wantTags) {
		t.Fatalf("tags after rollback = %#v, want %#v", gotTags, wantTags)
	}
	for _, tag := range gotTags {
		if !wantTags[tag] {
			t.Fatalf("unexpected tag after rollback %q: %#v", tag, gotTags)
		}
	}
}

func newLocalEHProvider(t *testing.T, server *httptest.Server, cfg config.EHentaiConfig) *ehentaiProvider {
	t.Helper()
	base, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	apiURL, err := url.Parse(server.URL + "/api.php")
	if err != nil {
		t.Fatal(err)
	}
	allowedOrigin := originOf(base)
	baseTransport := server.Client().Transport
	client := &http.Client{Transport: ehRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if originOf(req.URL) != allowedOrigin {
			return nil, fmt.Errorf("test transport rejected non-local request to %s", req.URL.Host)
		}
		return baseTransport.RoundTrip(req)
	})}
	provider, err := newEHentaiProviderWithEndpoints(
		cfg,
		client,
		ehEndpoints{searchBase: base, apiURL: apiURL},
		newEHIntervalLimiter(0),
		newEHIntervalLimiter(0),
	)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func assertEHCookie(t *testing.T, req *http.Request, name, want string) {
	t.Helper()
	cookie, err := req.Cookie(name)
	if err != nil {
		t.Errorf("cookie %q missing: %v", name, err)
		return
	}
	if cookie.Value != want {
		t.Errorf("cookie %q = %q, want fixture value", name, cookie.Value)
	}
}
