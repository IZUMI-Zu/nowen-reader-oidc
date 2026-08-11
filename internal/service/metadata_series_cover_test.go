package service

import (
	"bytes"
	"image/color"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nowen-reader/nowen-reader/internal/archive"
	"github.com/nowen-reader/nowen-reader/internal/config"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

// 目录作品封面和合集封面走同一套发布保护：等待者比较 URL、发布前复查数据库。
// 这个测试让旧封面 A 的下载卡在传输层，期间把封面换成 B，然后释放 A。
func TestDownloadSeriesCoverDoesNotLetOlderRequestOverwriteNewerURL(t *testing.T) {
	const seriesID = "series-cover-race"
	coverA := "https://ul.ehgt.org/a.png"
	coverB := "https://ul.ehgt.org/b.png"
	setupSeriesCoverTest(t, seriesID, coverA)
	imageA := solidPNG(t, color.RGBA{R: 255, A: 255})
	imageB := solidPNG(t, color.RGBA{B: 255, A: 255})

	aStarted := make(chan struct{})
	releaseA := make(chan struct{})
	var startOnce sync.Once
	stubGroupCoverTransport(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/a.png" {
			startOnce.Do(func() { close(aStarted) })
			<-releaseA
			return imageResponse(req, imageA), nil
		}
		return imageResponse(req, imageB), nil
	})

	oldDone := make(chan struct{})
	go func() {
		DownloadSeriesCover(seriesID, coverA, config.EHentaiSitePublic)
		close(oldDone)
	}()
	select {
	case <-aStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("old series cover request did not start")
	}

	if err := store.UpdateSeriesMetadata(seriesID, store.SeriesMetadataUpdate{CoverURL: &coverB}); err != nil {
		t.Fatal(err)
	}
	newDone := make(chan struct{})
	go func() {
		DownloadSeriesCover(seriesID, coverB, config.EHentaiSitePublic)
		close(newDone)
	}()
	close(releaseA)
	waitGroupCoverTestTask(t, "old series cover request", oldDone)
	waitGroupCoverTestTask(t, "new series cover request", newDone)

	if stored, err := store.GetSeriesStoredCoverURL(seriesID); err != nil || stored != coverB {
		t.Fatalf("stale in-flight request rolled the stored cover back to %q (%v)", stored, err)
	}
	cachePath := filepath.Join(config.GetThumbnailsDir(), archive.SeriesCoverCacheName(seriesID))
	got, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("read series cover cache: %v", err)
	}
	want, _, err := archive.ResizeImageToWebP(imageB, config.GetThumbnailWidth(), config.GetThumbnailHeight(), 85)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("series cover cache does not hold the newest cover")
	}
}

// 迟到的旧任务（goroutine 排队到新封面之后才被调度）不能把数据库和缓存
// 一起改回旧封面。
func TestDownloadSeriesCoverLateStaleTaskDoesNotRollBackTheStoredCover(t *testing.T) {
	const seriesID = "series-cover-late"
	coverA := "https://ul.ehgt.org/a.png"
	coverB := "https://ul.ehgt.org/b.png"
	setupSeriesCoverTest(t, seriesID, coverA)
	images := map[string][]byte{
		"/a.png": solidPNG(t, color.RGBA{R: 255, A: 255}),
		"/b.png": solidPNG(t, color.RGBA{B: 255, A: 255}),
	}
	stubGroupCoverTransport(t, func(req *http.Request) (*http.Response, error) {
		return imageResponse(req, images[req.URL.Path]), nil
	})

	if err := store.UpdateSeriesMetadata(seriesID, store.SeriesMetadataUpdate{CoverURL: &coverB}); err != nil {
		t.Fatal(err)
	}
	DownloadSeriesCover(seriesID, coverB, config.EHentaiSitePublic)
	// 旧封面的下载任务现在才轮到执行。
	DownloadSeriesCover(seriesID, coverA, config.EHentaiSitePublic)

	if stored, err := store.GetSeriesStoredCoverURL(seriesID); err != nil || stored != coverB {
		t.Fatalf("late stale task rolled the stored cover back to %q (%v)", stored, err)
	}
	cachePath := filepath.Join(config.GetThumbnailsDir(), archive.SeriesCoverCacheName(seriesID))
	got, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := archive.ResizeImageToWebP(images["/b.png"], config.GetThumbnailWidth(), config.GetThumbnailHeight(), 85)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("late stale task republished the old series cover")
	}
}

// 发布前复查：旧封面的下载在数据库已经换成新封面之后返回时，不允许落盘。
func TestDownloadSeriesCoverSkipsPublishAfterStoredURLChanged(t *testing.T) {
	const seriesID = "series-cover-stale"
	coverA := "https://ul.ehgt.org/a.png"
	coverB := "https://ul.ehgt.org/b.png"
	setupSeriesCoverTest(t, seriesID, coverA)
	imageA := solidPNG(t, color.RGBA{R: 255, A: 255})

	aStarted := make(chan struct{})
	releaseA := make(chan struct{})
	var startOnce sync.Once
	stubGroupCoverTransport(t, func(req *http.Request) (*http.Response, error) {
		startOnce.Do(func() { close(aStarted) })
		<-releaseA
		return imageResponse(req, imageA), nil
	})

	oldDone := make(chan struct{})
	go func() {
		DownloadSeriesCover(seriesID, coverA, config.EHentaiSitePublic)
		close(oldDone)
	}()
	select {
	case <-aStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("old series cover request did not start")
	}

	if err := store.UpdateSeriesMetadata(seriesID, store.SeriesMetadataUpdate{CoverURL: &coverB}); err != nil {
		t.Fatal(err)
	}
	close(releaseA)
	waitGroupCoverTestTask(t, "old series cover request", oldDone)

	if stored, err := store.GetSeriesStoredCoverURL(seriesID); err != nil || stored != coverB {
		t.Fatalf("stale in-flight request rolled the stored cover back to %q (%v)", stored, err)
	}
	cachePath := filepath.Join(config.GetThumbnailsDir(), archive.SeriesCoverCacheName(seriesID))
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("stale in-flight request published a series cover after the URL changed: %v", err)
	}
}

// 换封面后旧缓存必须在调度返回前就失效，否则新版本号会命中旧图片文件，
// 远端下载再失败的话旧封面就永久留下了。
func TestScheduleSeriesCoverRefreshClearsOldCacheBeforeReturning(t *testing.T) {
	const seriesID = "series-cover-refresh"
	coverB := "https://ul.ehgt.org/b.png"
	setupSeriesCoverTest(t, seriesID, coverB)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	fetched := make(chan struct{})
	var fetchOnce sync.Once
	stubGroupCoverTransport(t, func(req *http.Request) (*http.Response, error) {
		fetchOnce.Do(func() { close(fetched) })
		<-release
		return imageResponse(req, solidPNG(t, color.RGBA{B: 255, A: 255})), nil
	})

	thumbDir := config.GetThumbnailsDir()
	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(thumbDir, archive.SeriesCoverCacheName(seriesID))
	if err := os.WriteFile(cachePath, []byte("old cached cover"), 0644); err != nil {
		t.Fatal(err)
	}

	ScheduleSeriesCoverRefresh(seriesID, coverB, config.EHentaiSitePublic)
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("old series cover cache still existed when refresh scheduling returned: %v", err)
	}
	waitGroupCoverTestTask(t, "series cover refresh fetch", fetched)
}

func setupSeriesCoverTest(t *testing.T, seriesID, coverURL string) {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	setupTestDB(t)
	if err := store.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	db := store.DB()
	if _, err := db.Exec(`
		INSERT INTO "Library" ("id", "name", "rootPath") VALUES ('series-cover-lib', 'Cover library', '/tmp/series-cover')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO "ComicSeries" ("id", "libraryId", "rootRelativePath", "title", "coverUrl")
		VALUES (?, 'series-cover-lib', ?, 'Cover series', ?)
	`, seriesID, seriesID, coverURL); err != nil {
		t.Fatal(err)
	}
}
