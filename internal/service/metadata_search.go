package service

import (
	"strings"
)

var (
	defaultComicMetadataSources = []string{"anilist", "bangumi", "mangadex", "mangaupdates", "kitsu"}
	defaultNovelMetadataSources = []string{"googlebooks", "anilist_novel", "bangumi_novel"}
)

// ============================================================
// Unified search (parallel)
// ============================================================

// SearchMetadata searches multiple sources concurrently.
// contentType: "comic" | "novel" | "" (auto-detect default sources).
func SearchMetadata(query string, sources []string, lang string, contentType ...string) []ComicMetadata {
	ct := ""
	if len(contentType) > 0 {
		ct = contentType[0]
	}

	if len(sources) == 0 {
		switch ct {
		case "novel":
			sources = append([]string(nil), defaultNovelMetadataSources...)
		case "comic":
			sources = append([]string(nil), defaultComicMetadataSources...)
		default:
			sources = append([]string(nil), defaultComicMetadataSources...)
		}
	}

	// 主搜索
	all := doSearch(query, sources, lang)

	// 多重查询策略：如果主搜索结果为空或质量不佳，尝试清洗后的查询
	cleanedQuery := CleanTitle(query)
	if cleanedQuery != "" && cleanedQuery != query && len(cleanedQuery) >= 2 {
		if len(all) == 0 {
			// 主搜索无结果，用清洗后查询重新搜索
			all = doSearch(cleanedQuery, sources, lang)
		} else {
			// 主搜索有结果但不多，用清洗后查询补充搜索并合并
			if len(all) < 3 {
				extra := doSearch(cleanedQuery, sources, lang)
				all = mergeResults(all, extra)
			}
		}
	}

	// 按标题与搜索关键词的匹配度排序，优先返回最相关的结果
	sortByRelevance(all, query)

	return all
}

// doSearch 执行并行搜索
func doSearch(query string, sources []string, lang string) []ComicMetadata {
	type result struct {
		data []ComicMetadata
	}

	ch := make(chan result, len(sources))
	for _, src := range sources {
		go func(s string) {
			switch s {
			case "anilist":
				ch <- result{SearchAniList(query, lang)}
			case "anilist_novel":
				ch <- result{SearchAniListNovel(query, lang)}
			case "bangumi":
				ch <- result{SearchBangumi(query, lang)}
			case "bangumi_novel":
				ch <- result{SearchBangumiNovel(query, lang)}
			case "mangadex":
				ch <- result{SearchMangaDex(query, lang)}
			case "mangaupdates":
				ch <- result{SearchMangaUpdates(query, lang)}
			case "kitsu":
				ch <- result{SearchKitsu(query, lang)}
			case "googlebooks":
				ch <- result{SearchGoogleBooks(query, lang)}
			case "ehentai":
				ch <- result{SearchEHentai(query, lang)}
			default:
				ch <- result{}
			}
		}(src)
	}

	var all []ComicMetadata
	for range sources {
		r := <-ch
		all = append(all, r.data...)
	}
	return all
}

// mergeResults 合并两组搜索结果，去除标题+来源相同的重复项
func mergeResults(primary, extra []ComicMetadata) []ComicMetadata {
	seen := make(map[string]bool)
	for _, m := range primary {
		key := strings.ToLower(m.Title) + "|" + m.Source
		seen[key] = true
	}
	merged := append([]ComicMetadata{}, primary...)
	for _, m := range extra {
		key := strings.ToLower(m.Title) + "|" + m.Source
		if !seen[key] {
			merged = append(merged, m)
			seen[key] = true
		}
	}
	return merged
}
