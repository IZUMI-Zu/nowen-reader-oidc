package service

import (
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nowen-reader/nowen-reader/internal/archive"
	"github.com/nowen-reader/nowen-reader/internal/config"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

const maxCoverDownloadBytes = 20 << 20

// 合集封面下载去重：同一 groupID 同时只有一个下载任务，其余等待结果
var groupCoverDownload sync.Map  // groupID -> chan struct{}
var seriesCoverDownload sync.Map // seriesID -> chan struct{}

// ============================================================
// Apply metadata to comic
// ============================================================

// BuildRatingUpdates 构建外部评分的更新字段 map，避免在多处重复相同的逻辑。
// 如果 metadata 中没有评分信息，返回 nil。
func BuildRatingUpdates(meta ComicMetadata) map[string]interface{} {
	if meta.ExternalRating == nil {
		return nil
	}
	updates := map[string]interface{}{
		"externalRating":       *meta.ExternalRating,
		"externalRatingSource": meta.ExternalRatingSource,
	}
	if meta.ExternalRatingMax != nil {
		updates["externalRatingMax"] = *meta.ExternalRatingMax
	}
	if meta.ExternalRatingUpdatedAt != nil {
		updates["externalRatingUpdatedAt"] = *meta.ExternalRatingUpdatedAt
	} else {
		updates["externalRatingUpdatedAt"] = time.Now().UTC()
	}
	return updates
}

// ApplyMetadata updates comic fields in DB from metadata.
func ApplyMetadata(comicID string, meta ComicMetadata, lang string, overwrite bool, opts ...ApplyOption) (*store.ComicListItem, error) {
	// 解析可选参数
	opt := ApplyOption{}
	if len(opts) > 0 {
		opt = opts[0]
	}

	existing, err := store.GetComicByID(comicID)
	if err != nil || existing == nil {
		return nil, fmt.Errorf("comic not found: %s", comicID)
	}
	if err := ValidateMetadataCoverURL(meta.Source, meta.CoverURL); err != nil {
		return nil, err
	}

	updates := map[string]interface{}{}

	shouldUpdate := func(current string) bool {
		return overwrite || current == ""
	}

	if meta.Title != "" && shouldUpdate(existing.Title) {
		updates["title"] = meta.Title
	}
	if meta.Author != "" && shouldUpdate(existing.Author) {
		updates["author"] = meta.Author
	}
	if meta.Publisher != "" && shouldUpdate(existing.Publisher) {
		updates["publisher"] = meta.Publisher
	}
	if meta.Description != "" && shouldUpdate(existing.Description) {
		updates["description"] = meta.Description
	}
	if meta.Language != "" && shouldUpdate(existing.Language) {
		updates["language"] = meta.Language
	}
	if meta.Genre != "" && shouldUpdate(existing.Genre) {
		updates["genre"] = meta.Genre
	}
	if meta.Year != nil {
		if overwrite || existing.Year == nil {
			updates["year"] = *meta.Year
		}
	}
	if meta.Source != "" {
		updates["metadataSource"] = meta.Source
	}
	// P2-A: 当 skipCover 为 true 时，跳过封面更新
	if meta.CoverURL != "" && !opt.SkipCover {
		updates["coverImageUrl"] = meta.CoverURL
	}
	// External rating
	for k, v := range BuildRatingUpdates(meta) {
		updates[k] = v
	}

	// Resolve normalized tags before writing so comic fields and the EH/EX
	// gallery identity can be committed by one store transaction.
	var tagNames []string
	var replaceMatcher func(string) bool
	if meta.Genre != "" {
		for _, rawTag := range strings.Split(meta.Genre, ",") {
			tagName := strings.TrimSpace(rawTag)
			if tagName == "" {
				continue
			}
			tagNames = append(tagNames, tagName)
			if IsEHentaiGallerySourceTag(tagName) {
				replaceMatcher = IsEHentaiGallerySourceTag
			}
		}
	}

	if len(tagNames) > 0 {
		if err := store.UpdateComicFieldsAndTagsReplacingMatching(comicID, updates, tagNames, replaceMatcher); err != nil {
			return nil, fmt.Errorf("update comic metadata and tags: %w", err)
		}
	} else if len(updates) > 0 {
		if err := store.UpdateComicFields(comicID, updates); err != nil {
			return nil, fmt.Errorf("update comic fields: %w", err)
		}
	}

	// Download cover image as thumbnail（仅在不跳过封面时）。
	// 先清理旧缓存，避免前端在后台下载完成前继续命中旧封面。
	if meta.CoverURL != "" && !opt.SkipCover {
		archive.ClearThumbnailCache(comicID)
		go func() {
			if err := cacheCoverAsThumbnailForSource(comicID, meta.CoverURL, meta.Source); err != nil {
				log.Printf("[metadata] Cover cache failed for %s: %v", comicID, err)
			}
		}()
	}

	return store.GetComicByID(comicID)
}

// downloadCoverAsThumbnail fetches a cover URL and saves as WebP thumbnail.
func downloadCoverAsThumbnail(comicID, coverURL string) {
	if err := cacheCoverAsThumbnail(comicID, coverURL); err != nil {
		log.Printf("[metadata] Cover cache failed for %s: %v", comicID, err)
	}
}

func cacheCoverAsThumbnail(comicID, coverURL string) error {
	return cacheCoverAsThumbnailForSource(comicID, coverURL, "")
}

func cacheCoverAsThumbnailForSource(comicID, coverURL, metadataSource string) error {
	// Bangumi 等源可能返回 http:// URL，Go HTTP 客户端会跟随重定向，
	// 但显式转为 https 更安全
	coverURL = strings.Replace(coverURL, "http://", "https://", 1)
	metadataSource = metadataCoverPolicySource(metadataSource, coverURL)

	thumbDir := config.GetThumbnailsDir()
	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		return err
	}

	client := metadataCoverHTTPClient(metadataSource)
	req, err := http.NewRequest("GET", coverURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "NowenReader/1.0")

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		if err != nil {
			return err
		}
		return fmt.Errorf("download cover returned HTTP %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	imgData, err := io.ReadAll(io.LimitReader(resp.Body, maxCoverDownloadBytes+1))
	if err != nil || len(imgData) == 0 {
		if err != nil {
			return err
		}
		return fmt.Errorf("empty cover response")
	}
	if len(imgData) > maxCoverDownloadBytes {
		return fmt.Errorf("cover download too large")
	}

	thumbPath := filepath.Join(thumbDir, archive.ThumbnailCacheName(comicID))
	webpData, _, err := archive.ResizeImageToWebP(imgData, config.GetThumbnailWidth(), config.GetThumbnailHeight(), 85)
	if err != nil {
		return fmt.Errorf("convert cover: %w", err)
	}
	archive.ClearThumbnailCache(comicID)
	if err := os.WriteFile(thumbPath, webpData, 0644); err != nil {
		return err
	}
	log.Printf("[metadata] Cover cached for %s", comicID)
	return nil
}

// DownloadGroupCover 保存系列封面 URL 到数据库，并下载到本地缓存。
func DownloadGroupCover(groupID int, coverURL string, metadataSources ...string) {
	if coverURL == "" {
		return
	}
	metadataSource := metadataCoverPolicySource(firstMetadataSource(metadataSources), coverURL)
	if err := ValidateMetadataCoverURL(metadataSource, coverURL); err != nil {
		log.Printf("[metadata] Group cover rejected for group %d: %v", groupID, err)
		return
	}
	// Bangumi 等源可能返回 http:// URL，强制转为 https://
	coverURL = strings.Replace(coverURL, "http://", "https://", 1)

	// 保存外部 URL 到数据库
	if err := store.UpdateGroupMetadata(groupID, store.GroupMetadataUpdate{
		CoverURL: &coverURL,
	}); err != nil {
		log.Printf("[metadata] Group cover URL save failed for group %d: %v", groupID, err)
		return
	}
	log.Printf("[metadata] Group cover URL saved for group %d", groupID)

	// 下载封面到本地缓存
	downloadGroupCoverToLocal(groupID, coverURL, metadataSource)
}

// downloadGroupCoverToLocal 下载合集封面图片并保存为本地 WebP 缩略图。
// 使用去重机制确保同一 groupID 同时只有一个下载任务。
func downloadGroupCoverToLocal(groupID int, coverURL, metadataSource string) {
	thumbDir := config.GetThumbnailsDir()
	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		return
	}

	// 快速路径：磁盘缓存命中
	cacheName := archive.GroupCoverCacheName(groupID)
	cachePath := filepath.Join(thumbDir, cacheName)
	if data, err := os.ReadFile(cachePath); err == nil && len(data) > 0 {
		return
	}

	// 去重：如果同一 groupID 正在下载，等待其完成后重新检查缓存
	ch, loaded := groupCoverDownload.LoadOrStore(groupID, make(chan struct{}, 1))
	done := ch.(chan struct{})
	if loaded {
		// 其他 goroutine 正在下载，等待完成
		<-done
		// 重新检查缓存是否被成功写入
		if data, err := os.ReadFile(cachePath); err == nil && len(data) > 0 {
			return
		}
		// 下载失败，等待下次请求时重新尝试
		return
	}

	// 当前 goroutine 负责下载，完成后通知等待者
	defer func() {
		close(done)
		groupCoverDownload.Delete(groupID)
	}()

	downloadGroupCoverToLocalInternal(groupID, coverURL, metadataSource, thumbDir, cachePath)
}

// downloadGroupCoverToLocalInternal 执行实际的封面下载和保存逻辑。
func downloadGroupCoverToLocalInternal(groupID int, coverURL, metadataSource, thumbDir, cachePath string) {
	client := metadataCoverHTTPClient(metadataSource)
	req, err := http.NewRequest("GET", coverURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", "NowenReader/1.0")

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return
	}
	defer resp.Body.Close()

	imgData, err := io.ReadAll(io.LimitReader(resp.Body, maxCoverDownloadBytes+1))
	if err != nil || len(imgData) == 0 {
		return
	}
	if len(imgData) > maxCoverDownloadBytes {
		log.Printf("[metadata] Group cover download too large for group %d", groupID)
		return
	}

	webpData, _, err := archive.ResizeImageToWebP(imgData, config.GetThumbnailWidth(), config.GetThumbnailHeight(), 85)
	if err != nil {
		// Fallback: 保存原始图片数据
		log.Printf("[metadata] Group cover cache failed for group %d: %v", groupID, err)
		return
	}
	archive.ClearGroupCoverCache(groupID)
	_ = os.WriteFile(cachePath, webpData, 0644)
	log.Printf("[metadata] Group cover cached locally for group %d", groupID)
}

func CacheGroupCoverDataURL(groupID int, coverDataURL string) error {
	comma := strings.Index(coverDataURL, ",")
	if comma <= 0 || !strings.HasPrefix(coverDataURL, "data:image/") {
		return fmt.Errorf("unsupported data URL")
	}
	meta := coverDataURL[:comma]
	if !strings.Contains(meta, ";base64") {
		return fmt.Errorf("unsupported non-base64 data URL")
	}
	imgData, err := base64.StdEncoding.DecodeString(coverDataURL[comma+1:])
	if err != nil {
		return err
	}
	if len(imgData) == 0 || len(imgData) > maxCoverDownloadBytes {
		return fmt.Errorf("invalid image size")
	}

	thumbDir := config.GetThumbnailsDir()
	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		return err
	}
	webpData, _, err := archive.ResizeImageToWebP(imgData, config.GetThumbnailWidth(), config.GetThumbnailHeight(), 85)
	if err != nil {
		return err
	}
	archive.ClearGroupCoverCache(groupID)
	cachePath := filepath.Join(thumbDir, archive.GroupCoverCacheName(groupID))
	if err := os.WriteFile(cachePath, webpData, 0644); err != nil {
		return err
	}
	log.Printf("[metadata] Group cover cached locally for group %d", groupID)
	return nil
}

func DownloadSeriesCover(seriesID, coverURL string, metadataSources ...string) {
	if seriesID == "" || coverURL == "" {
		return
	}
	metadataSource := metadataCoverPolicySource(firstMetadataSource(metadataSources), coverURL)
	if err := ValidateMetadataCoverURL(metadataSource, coverURL); err != nil {
		log.Printf("[metadata] Series cover rejected for %s: %v", seriesID, err)
		return
	}
	coverURL = strings.Replace(coverURL, "http://", "https://", 1)
	if err := store.UpdateSeriesMetadata(seriesID, store.SeriesMetadataUpdate{CoverURL: &coverURL}); err != nil {
		log.Printf("[metadata] Series cover URL save failed for %s: %v", seriesID, err)
		return
	}

	thumbDir := config.GetThumbnailsDir()
	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		return
	}
	cachePath := filepath.Join(thumbDir, archive.SeriesCoverCacheName(seriesID))
	ch, loaded := seriesCoverDownload.LoadOrStore(seriesID, make(chan struct{}))
	done := ch.(chan struct{})
	if loaded {
		<-done
		return
	}
	defer func() {
		close(done)
		seriesCoverDownload.Delete(seriesID)
	}()

	client := metadataCoverHTTPClient(metadataSource)
	req, err := http.NewRequest(http.MethodGet, coverURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", "NowenReader/1.0")
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return
	}
	defer resp.Body.Close()
	imgData, err := io.ReadAll(io.LimitReader(resp.Body, maxCoverDownloadBytes+1))
	if err != nil || len(imgData) == 0 || len(imgData) > maxCoverDownloadBytes {
		return
	}
	webpData, _, err := archive.ResizeImageToWebP(imgData, config.GetThumbnailWidth(), config.GetThumbnailHeight(), 85)
	if err != nil {
		return
	}
	archive.ClearSeriesCoverCache(seriesID)
	if err := os.WriteFile(cachePath, webpData, 0644); err != nil {
		return
	}
	log.Printf("[metadata] Series cover cached locally for %s", seriesID)
}

// ValidateMetadataCoverURL applies provider-specific trust rules before a
// client-supplied metadata result can be persisted or fetched.
func ValidateMetadataCoverURL(metadataSource, coverURL string) error {
	if coverURL == "" || !isEHMetadataSource(metadataSource) {
		return nil
	}
	if safeEHCoverURL(coverURL) == "" {
		return fmt.Errorf("E-Hentai cover URL was rejected")
	}
	return nil
}

func metadataCoverHTTPClient(metadataSource string) *http.Client {
	client := &http.Client{Timeout: 30 * time.Second}
	if isEHMetadataSource(metadataSource) {
		client.CheckRedirect = metadataCoverRedirectPolicy(metadataSource)
	}
	return client
}

func metadataCoverRedirectPolicy(metadataSource string) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many E-Hentai cover redirects")
		}
		if isEHMetadataSource(metadataSource) && safeEHCoverURL(req.URL.String()) == "" {
			return fmt.Errorf("E-Hentai cover redirect was rejected")
		}
		return nil
	}
}

func isEHMetadataSource(source string) bool {
	return source == config.EHentaiSitePublic || source == config.EHentaiSiteRestricted
}

func metadataCoverPolicySource(metadataSource, coverURL string) string {
	if isEHMetadataSource(metadataSource) {
		return metadataSource
	}
	// Persisted cover rows do not retain provider context on every entity. An
	// official EH image URL is sufficient to restore the strict redirect policy
	// when a missing cache is rebuilt later.
	if safeEHCoverURL(coverURL) != "" {
		return config.EHentaiSitePublic
	}
	return metadataSource
}

func firstMetadataSource(sources []string) string {
	if len(sources) == 0 {
		return ""
	}
	return sources[0]
}

// ============================================================
// HTTP helpers
// ============================================================

// maxRetries429 是遇到 HTTP 429 时的最大重试次数
const maxRetries429 = 3

// retryAfterFromHeader 从 Retry-After 头中解析等待秒数，默认返回 fallback
