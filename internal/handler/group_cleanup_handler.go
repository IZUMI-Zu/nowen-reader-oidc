package handler

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nowen-reader/nowen-reader/internal/service"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

// ============================================================
// POST /api/groups/detect-dirty — 检测系列脏数据
// ============================================================

func (h *GroupHandler) DetectDirty(c *gin.Context) {
	issues, err := store.DetectGroupDirtyData()
	if err != nil {
		log.Printf("[API] DetectDirty error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "检测脏数据失败: " + err.Error()})
		return
	}
	if issues == nil {
		issues = []store.GroupDirtyIssue{}
	}

	// 统计各类问题数量
	stats := map[string]int{
		"empty_group":    0,
		"orphan_link":    0,
		"dirty_name":     0,
		"duplicate_name": 0,
	}
	for _, issue := range issues {
		stats[issue.Type]++
	}

	c.JSON(http.StatusOK, gin.H{
		"issues": issues,
		"stats":  stats,
		"total":  len(issues),
	})
}

// ============================================================
// POST /api/groups/cleanup — 执行系列数据清理
// ============================================================

func (h *GroupHandler) Cleanup(c *gin.Context) {
	var body struct {
		Actions []string `json:"actions"` // 要执行的清理动作: empty_groups, orphan_links, dirty_names, full
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		body.Actions = []string{"full"}
	}

	// 默认执行全部清理
	if len(body.Actions) == 0 || contains(body.Actions, "full") {
		result, err := store.RunFullGroupCleanup()
		if err != nil {
			log.Printf("[API] Cleanup error: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "清理失败: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"result":  result,
		})
		return
	}

	// 按指定动作执行
	result := store.GroupCleanupResult{}
	for _, action := range body.Actions {
		switch action {
		case "empty_groups":
			n, err := store.CleanupEmptyGroups()
			if err != nil {
				log.Printf("[API] Cleanup empty_groups error: %v", err)
			}
			result.EmptyGroupsDeleted = n
		case "orphan_links":
			n, err := store.CleanupOrphanLinks()
			if err != nil {
				log.Printf("[API] Cleanup orphan_links error: %v", err)
			}
			result.OrphanLinksRemoved = n
		case "dirty_names":
			n, err := store.FixDirtyGroupNames()
			if err != nil {
				log.Printf("[API] Cleanup dirty_names error: %v", err)
			}
			result.DirtyNamesFixed = n
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"result":  result,
	})
}

// ============================================================
// POST /api/groups/fix-name — 修复单个系列名称
// ============================================================

func (h *GroupHandler) FixName(c *gin.Context) {
	var body struct {
		GroupID int    `json:"groupId"`
		NewName string `json:"newName"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.GroupID == 0 || body.NewName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数不完整"})
		return
	}

	if err := store.FixSingleGroupName(body.GroupID, body.NewName); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "修复名称失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// ============================================================
// POST /api/groups/batch-scrape — 批量刮削系列元数据
// ============================================================

// BatchScrapeResult 单个系列的批量刮削结果
type BatchScrapeResult struct {
	GroupID           int                    `json:"groupId"`
	GroupName         string                 `json:"groupName"`
	Success           bool                   `json:"success"`
	Error             string                 `json:"error,omitempty"`
	Metadata          *service.ComicMetadata `json:"metadata,omitempty"`
	Applied           bool                   `json:"applied"`
	Volumes           int                    `json:"volumes"`
	MemberSyncSkipped bool                   `json:"memberSyncSkipped,omitempty"`
}

func (h *GroupHandler) BatchScrape(c *gin.Context) {
	var body struct {
		GroupIDs      []int    `json:"groupIds"`
		Sources       []string `json:"sources"`
		Lang          string   `json:"lang"`
		Fields        []string `json:"fields"`
		Overwrite     bool     `json:"overwrite"`
		SyncTags      bool     `json:"syncTags"`
		SyncToVolumes bool     `json:"syncToVolumes"`
		AutoApply     bool     `json:"autoApply"`
		DryRun        bool     `json:"dryRun"`      // 预览模式，不实际应用
		ContentType   string   `json:"contentType"` // 可选："comic" | "novel"，为空时自动检测
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	if len(body.GroupIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择至少一个系列"})
		return
	}
	if len(body.GroupIDs) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "单次最多处理 100 个系列"})
		return
	}

	if body.Lang == "" {
		body.Lang = "zh"
	}
	sourcesExplicitlySelected := len(body.Sources) > 0
	// 如果未指定数据源，根据 contentType 自动选择默认数据源
	// 注意：如果 contentType 为空，将在每个系列处理时自动检测
	if len(body.Sources) == 0 && body.ContentType != "" {
		if body.ContentType == "novel" {
			body.Sources = []string{"googlebooks", "anilist_novel", "bangumi_novel"}
		} else {
			body.Sources = []string{"anilist", "bangumi", "mangadex", "mangaupdates", "kitsu"}
		}
	} else if len(body.Sources) == 0 {
		// 未指定 contentType 时使用漫画源作为默认（后续会按系列自动检测）
		body.Sources = []string{"anilist", "bangumi", "mangadex", "mangaupdates", "kitsu"}
	}

	fieldsSet := make(map[string]bool)
	for _, f := range body.Fields {
		fieldsSet[f] = true
	}

	results := make([]BatchScrapeResult, 0, len(body.GroupIDs))

	for _, gid := range body.GroupIDs {
		result := BatchScrapeResult{GroupID: gid}

		group, err := store.GetGroupByID(gid)
		if err != nil || group == nil {
			result.Error = "系列不存在"
			results = append(results, result)
			continue
		}
		result.GroupName = group.Name
		result.Volumes = len(group.Comics)
		policy, policyErr := store.GetGroupMetadataPolicy(gid)
		if policyErr != nil || policy == nil {
			result.Error = "读取合集结构失败"
			results = append(results, result)
			continue
		}
		allowMemberSync := policy.AllowsMemberSync()
		result.MemberSyncSkipped = !allowMemberSync && (body.SyncTags || body.SyncToVolumes)

		// 自动检测系列内容类型，选择对应的数据源和搜索策略
		groupCT := body.ContentType
		if groupCT == "" {
			groupCT = detectGroupContentType(group)
		}
		// Preserve an explicit UI selection. Only choose defaults when the
		// request omitted sources entirely.
		sources := resolveBatchMetadataSources(body.Sources, groupCT, sourcesExplicitlySelected)
		if len(sources) == 0 {
			result.Error = "所选元数据源不支持该系列内容类型"
			results = append(results, result)
			continue
		}
		options, optionsErr := groupMetadataSearchOptions(gid, sources)
		if optionsErr != nil {
			result.Error = "读取合集标签失败"
			results = append(results, result)
			continue
		}
		metaResults := searchMetadataWithOptionsContext(c.Request.Context(), group.Name, sources, body.Lang, options, groupCT)
		if len(metaResults) == 0 {
			result.Error = "未找到匹配的元数据"
			results = append(results, result)
			continue
		}

		// 取第一个结果（最佳匹配）
		bestMatch := metaResults[0]
		result.Metadata = &bestMatch
		result.Success = true

		// 预览模式不实际应用
		if body.DryRun {
			results = append(results, result)
			continue
		}

		// 自动应用模式
		if body.AutoApply {
			applyAll := len(body.Fields) == 0
			shouldApply := func(field string) bool {
				return applyAll || fieldsSet[field]
			}
			if bestMatch.CoverURL != "" && shouldApply("cover") {
				if err := service.ValidateMetadataCoverURL(bestMatch.Source, bestMatch.CoverURL); err != nil {
					result.Error = "封面地址不符合所选元数据源的安全规则"
					result.Success = false
					results = append(results, result)
					continue
				}
			}

			update := store.GroupMetadataUpdate{}
			if bestMatch.Title != "" && shouldApply("title") {
				if body.Overwrite || group.Name == "" {
					update.Name = &bestMatch.Title
				}
			}
			if bestMatch.Author != "" && shouldApply("author") {
				if body.Overwrite || group.Author == "" {
					update.Author = &bestMatch.Author
				}
			}
			if bestMatch.Description != "" && shouldApply("description") {
				if body.Overwrite || group.Description == "" {
					update.Description = &bestMatch.Description
				}
			}
			if bestMatch.Genre != "" && shouldApply("genre") {
				if body.Overwrite || group.Genre == "" {
					update.Genre = &bestMatch.Genre
				}
			}
			if bestMatch.Publisher != "" && shouldApply("publisher") {
				if body.Overwrite || group.Publisher == "" {
					update.Publisher = &bestMatch.Publisher
				}
			}
			if bestMatch.Language != "" && shouldApply("language") {
				if body.Overwrite || group.Language == "" {
					update.Language = &bestMatch.Language
				}
			}
			if bestMatch.Year != nil && shouldApply("year") {
				if body.Overwrite || group.Year == nil {
					update.Year = bestMatch.Year
				}
			}
			if bestMatch.CoverURL != "" && shouldApply("cover") {
				update.CoverURL = &bestMatch.CoverURL
			}
			if bestMatch.ExternalRating != nil && shouldApply("rating") {
				update.ExternalRating = bestMatch.ExternalRating
				update.ExternalRatingMax = bestMatch.ExternalRatingMax
				update.ExternalRatingSource = &bestMatch.ExternalRatingSource
				now := time.Now().UTC()
				update.ExternalRatingUpdatedAt = &now
			}

			var mergedTags []string
			applyTags := false
			if bestMatch.Genre != "" && shouldApply("tags") {
				genres := splitAndTrim(bestMatch.Genre)
				if len(genres) > 0 {
					existingTags, err := store.GetGroupTags(gid)
					if err != nil {
						result.Error = "读取现有标签失败: " + err.Error()
						result.Success = false
						results = append(results, result)
						continue
					}
					existingNames := make([]string, 0, len(existingTags))
					for _, t := range existingTags {
						existingNames = append(existingNames, t.Name)
					}
					mergedTags = mergeMetadataTags(existingNames, genres, update.Genre != nil)
					applyTags = true
				}
			}
			var updateErr error
			if applyTags {
				updateErr = store.UpdateGroupMetadataAndTags(gid, update, mergedTags)
			} else {
				updateErr = store.UpdateGroupMetadata(gid, update)
			}
			if updateErr != nil {
				result.Error = "应用元数据失败: " + updateErr.Error()
				result.Success = false
				results = append(results, result)
				continue
			}
			if update.CoverURL != nil {
				service.ScheduleGroupCoverRefresh(gid, *update.CoverURL, bestMatch.Source)
			}
			if applyTags && body.SyncTags && allowMemberSync {
				total, synced, _, err := store.SyncGroupTagsToVolumes(gid)
				if err != nil || synced != total {
					result.Error = fmt.Sprintf("合集标签已保存，但同步成员标签失败 (%d/%d)", synced, total)
					result.Success = false
					results = append(results, result)
					continue
				}
			}

			// 同步到所有卷
			if body.SyncToVolumes && allowMemberSync {
				successCount, errorCount, err := syncGroupMetadataToVolumes(gid, bestMatch, fieldsSet, body.Overwrite, shouldApply("rating"))
				if err != nil || errorCount > 0 {
					result.Error = fmt.Sprintf("合集元数据已保存，但同步成员字段失败 (%d success, %d errors): %v", successCount, errorCount, err)
					result.Success = false
					results = append(results, result)
					continue
				}
				log.Printf("[API] BatchScrape: synced to %d volumes (%d errors) for group %d", successCount, errorCount, gid)
			}

			result.Applied = true
		}

		results = append(results, result)
	}

	// 统计
	totalSuccess := 0
	totalFailed := 0
	totalApplied := 0
	for _, r := range results {
		if r.Success {
			totalSuccess++
		} else {
			totalFailed++
		}
		if r.Applied {
			totalApplied++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"results": results,
		"total":   len(body.GroupIDs),
		"success": totalSuccess,
		"failed":  totalFailed,
		"applied": totalApplied,
	})
}

func resolveBatchMetadataSources(requested []string, contentType string, explicitlySelected bool) []string {
	sources := append([]string(nil), requested...)
	if !explicitlySelected {
		if contentType == "novel" {
			sources = []string{"googlebooks", "anilist_novel", "bangumi_novel"}
		} else {
			sources = []string{"anilist", "bangumi", "mangadex", "mangaupdates", "kitsu"}
		}
	}
	return filterSeriesMetadataSources(sources, contentType)
}
