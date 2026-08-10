package handler

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/nowen-reader/nowen-reader/internal/model"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

func TestTagEndpointsRespectLibraryVisibility(t *testing.T) {
	router := setupTestRouter(t)
	if err := store.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	adminToken := registerAndLogin(t, router)

	for _, library := range []*model.Library{
		{
			ID:            "tag-visible-library",
			Name:          "Visible tags",
			Type:          "comic",
			RootPath:      t.TempDir(),
			Enabled:       true,
			DefaultAccess: "private",
		},
		{
			ID:            "tag-hidden-library",
			Name:          "Hidden tags",
			Type:          "comic",
			RootPath:      t.TempDir(),
			Enabled:       true,
			DefaultAccess: "private",
		},
	} {
		if err := store.CreateLibrary(library); err != nil {
			t.Fatalf("CreateLibrary(%s) error = %v", library.ID, err)
		}
	}

	if _, err := store.DB().Exec(`
		INSERT INTO "Comic" ("id", "filename", "title", "type", "libraryId", "relativePath") VALUES
			('tag-visible-comic', 'visible.cbz', 'Visible', 'comic', 'tag-visible-library', 'visible.cbz'),
			('tag-hidden-comic', 'hidden.cbz', 'Hidden', 'comic', 'tag-hidden-library', 'hidden.cbz')
	`); err != nil {
		t.Fatalf("insert comics: %v", err)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO "ComicSeries" ("id", "libraryId", "rootRelativePath", "title", "sortTitle") VALUES
			('tag-visible-series', 'tag-visible-library', 'visible', 'Visible series', 'visible series'),
			('tag-hidden-series', 'tag-hidden-library', 'hidden', 'Hidden series', 'hidden series')
	`); err != nil {
		t.Fatalf("insert series: %v", err)
	}

	const (
		visibleComicTag  = "artist:visible"
		visibleGroupTag  = "group:visible"
		visibleSeriesTag = "parody:visible"
		sharedTag        = "language:english"
		hiddenComicTag   = "source:https://exhentai.org/g/991/aaaaaaaaaa"
		hiddenGroupTag   = "source:https://e-hentai.org/g/992/bbbbbbbbbb"
		hiddenSeriesTag  = "female:hidden"
	)
	if err := store.AddTagsToComic("tag-visible-comic", []string{visibleComicTag, sharedTag}); err != nil {
		t.Fatalf("tag visible comic: %v", err)
	}
	if err := store.AddTagsToComic("tag-hidden-comic", []string{hiddenComicTag, sharedTag}); err != nil {
		t.Fatalf("tag hidden comic: %v", err)
	}
	if err := store.SetSeriesTags("tag-visible-series", []string{visibleSeriesTag}); err != nil {
		t.Fatalf("tag visible series: %v", err)
	}
	if err := store.SetSeriesTags("tag-hidden-series", []string{hiddenSeriesTag}); err != nil {
		t.Fatalf("tag hidden series: %v", err)
	}
	visibleGroupID, err := store.CreateGroupWithItems("Visible group", "", []string{"tag-visible-comic"}, nil)
	if err != nil {
		t.Fatalf("create visible group: %v", err)
	}
	hiddenGroupID, err := store.CreateGroupWithItems("Hidden group", "", nil, []string{"tag-hidden-series"})
	if err != nil {
		t.Fatalf("create hidden group: %v", err)
	}
	if err := store.SetGroupTags(int(visibleGroupID), []string{visibleGroupTag}); err != nil {
		t.Fatalf("tag visible group: %v", err)
	}
	if err := store.SetGroupTags(int(hiddenGroupID), []string{hiddenGroupTag}); err != nil {
		t.Fatalf("tag hidden group: %v", err)
	}

	reader := &model.User{
		ID:       "tag-visibility-reader",
		Username: "tag-visibility-reader",
		Password: "hash",
		Role:     "user",
	}
	if err := store.CreateUser(reader); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if err := store.SetUserLibraryAccess(reader.ID, []store.LibraryAccessReq{{
		LibraryID: "tag-visible-library",
		CanView:   true,
	}}); err != nil {
		t.Fatalf("SetUserLibraryAccess() error = %v", err)
	}
	readerToken := "tag-visibility-reader-session"
	if err := store.CreateSession(&model.UserSession{
		ID: readerToken, UserID: reader.ID, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	wantReaderTags := map[string]int{
		visibleComicTag:  1,
		visibleGroupTag:  1,
		visibleSeriesTag: 1,
		sharedTag:        1,
	}
	readerTagsResponse := performAuthedRequest(router, http.MethodGet, "/api/tags", nil, readerToken)
	if readerTagsResponse.Code != http.StatusOK {
		t.Fatalf("reader GET /api/tags = %d %s", readerTagsResponse.Code, readerTagsResponse.Body.String())
	}
	assertTagCounts(t, readerTagsResponse.Body.Bytes(), wantReaderTags)

	adminTagsResponse := performAuthedRequest(router, http.MethodGet, "/api/tags", nil, adminToken)
	if adminTagsResponse.Code != http.StatusOK {
		t.Fatalf("admin GET /api/tags = %d %s", adminTagsResponse.Code, adminTagsResponse.Body.String())
	}
	adminTags := decodeTagCounts(t, adminTagsResponse.Body.Bytes())
	for _, hiddenTag := range []string{hiddenComicTag, hiddenGroupTag, hiddenSeriesTag} {
		if adminTags[hiddenTag] != 1 {
			t.Fatalf("admin tag %q count = %d, want 1; all tags = %#v", hiddenTag, adminTags[hiddenTag], adminTags)
		}
	}
	if adminTags[sharedTag] != 2 {
		t.Fatalf("admin shared tag count = %d, want 2", adminTags[sharedTag])
	}

	exportResponse := performAuthedRequest(router, http.MethodGet, "/api/export/json", nil, readerToken)
	if exportResponse.Code != http.StatusOK {
		t.Fatalf("reader GET /api/export/json = %d %s", exportResponse.Code, exportResponse.Body.String())
	}
	assertTagCounts(t, exportResponse.Body.Bytes(), wantReaderTags)
}

func assertTagCounts(t *testing.T, body []byte, want map[string]int) {
	t.Helper()
	got := decodeTagCounts(t, body)
	if len(got) != len(want) {
		t.Fatalf("tag count = %d, want %d; tags = %#v", len(got), len(want), got)
	}
	for name, wantCount := range want {
		if got[name] != wantCount {
			t.Fatalf("tag %q count = %d, want %d; tags = %#v", name, got[name], wantCount, got)
		}
	}
}

func decodeTagCounts(t *testing.T, body []byte) map[string]int {
	t.Helper()
	var response struct {
		Tags []store.TagWithCount `json:"tags"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode tags response: %v; body = %s", err, body)
	}
	counts := make(map[string]int, len(response.Tags))
	for _, tag := range response.Tags {
		counts[tag.Name] = tag.Count
	}
	return counts
}
