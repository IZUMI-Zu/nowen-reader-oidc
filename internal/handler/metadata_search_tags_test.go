package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/nowen-reader/nowen-reader/internal/config"
	"github.com/nowen-reader/nowen-reader/internal/model"
	"github.com/nowen-reader/nowen-reader/internal/service"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

// 用户改写查询通常正是为了纠正上一次选错的画廊，此时不能再把已存的
// source: 标签传下去，否则搜索结果永远是那个错的画廊。
func TestMetadataSearchDropsGalleryTagsWhenQueryDiffersFromTitle(t *testing.T) {
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
		ID: "search-tags-library", Name: "Search tags", Type: "comic",
		RootPath: t.TempDir(), Enabled: true, DefaultAccess: "public", ScanEnabled: true,
	}
	if err := store.CreateLibrary(library); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO "Comic" ("id", "filename", "title", "type", "libraryId", "relativePath")
		VALUES ('search-tags-comic', 'a.cbz', 'Stored Title', 'comic', ?, 'a.cbz')
	`, library.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.AddTagsToComic("search-tags-comic", []string{"source:https://e-hentai.org/g/424242/aaaaaaaaaa/"}); err != nil {
		t.Fatal(err)
	}

	var capturedTags []string
	originalSearch := searchMetadataWithOptionsContext
	searchMetadataWithOptionsContext = func(_ context.Context, _ string, _ []string, _ string, options service.MetadataSearchOptions, _ ...string) []service.ComicMetadata {
		capturedTags = append([]string(nil), options.EHentaiExistingTags...)
		return nil
	}
	t.Cleanup(func() { searchMetadataWithOptionsContext = originalSearch })

	search := func(query string) []string {
		capturedTags = nil
		response := performAuthedRequest(router, http.MethodPost, "/api/metadata/search", map[string]any{
			"query": query, "sources": []string{config.EHentaiSitePublic}, "comicId": "search-tags-comic",
		}, cookie)
		if response.Code != http.StatusOK {
			t.Fatalf("search %q = %d %s", query, response.Code, response.Body.String())
		}
		return capturedTags
	}

	if tags := search("Stored Title"); len(tags) == 0 {
		t.Fatal("searching with the stored title dropped the gallery tags")
	}
	if tags := search("A Different Title"); len(tags) != 0 {
		t.Fatalf("rewritten query still pinned the stored gallery: %v", tags)
	}
}
