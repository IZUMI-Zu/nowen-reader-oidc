package handler

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/nowen-reader/nowen-reader/internal/config"
	"github.com/nowen-reader/nowen-reader/internal/model"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

func TestApplySeriesScrapedMetadata(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	originalConfig := config.GetSiteConfig()
	enabled := true
	siteConfig := originalConfig
	siteConfig.ScraperEnabled = &enabled
	if err := config.SaveSiteConfig(&siteConfig); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = config.SaveSiteConfig(&originalConfig) })

	router := setupTestRouter(t)
	if err := store.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	cookie := registerAndLogin(t, router)
	library := &model.Library{
		ID:            "series-scrape-library",
		Name:          "Series Scrape",
		Type:          "comic",
		RootPath:      t.TempDir(),
		Enabled:       true,
		DefaultAccess: "private",
		ScanEnabled:   true,
	}
	if err := store.CreateLibrary(library); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO "Comic" ("id", "filename", "title", "type", "libraryId", "relativePath") VALUES
			('scrape-volume-1', 'work/01.epub', '01', 'novel', ?, 'work/01.epub'),
			('scrape-volume-2', 'work/02.epub', '02', 'novel', ?, 'work/02.epub')
	`, library.ID, library.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO "ComicSeries" ("id", "libraryId", "rootRelativePath", "title", "sortTitle")
		VALUES ('series-scrape', ?, 'work', 'Work', 'work')
	`, library.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO "ComicSeriesItem" ("seriesId", "comicId", "sortIndex") VALUES
			('series-scrape', 'scrape-volume-1', 0),
			('series-scrape', 'scrape-volume-2', 1)
	`); err != nil {
		t.Fatal(err)
	}
	parentGenre := "artist:first artist, female:first tag, source:https://e-hentai.org/g/31/cccccccccc"
	memberGenre := "artist:first artist, female:first tag"

	response := performAuthedRequest(router, http.MethodPost, "/api/series/series-scrape/apply-metadata", map[string]interface{}{
		"metadata": map[string]interface{}{
			"author":      "Test Author",
			"description": "Test Description",
			"genre":       parentGenre,
			"publisher":   "Test Publisher",
			"language":    "zh",
			"year":        2026,
			"source":      "test",
		},
		"fields":        []string{"author", "description", "genre", "publisher", "language", "year", "tags"},
		"overwrite":     true,
		"syncTags":      true,
		"syncToVolumes": true,
	}, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("apply metadata status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Success     bool `json:"success"`
		SyncSuccess int  `json:"syncSuccess"`
		SyncErrors  int  `json:"syncErrors"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Success || payload.SyncSuccess != 2 || payload.SyncErrors != 0 {
		t.Fatalf("unexpected response: %#v", payload)
	}

	detail, err := store.GetSeriesDetail("series-scrape", "")
	if err != nil || detail == nil {
		t.Fatalf("GetSeriesDetail failed: %v", err)
	}
	if detail.Series.Author != "Test Author" || detail.Series.Description != "Test Description" ||
		detail.Series.Publisher != "Test Publisher" || detail.Series.Year == nil || *detail.Series.Year != 2026 ||
		detail.Series.Genre != parentGenre || len(detail.Series.Tags) != 3 {
		t.Fatalf("unexpected series metadata: %#v", detail.Series)
	}
	if detail.Series.ContentType != "comic" || detectSeriesContentType(detail) != "comic" {
		t.Fatalf("comic-library series content type = %q", detail.Series.ContentType)
	}
	if got := filterSeriesMetadataSources(
		[]string{"googlebooks", "anilist_novel", "bangumi", "ehentai", "bangumi"},
		detail.Series.ContentType,
	); !reflect.DeepEqual(got, []string{"bangumi", "ehentai"}) {
		t.Fatalf("filtered comic sources = %#v", got)
	}
	if got := filterSeriesMetadataSources([]string{"ehentai"}, "novel"); len(got) != 0 {
		t.Fatalf("EH source must not be accepted for novels: %#v", got)
	}
	existingMergeTags := []string{"artist:old", "source:http://e-hentai.org/g/1/0123456789"}
	incomingMergeTags := []string{"artist:new", "source:https://exhentai.org/g/2/abcdef0123"}
	if got := mergeMetadataTags(existingMergeTags, incomingMergeTags, true); !reflect.DeepEqual(
		got, []string{"artist:old", "artist:new", "source:https://exhentai.org/g/2/abcdef0123"}) {
		t.Fatalf("merged metadata tags = %#v", got)
	}
	// genre 这次没写入时，gallery 身份停在旧画廊，其余标签照常合并。
	if got := mergeMetadataTags(existingMergeTags, incomingMergeTags, false); !reflect.DeepEqual(
		got, []string{"artist:old", "source:http://e-hentai.org/g/1/0123456789", "artist:new"}) {
		t.Fatalf("merged metadata tags without a genre write = %#v", got)
	}
	if got := resolveBatchMetadataSources([]string{"ehentai"}, "comic", true); !reflect.DeepEqual(got, []string{"ehentai"}) {
		t.Fatalf("explicit batch EH selection was not preserved: %#v", got)
	}
	if got := resolveBatchMetadataSources([]string{"ehentai"}, "novel", true); len(got) != 0 {
		t.Fatalf("batch EH selection must be rejected for novels: %#v", got)
	}
	if !detail.Series.MetadataLocked || detail.Series.ManualLocked {
		t.Fatalf("unexpected metadata/structure locks: %#v", detail.Series)
	}
	for _, comicID := range []string{"scrape-volume-1", "scrape-volume-2"} {
		comic, err := store.GetComicByID(comicID)
		if err != nil || comic == nil {
			t.Fatalf("load comic %s: %v", comicID, err)
		}
		if comic.Author != "Test Author" || comic.Description != "Test Description" || comic.Year == nil || *comic.Year != 2026 || comic.Genre != memberGenre {
			t.Fatalf("metadata not synced to %s: %#v", comicID, comic)
		}
		gotTags := make(map[string]bool, len(comic.Tags))
		for _, tag := range comic.Tags {
			gotTags[tag.Name] = true
		}
		if !reflect.DeepEqual(gotTags, map[string]bool{"artist:first artist": true, "female:first tag": true}) {
			t.Fatalf("member %s tags = %#v", comicID, gotTags)
		}
	}

	failedGenre := "artist:must fail atomically"
	if _, err := store.DB().Exec(`
		CREATE TRIGGER "fail_handler_series_tag"
		BEFORE INSERT ON "ComicSeriesTag"
		WHEN (SELECT "name" FROM "Tag" WHERE "id" = NEW."tagId") = 'artist:must fail atomically'
		BEGIN
			SELECT RAISE(ABORT, 'forced handler series tag failure');
		END
	`); err != nil {
		t.Fatal(err)
	}
	response = performAuthedRequest(router, http.MethodPost, "/api/series/series-scrape/apply-metadata", map[string]interface{}{
		"metadata": map[string]interface{}{
			"genre":  failedGenre,
			"source": "test",
		},
		"fields":    []string{"genre", "tags"},
		"overwrite": true,
	}, cookie)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("atomic failure status = %d, body = %s", response.Code, response.Body.String())
	}
	afterFailure, err := store.GetSeriesDetail("series-scrape", "")
	if err != nil || afterFailure == nil || afterFailure.Series.Genre != parentGenre || len(afterFailure.Series.Tags) != 3 {
		t.Fatalf("series changed despite atomic failure: %#v, err=%v", afterFailure, err)
	}

	manager := &model.User{ID: "series-manager", Username: "series-manager", Password: "hash", Role: "user"}
	if err := store.CreateUser(manager); err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserLibraryAccess(manager.ID, []store.LibraryAccessReq{{
		LibraryID: library.ID,
		CanView:   true,
		CanManage: true,
	}}); err != nil {
		t.Fatal(err)
	}
	managerSession := "series-manager-session"
	if err := store.CreateSession(&model.UserSession{
		ID: managerSession, UserID: manager.ID, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	response = performAuthedRequest(router, http.MethodPost, "/api/series/series-scrape/apply-metadata", map[string]interface{}{}, managerSession)
	if response.Code != http.StatusForbidden {
		t.Fatalf("non-admin manager apply status = %d, want 403", response.Code)
	}
}
