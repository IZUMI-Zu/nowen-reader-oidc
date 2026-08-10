package service

import (
	"context"
	"strings"
)

var (
	defaultComicMetadataSources = []string{"anilist", "bangumi", "mangadex", "mangaupdates", "kitsu"}
	defaultNovelMetadataSources = []string{"googlebooks", "anilist_novel", "bangumi_novel"}
)

// MetadataSearchOptions carries source-specific context that must not be folded
// into the shared query sent to unrelated providers.
type MetadataSearchOptions struct {
	EHentaiExistingTags []string
}

// ============================================================
// Unified search (parallel)
// ============================================================

// SearchMetadata searches multiple sources concurrently.
// contentType: "comic" | "novel" | "" (auto-detect default sources).
func SearchMetadata(query string, sources []string, lang string, contentType ...string) []ComicMetadata {
	return SearchMetadataWithOptionsContext(context.Background(), query, sources, lang, MetadataSearchOptions{}, contentType...)
}

// SearchMetadataWithOptions searches multiple sources with optional
// provider-specific hints.
func SearchMetadataWithOptions(query string, sources []string, lang string, options MetadataSearchOptions, contentType ...string) []ComicMetadata {
	return SearchMetadataWithOptionsContext(context.Background(), query, sources, lang, options, contentType...)
}

// SearchMetadataWithContext propagates request cancellation to sources that
// support it, including EH/EX rate-limit waits and HTTP requests.
func SearchMetadataWithContext(ctx context.Context, query string, sources []string, lang string, contentType ...string) []ComicMetadata {
	return SearchMetadataWithOptionsContext(ctx, query, sources, lang, MetadataSearchOptions{}, contentType...)
}

// SearchMetadataWithOptionsContext is the context-aware search entry point.
func SearchMetadataWithOptionsContext(ctx context.Context, query string, sources []string, lang string, options MetadataSearchOptions, contentType ...string) []ComicMetadata {
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
	all := doSearch(ctx, query, sources, lang, options)

	// 多重查询策略：如果主搜索结果为空或质量不佳，尝试清洗后的查询
	cleanedQuery := CleanTitle(query)
	retrySources := metadataRetrySources(sources, options)
	if len(retrySources) > 0 && cleanedQuery != "" && cleanedQuery != query && len(cleanedQuery) >= 2 {
		if len(all) == 0 {
			// 主搜索无结果，用清洗后查询重新搜索
			all = doSearch(ctx, cleanedQuery, retrySources, lang, options)
		} else {
			// 主搜索有结果但不多，用清洗后查询补充搜索并合并
			if len(all) < 3 {
				extra := doSearch(ctx, cleanedQuery, retrySources, lang, options)
				all = mergeResults(all, extra)
			}
		}
	}

	// 按标题与搜索关键词的匹配度排序，优先返回最相关的结果
	sortByRelevance(all, query)

	return all
}

func metadataRetrySources(sources []string, options MetadataSearchOptions) []string {
	if _, hasExactEHSource := galleryRefFromEHTags(options.EHentaiExistingTags); !hasExactEHSource {
		return sources
	}
	retry := make([]string, 0, len(sources))
	for _, source := range sources {
		if source != "ehentai" {
			retry = append(retry, source)
		}
	}
	return retry
}

// doSearch 执行并行搜索
func doSearch(ctx context.Context, query string, sources []string, lang string, options MetadataSearchOptions) []ComicMetadata {
	type result struct {
		data []ComicMetadata
	}

	ch := make(chan result, len(sources))
	for _, src := range sources {
		go func(s string) {
			if ctx.Err() != nil {
				ch <- result{}
				return
			}
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
				ch <- result{SearchEHentaiWithTagsContext(ctx, query, lang, options.EHentaiExistingTags)}
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
