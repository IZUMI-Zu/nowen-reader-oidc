package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/nowen-reader/nowen-reader/internal/config"
	"github.com/nowen-reader/nowen-reader/internal/model"
	"github.com/nowen-reader/nowen-reader/internal/service"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

func TestOwnerScrapePassesStoredTagsOnlyToEHOptions(t *testing.T) {
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
		ID:            "owner-search-library",
		Name:          "Owner Search",
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
		INSERT INTO "Comic" ("id", "filename", "title", "type", "libraryId", "relativePath")
		VALUES ('owner-search-comic', 'group.cbz', 'Group member', 'comic', ?, 'group.cbz');
		INSERT INTO "ComicSeries" ("id", "libraryId", "rootRelativePath", "title", "sortTitle")
		VALUES ('owner-search-series', ?, 'series', 'Series Search', 'series search');
		INSERT INTO "ComicSeriesItem" ("seriesId", "comicId", "sortIndex")
		VALUES ('owner-search-series', 'owner-search-comic', 0);
	`, library.ID, library.ID); err != nil {
		t.Fatal(err)
	}
	groupID, err := store.CreateGroupWithItems("Group Search", "", []string{"owner-search-comic"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	groupTags := []string{
		"artist:group first",
		"artist:group second",
		"source:https://e-hentai.org/g/71/aaaaaaaaaa",
	}
	seriesTags := []string{
		"artist:series first",
		"female:series tag",
		"source:https://exhentai.org/g/72/bbbbbbbbbb",
	}
	if err := store.SetGroupTags(int(groupID), groupTags); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSeriesTags("owner-search-series", seriesTags); err != nil {
		t.Fatal(err)
	}

	type capturedSearch struct {
		query   string
		sources []string
		tags    []string
	}
	var captured []capturedSearch
	originalSearch := searchMetadataWithOptionsContext
	searchMetadataWithOptionsContext = func(_ context.Context, query string, sources []string, _ string, options service.MetadataSearchOptions, _ ...string) []service.ComicMetadata {
		captured = append(captured, capturedSearch{
			query:   query,
			sources: append([]string(nil), sources...),
			tags:    append([]string(nil), options.EHentaiExistingTags...),
		})
		return []service.ComicMetadata{{Title: "Fixture result", Source: config.EHentaiSitePublic}}
	}
	t.Cleanup(func() { searchMetadataWithOptionsContext = originalSearch })

	groupResponse := performAuthedRequest(router, http.MethodPost, "/api/groups/"+strconv.FormatInt(groupID, 10)+"/scrape-metadata", map[string]interface{}{
		"query":       "group-query",
		"sources":     []string{"ehentai"},
		"contentType": "comic",
	}, cookie)
	if groupResponse.Code != http.StatusOK {
		t.Fatalf("group scrape status = %d, body = %s", groupResponse.Code, groupResponse.Body.String())
	}
	seriesResponse := performAuthedRequest(router, http.MethodPost, "/api/series/owner-search-series/scrape-metadata", map[string]interface{}{
		"query":   "series-query",
		"sources": []string{"ehentai"},
	}, cookie)
	if seriesResponse.Code != http.StatusOK {
		t.Fatalf("series scrape status = %d, body = %s", seriesResponse.Code, seriesResponse.Body.String())
	}
	batchResponse := performAuthedRequest(router, http.MethodPost, "/api/groups/batch-scrape", map[string]interface{}{
		"groupIds":    []int{int(groupID)},
		"sources":     []string{"ehentai"},
		"contentType": "comic",
		"dryRun":      true,
	}, cookie)
	if batchResponse.Code != http.StatusOK {
		t.Fatalf("batch group scrape status = %d, body = %s", batchResponse.Code, batchResponse.Body.String())
	}
	nonEHResponse := performAuthedRequest(router, http.MethodPost, "/api/groups/"+strconv.FormatInt(groupID, 10)+"/scrape-metadata", map[string]interface{}{
		"query":       "group-non-eh-query",
		"sources":     []string{"bangumi"},
		"contentType": "comic",
	}, cookie)
	if nonEHResponse.Code != http.StatusOK {
		t.Fatalf("non-EH group scrape status = %d, body = %s", nonEHResponse.Code, nonEHResponse.Body.String())
	}

	if len(captured) != 4 {
		t.Fatalf("captured searches = %#v, want 4", captured)
	}
	if captured[0].query != "group-query" || captured[1].query != "series-query" || captured[2].query != "Group Search" || captured[3].query != "group-non-eh-query" {
		t.Fatalf("captured query order = %#v", captured)
	}
	assertStringsExactly(t, groupTags, captured[0].tags)
	assertStringsExactly(t, seriesTags, captured[1].tags)
	assertStringsExactly(t, groupTags, captured[2].tags)
	if includesMetadataSource(captured[3].sources, "ehentai") || len(captured[3].tags) != 0 {
		t.Fatalf("non-EH search received EH tag context: %#v", captured[3])
	}
}

func assertStringsExactly(t *testing.T, want, got []string) {
	t.Helper()
	wantSet := make(map[string]bool, len(want))
	for _, value := range want {
		wantSet[value] = true
	}
	gotSet := make(map[string]bool, len(got))
	for _, value := range got {
		gotSet[value] = true
	}
	if len(wantSet) != len(gotSet) {
		t.Fatalf("strings = %#v, want %#v", gotSet, wantSet)
	}
	for value := range wantSet {
		if !gotSet[value] {
			t.Fatalf("strings missing %q: %#v", value, gotSet)
		}
	}
}

func TestGroupScrapeDoesNotSyncIntoDirectorySeries(t *testing.T) {
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
		ID:            "group-scrape-policy-library",
		Name:          "Group Scrape Policy",
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
			('group-policy-volume-1', 'work/01.cbz', '01', 'comic', ?, 'work/01.cbz'),
			('group-policy-volume-2', 'work/02.cbz', '02', 'comic', ?, 'work/02.cbz')
	`, library.ID, library.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO "ComicSeries" ("id", "libraryId", "rootRelativePath", "title", "sortTitle")
		VALUES ('group-policy-series', ?, 'work', 'Work', 'work')
	`, library.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO "ComicSeriesItem" ("seriesId", "comicId", "sortIndex") VALUES
			('group-policy-series', 'group-policy-volume-1', 0),
			('group-policy-series', 'group-policy-volume-2', 1)
	`); err != nil {
		t.Fatal(err)
	}
	groupID, err := store.CreateGroupWithItems("Franchise", "", nil, []string{"group-policy-series"})
	if err != nil {
		t.Fatal(err)
	}

	response := performAuthedRequest(router, http.MethodPost, "/api/groups/"+strconv.FormatInt(groupID, 10)+"/apply-metadata", map[string]interface{}{
		"metadata": map[string]interface{}{
			"author": "Group Display Author",
			"genre":  "Adventure",
			"source": "test",
		},
		"fields":        []string{"author", "genre", "tags"},
		"overwrite":     true,
		"syncTags":      true,
		"syncToVolumes": true,
	}, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("apply status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		MemberSyncAllowed bool `json:"memberSyncAllowed"`
		MemberSyncSkipped bool `json:"memberSyncSkipped"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.MemberSyncAllowed || !payload.MemberSyncSkipped {
		t.Fatalf("unexpected sync policy response: %#v", payload)
	}

	group, err := store.GetGroupByID(int(groupID))
	if err != nil || group == nil || group.Author != "Group Display Author" {
		t.Fatalf("group display metadata was not applied: %#v, err=%v", group, err)
	}
	for _, comicID := range []string{"group-policy-volume-1", "group-policy-volume-2"} {
		comic, err := store.GetComicByID(comicID)
		if err != nil || comic == nil {
			t.Fatalf("load comic %s: %v", comicID, err)
		}
		if comic.Author != "" {
			t.Fatalf("directory member %s author was overwritten: %q", comicID, comic.Author)
		}
		var tagCount int
		if err := store.DB().QueryRow(`SELECT COUNT(*) FROM "ComicTag" WHERE "comicId" = ?`, comicID).Scan(&tagCount); err != nil {
			t.Fatal(err)
		}
		if tagCount != 0 {
			t.Fatalf("directory member %s received %d group tags", comicID, tagCount)
		}
	}
}

func TestGroupScrapeReportsAtomicTagFailure(t *testing.T) {
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
	groupID, err := store.CreateGroup("Atomic scrape group")
	if err != nil {
		t.Fatal(err)
	}
	oldGenre := "artist:old handler value"
	newGenre := "artist:new handler value"
	if err := store.UpdateGroupMetadataAndTags(int(groupID), store.GroupMetadataUpdate{Genre: &oldGenre}, []string{oldGenre}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		CREATE TRIGGER "fail_handler_group_tag"
		BEFORE INSERT ON "ComicGroupTag"
		WHEN (SELECT "name" FROM "Tag" WHERE "id" = NEW."tagId") = 'artist:new handler value'
		BEGIN
			SELECT RAISE(ABORT, 'forced handler group tag failure');
		END
	`); err != nil {
		t.Fatal(err)
	}

	response := performAuthedRequest(router, http.MethodPost, "/api/groups/"+strconv.FormatInt(groupID, 10)+"/apply-metadata", map[string]interface{}{
		"metadata": map[string]interface{}{
			"genre":  newGenre,
			"source": "test",
		},
		"fields":    []string{"genre", "tags"},
		"overwrite": true,
	}, cookie)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("apply failure status = %d, body = %s", response.Code, response.Body.String())
	}
	group, err := store.GetGroupByID(int(groupID))
	if err != nil || group == nil || group.Genre != oldGenre {
		t.Fatalf("group Genre changed despite failed tags: %#v, err=%v", group, err)
	}
	tags, err := store.GetGroupTags(int(groupID))
	if err != nil || len(tags) != 1 || tags[0].Name != oldGenre {
		t.Fatalf("group tags after failed apply = %#v, err=%v", tags, err)
	}
}

func TestFirstGroupRecognitionComicUsesDirectorySeriesMember(t *testing.T) {
	group := &store.ComicGroupDetail{
		SeriesList: []store.GroupSeriesItem{
			{
				SeriesID: "series-1",
				Comics: []store.GroupComicItem{
					{ComicID: "series-volume-1", Filename: "work/01.cbz"},
				},
			},
		},
	}

	comic, ok := firstGroupRecognitionComic(group)
	if !ok {
		t.Fatal("expected a directory series member")
	}
	if comic.ComicID != "series-volume-1" {
		t.Fatalf("comic id = %q, want series-volume-1", comic.ComicID)
	}
}

func TestFirstGroupRecognitionComicPrefersDirectMember(t *testing.T) {
	group := &store.ComicGroupDetail{
		Comics: []store.GroupComicItem{
			{ComicID: "direct-volume", Filename: "direct.cbz"},
		},
		SeriesList: []store.GroupSeriesItem{
			{
				SeriesID: "series-1",
				Comics: []store.GroupComicItem{
					{ComicID: "series-volume-1", Filename: "work/01.cbz"},
				},
			},
		},
	}

	comic, ok := firstGroupRecognitionComic(group)
	if !ok {
		t.Fatal("expected a direct member")
	}
	if comic.ComicID != "direct-volume" {
		t.Fatalf("comic id = %q, want direct-volume", comic.ComicID)
	}
}
