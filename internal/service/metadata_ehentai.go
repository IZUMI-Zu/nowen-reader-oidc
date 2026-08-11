package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nowen-reader/nowen-reader/internal/config"
	"github.com/nowen-reader/nowen-reader/internal/store"
	xhtml "golang.org/x/net/html"
)

const (
	ehPublicOrigin        = "https://e-hentai.org"
	ehRestrictedOrigin    = "https://exhentai.org"
	ehAPIEndpoint         = "https://api.e-hentai.org/api.php"
	ehUserAgent           = "NowenReader/1.0"
	ehSearchInterval      = 4 * time.Second
	ehAPIInterval         = 1 * time.Second
	ehRequestTimeout      = 15 * time.Second
	ehMaxSearchBodyBytes  = 2 << 20
	ehMaxAPIResponseBytes = 4 << 20
	ehMaxSearchResults    = 10
	ehMaxAPIEntries       = 25
	ehMaxGalleryTags      = 256
	ehMaxTagBytes         = 256
	ehMaxTitleBytes       = 4096
	ehMaxCoverURLBytes    = 2048
)

var (
	errEHInvalidQuery      = errors.New("invalid E-Hentai query")
	errEHAuthentication    = errors.New("E-Hentai authentication required")
	errEHRateLimited       = errors.New("E-Hentai rate limited")
	errEHTemporarilyBanned = errors.New("E-Hentai temporarily banned this client")
	errEHForbiddenRedirect = errors.New("E-Hentai redirect left the allowed origins")
	errEHResponseTooLarge  = errors.New("E-Hentai response exceeded the size limit")
	errEHInvalidResponse   = errors.New("invalid E-Hentai response")
	errEHInvalidGallery    = errors.New("invalid or unavailable E-Hentai gallery")
	errEHRemote            = errors.New("E-Hentai remote request failed")

	ehGalleryTokenPattern = regexp.MustCompile(`^[0-9a-fA-F]{10}$`)
	// 只把足够长的方括号数字当作画廊 ID。汉化/同人命名里的 [2021]、[01]
	// 这类年份和卷号如果被当成 gid，整个标题就会被丢弃。
	ehTitleGIDPattern = regexp.MustCompile(`\[([0-9]{6,12})\]`)
	defaultEHSearchGate   = newEHIntervalLimiter(ehSearchInterval)
	defaultEHAPIGate      = newEHIntervalLimiter(ehAPIInterval)
)

type ehGalleryRef struct {
	GID   int64
	Token string
	Site  string
}

type ehEndpoints struct {
	searchBase *url.URL
	apiURL     *url.URL
}

type ehentaiProvider struct {
	cfg            config.EHentaiConfig
	client         *http.Client
	endpoints      ehEndpoints
	searchGate     *ehIntervalLimiter
	apiGate        *ehIntervalLimiter
	allowedOrigins map[string]struct{}
	cookieOrigins  map[string]struct{}
}

type ehIntervalLimiter struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
	now      func() time.Time
}

func newEHIntervalLimiter(interval time.Duration) *ehIntervalLimiter {
	return &ehIntervalLimiter{interval: interval, now: time.Now}
}

func (l *ehIntervalLimiter) Wait(ctx context.Context) error {
	if l == nil || l.interval <= 0 {
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		l.mu.Lock()
		if err := ctx.Err(); err != nil {
			l.mu.Unlock()
			return err
		}
		now := l.now()
		wait := l.next.Sub(now)
		if wait <= 0 {
			l.next = now.Add(l.interval)
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
			// Competing waiters re-check the next slot instead of reserving an
			// unbounded queue of future slots.
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		}
	}
}

// SearchEHentai searches the explicitly enabled E-Hentai or ExHentai metadata
// source. Configuration failures and remote failures are intentionally reduced
// to a sanitized log entry, matching the behavior of the other metadata
// sources without exposing account cookies, response bodies, or search terms.
func SearchEHentai(query, lang string) []ComicMetadata {
	return SearchEHentaiWithTagsContext(context.Background(), query, lang, nil)
}

// SearchEHentaiWithTags uses trusted, already-stored archive tags as optional
// hints. A valid source tag bypasses title search; an ASCII artist tag narrows
// the EH query without changing what is sent to other metadata providers.
func SearchEHentaiWithTags(query, lang string, existingTags []string) []ComicMetadata {
	return SearchEHentaiWithTagsContext(context.Background(), query, lang, existingTags)
}

// SearchEHentaiWithTagsContext cancels queued rate-limit waits and in-flight
// requests when the originating HTTP request is canceled.
func SearchEHentaiWithTagsContext(ctx context.Context, query, lang string, existingTags []string) []ComicMetadata {
	cfg, err := config.GetEHentaiConfig()
	if err != nil {
		log.Printf("[metadata] E-Hentai source unavailable: %s", safeEHError(err))
		return nil
	}
	if !cfg.Enabled {
		return nil
	}
	provider, err := newEHentaiProvider(cfg)
	if err != nil {
		log.Printf("[metadata] E-Hentai source unavailable: %s", safeEHError(err))
		return nil
	}
	results, err := provider.searchWithTags(ctx, query, lang, existingTags)
	if err != nil {
		log.Printf("[metadata] E-Hentai search failed: %s", safeEHError(err))
		return nil
	}
	return results
}

func newEHentaiProvider(cfg config.EHentaiConfig) (*ehentaiProvider, error) {
	searchOrigin := ehPublicOrigin
	if cfg.Site == config.EHentaiSiteRestricted {
		searchOrigin = ehRestrictedOrigin
	}
	searchBase, err := url.Parse(searchOrigin + "/")
	if err != nil {
		return nil, errEHInvalidResponse
	}
	apiURL, err := url.Parse(ehAPIEndpoint)
	if err != nil {
		return nil, errEHInvalidResponse
	}
	return newEHentaiProviderWithEndpoints(cfg, nil, ehEndpoints{
		searchBase: searchBase,
		apiURL:     apiURL,
	}, defaultEHSearchGate, defaultEHAPIGate)
}

// newEHentaiProviderWithEndpoints is an internal test seam. Production callers
// always use fixed HTTPS endpoints from newEHentaiProvider.
func newEHentaiProviderWithEndpoints(cfg config.EHentaiConfig, baseClient *http.Client, endpoints ehEndpoints, searchGate, apiGate *ehIntervalLimiter) (*ehentaiProvider, error) {
	if endpoints.searchBase == nil || endpoints.apiURL == nil || endpoints.searchBase.Host == "" || endpoints.apiURL.Host == "" {
		return nil, errEHInvalidResponse
	}
	allowedOrigins := map[string]struct{}{
		originOf(endpoints.searchBase): {},
		originOf(endpoints.apiURL):     {},
	}
	// Production redirects can legitimately remain within the three EH origins.
	if endpoints.searchBase.Scheme == "https" && (endpoints.searchBase.Hostname() == "e-hentai.org" || endpoints.searchBase.Hostname() == "exhentai.org") {
		allowedOrigins[ehPublicOrigin] = struct{}{}
		allowedOrigins[ehRestrictedOrigin] = struct{}{}
		allowedOrigins["https://api.e-hentai.org"] = struct{}{}
	}

	client := &http.Client{Timeout: ehRequestTimeout}
	if baseClient != nil {
		*client = *baseClient
		if client.Timeout <= 0 || client.Timeout > ehRequestTimeout {
			client.Timeout = ehRequestTimeout
		}
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errEHForbiddenRedirect
		}
		if _, ok := allowedOrigins[originOf(req.URL)]; !ok {
			return errEHForbiddenRedirect
		}
		return nil
	}

	return &ehentaiProvider{
		cfg:            cfg,
		client:         client,
		endpoints:      endpoints,
		searchGate:     searchGate,
		apiGate:        apiGate,
		allowedOrigins: allowedOrigins,
		cookieOrigins: map[string]struct{}{
			originOf(endpoints.searchBase): {},
			originOf(endpoints.apiURL):     {},
		},
	}, nil
}

func (p *ehentaiProvider) search(ctx context.Context, query, _ string) ([]ComicMetadata, error) {
	return p.searchWithTags(ctx, query, "", nil)
}

func (p *ehentaiProvider) searchWithTags(ctx context.Context, query, _ string, existingTags []string) ([]ComicMetadata, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errEHInvalidQuery
	}
	if ref, ok := parseEHGalleryURL(query); ok {
		return p.fetchGalleryMetadata(ctx, []ehGalleryRef{ref})
	}
	if ref, ok := galleryRefFromEHTags(existingTags); ok {
		results, err := p.fetchGalleryMetadata(ctx, []ehGalleryRef{ref})
		// 画廊被删除或 token 失效时不能让这本书在 EH 源上永远搜不到东西，
		// 回落到标题搜索。限流/认证一类的错误照常返回，避免再打一次站点。
		if len(results) > 0 || (err != nil && !errors.Is(err, errEHInvalidGallery)) {
			return results, err
		}
	}

	artist := artistFromEHTags(existingTags)
	refs, err := p.searchGalleryRefsWithArtist(ctx, query, artist)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		// 与 LANraragi 的 lookup_gallery 一致：标题里的 gid 搜不到时按序落到
		// 整条标题搜索，而不是让这次查询到此为止。
		if stripped := stripEHTitleGID(query); stripped != "" {
			refs, err = p.searchGalleryRefsWithArtist(ctx, stripped, artist)
			if err != nil {
				return nil, err
			}
		}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	if len(refs) > ehMaxSearchResults {
		refs = refs[:ehMaxSearchResults]
	}
	return p.fetchGalleryMetadata(ctx, refs)
}

func (p *ehentaiProvider) searchGalleryRefs(ctx context.Context, query string) ([]ehGalleryRef, error) {
	return p.searchGalleryRefsWithArtist(ctx, query, "")
}

func (p *ehentaiProvider) searchGalleryRefsWithArtist(ctx context.Context, query, artist string) ([]ehGalleryRef, error) {
	searchURL, err := p.buildSearchURLWithArtist(query, artist)
	if err != nil {
		return nil, err
	}
	if err := p.searchGate.Wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL.String(), nil)
	if err != nil {
		return nil, errEHInvalidQuery
	}
	p.prepareRequest(req, "text/html,application/xhtml+xml")
	resp, err := p.client.Do(req)
	if err != nil {
		if errors.Is(err, errEHForbiddenRedirect) {
			return nil, errEHForbiddenRedirect
		}
		return nil, errEHRemote
	}
	defer resp.Body.Close()
	if err := classifyEHStatus(resp.StatusCode); err != nil {
		return nil, err
	}
	body, err := readEHBody(resp, ehMaxSearchBodyBytes)
	if err != nil {
		return nil, err
	}
	if err := classifyEHPage(body); err != nil {
		return nil, err
	}
	return parseEHGalleryRefs(body)
}

func (p *ehentaiProvider) buildSearchURL(query string) (*url.URL, error) {
	return p.buildSearchURLWithArtist(query, "")
}

func (p *ehentaiProvider) buildSearchURLWithArtist(query, artist string) (*url.URL, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errEHInvalidQuery
	}
	for _, char := range query {
		if char < 0x20 || char == 0x7f {
			return nil, errEHInvalidQuery
		}
	}
	searchTerm := ""
	if match := ehTitleGIDPattern.FindStringSubmatch(query); len(match) == 2 {
		searchTerm = "gid:" + match[1]
	} else {
		query = strings.ReplaceAll(query, `"`, " ")
		query = strings.Join(strings.Fields(query), " ")
		if query == "" {
			return nil, errEHInvalidQuery
		}
		searchTerm = `"` + query + `"`
		if artistTerm := exactEHArtistSearchTerm(artist); artistTerm != "" {
			searchTerm += " " + artistTerm
		}
		if language := normalizeEHSearchTagValue(p.cfg.ForcedLanguage, 32); language != "" {
			searchTerm += " language:" + language
		}
	}
	if len(query) > 200 || len(searchTerm) > 384 {
		return nil, errEHInvalidQuery
	}

	u := *p.endpoints.searchBase
	u.Path = "/"
	u.RawQuery = ""
	values := url.Values{
		"advsearch": {"1"},
		"f_sfu":     {"on"},
		"f_sft":     {"on"},
		"f_sfl":     {"on"},
		"f_search":  {searchTerm},
	}
	if p.cfg.SearchExpunged {
		values.Set("f_sh", "on")
	}
	u.RawQuery = values.Encode()
	return &u, nil
}

func galleryRefFromEHTags(tags []string) (ehGalleryRef, bool) {
	for _, tag := range tags {
		name, value, ok := strings.Cut(strings.TrimSpace(tag), ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "source") {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(strings.ToLower(value), "http://") {
			value = "https://" + value[len("http://"):]
		}
		if !strings.Contains(value, "://") {
			value = "https://" + value
		}
		if ref, ok := parseEHGalleryURL(value); ok {
			return ref, true
		}
	}
	return ehGalleryRef{}, false
}

// IsEHentaiGallerySourceTag reports whether a stored tag is an exact EH/EX
// gallery source. It is used when applying a new result so stale gallery
// identities are replaced instead of accumulating.
func IsEHentaiGallerySourceTag(tag string) bool {
	return store.IsEHentaiGallerySourceTag(tag)
}

// stripEHTitleGID 去掉标题里被当作画廊 ID 的方括号数字，用于 gid 搜索落空后的
// 标题回落。标题本身就只有一个 gid 时返回空串，没有可搜的标题。
func stripEHTitleGID(query string) string {
	match := ehTitleGIDPattern.FindString(query)
	if match == "" {
		return ""
	}
	return strings.Join(strings.Fields(strings.Replace(query, match, " ", 1)), " ")
}

func artistFromEHTags(tags []string) string {
	for _, tag := range tags {
		name, value, ok := strings.Cut(strings.TrimSpace(tag), ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "artist") {
			continue
		}
		if value = normalizeEHSearchTagValue(value, 100); value != "" {
			return value
		}
	}
	return ""
}

func normalizeEHSearchTagValue(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxBytes {
		return ""
	}
	for _, char := range value {
		if char < 0x20 || char > 0x7e || char == '"' || char == '\\' {
			return ""
		}
	}
	return value
}

func exactEHArtistSearchTerm(value string) string {
	value = normalizeEHSearchTagValue(value, 100)
	if value == "" || strings.ContainsAny(value, "*$%") {
		return ""
	}
	// EH treats whitespace-separated input as separate search terms. Keep the
	// namespace outside the quoted value and append the exact-tag operator so a
	// stored multi-word artist remains one constrained artist tag search.
	return `artist:"` + value + `"$`
}

func (p *ehentaiProvider) fetchGalleryMetadata(ctx context.Context, refs []ehGalleryRef) ([]ComicMetadata, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if len(refs) > ehMaxAPIEntries {
		refs = refs[:ehMaxAPIEntries]
	}
	gidList := make([][2]any, 0, len(refs))
	refByKey := make(map[string]ehGalleryRef, len(refs))
	for _, ref := range refs {
		if ref.GID <= 0 || !ehGalleryTokenPattern.MatchString(ref.Token) {
			continue
		}
		gidList = append(gidList, [2]any{ref.GID, ref.Token})
		refByKey[ehGalleryKey(ref.GID, ref.Token)] = ref
	}
	if len(gidList) == 0 {
		return nil, errEHInvalidGallery
	}
	if err := p.apiGate.Wait(ctx); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{
		"method":    "gdata",
		"gidlist":   gidList,
		"namespace": 1,
	})
	if err != nil {
		return nil, errEHInvalidResponse
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoints.apiURL.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, errEHInvalidResponse
	}
	req.Header.Set("Content-Type", "application/json")
	p.prepareRequest(req, "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		if errors.Is(err, errEHForbiddenRedirect) {
			return nil, errEHForbiddenRedirect
		}
		return nil, errEHRemote
	}
	defer resp.Body.Close()
	if err := classifyEHStatus(resp.StatusCode); err != nil {
		return nil, err
	}
	body, err := readEHBody(resp, ehMaxAPIResponseBytes)
	if err != nil {
		return nil, err
	}

	var apiResponse ehAPIResponse
	if err := json.Unmarshal(body, &apiResponse); err != nil {
		return nil, errEHInvalidResponse
	}
	if apiResponse.Error != "" {
		return nil, errEHRemote
	}
	results := make([]ComicMetadata, 0, len(apiResponse.GMetadata))
	seenResults := make(map[string]struct{}, len(refs))
	hadGalleryError := false
	for _, gallery := range apiResponse.GMetadata {
		if len(results) >= len(refByKey) {
			break
		}
		if gallery.Error != "" {
			hadGalleryError = true
			continue
		}
		key := ehGalleryKey(gallery.GID, gallery.Token)
		ref, ok := refByKey[key]
		if !ok {
			continue
		}
		if _, duplicate := seenResults[key]; duplicate {
			continue
		}
		meta, ok := p.mapGallery(gallery, ref)
		if ok {
			seenResults[key] = struct{}{}
			results = append(results, meta)
		}
	}
	if len(results) == 0 && hadGalleryError {
		return nil, errEHInvalidGallery
	}
	return results, nil
}

func (p *ehentaiProvider) prepareRequest(req *http.Request, accept string) {
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", ehUserAgent)
	if _, ok := p.cookieOrigins[originOf(req.URL)]; !ok {
		return
	}
	for _, cookie := range []struct {
		name  string
		value string
	}{
		{name: "ipb_member_id", value: p.cfg.IPBMemberID},
		{name: "ipb_pass_hash", value: p.cfg.IPBPassHash},
		{name: "star", value: p.cfg.Star},
		{name: "igneous", value: p.cfg.Igneous},
	} {
		if cookie.value != "" {
			req.AddCookie(&http.Cookie{Name: cookie.name, Value: cookie.value, Path: "/", Secure: req.URL.Scheme == "https", HttpOnly: true})
		}
	}
	req.AddCookie(&http.Cookie{Name: "nw", Value: "1", Path: "/", Secure: req.URL.Scheme == "https", HttpOnly: true})
}

func parseEHGalleryURL(raw string) (ehGalleryRef, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return ehGalleryRef{}, false
	}
	host := strings.ToLower(u.Hostname())
	site := ""
	switch host {
	case "e-hentai.org":
		site = config.EHentaiSitePublic
	case "exhentai.org":
		site = config.EHentaiSiteRestricted
	default:
		return ehGalleryRef{}, false
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) != 3 || parts[0] != "g" || !ehGalleryTokenPattern.MatchString(parts[2]) {
		return ehGalleryRef{}, false
	}
	gid, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || gid <= 0 {
		return ehGalleryRef{}, false
	}
	return ehGalleryRef{GID: gid, Token: strings.ToLower(parts[2]), Site: site}, true
}

func parseEHGalleryRefs(body []byte) ([]ehGalleryRef, error) {
	doc, err := xhtml.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, errEHInvalidResponse
	}
	seen := make(map[string]struct{})
	refs := make([]ehGalleryRef, 0, ehMaxSearchResults)
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if len(refs) >= ehMaxAPIEntries {
			return
		}
		if node.Type == xhtml.ElementNode && node.Data == "a" {
			for _, attr := range node.Attr {
				if attr.Key != "href" {
					continue
				}
				ref, ok := parseEHGalleryURL(stdhtml.UnescapeString(attr.Val))
				if !ok {
					continue
				}
				key := ehGalleryKey(ref.GID, ref.Token)
				if _, exists := seen[key]; !exists {
					seen[key] = struct{}{}
					refs = append(refs, ref)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
			if len(refs) >= ehMaxAPIEntries {
				return
			}
		}
	}
	walk(doc)
	return refs, nil
}

type ehFlexibleString string

func (s *ehFlexibleString) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*s = ""
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err == nil {
		*s = ehFlexibleString(value)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	*s = ehFlexibleString(number.String())
	return nil
}

type ehAPIResponse struct {
	GMetadata []ehGallery `json:"gmetadata"`
	Error     string      `json:"error"`
}

type ehGallery struct {
	GID      int64            `json:"gid"`
	Token    string           `json:"token"`
	Title    string           `json:"title"`
	TitleJPN string           `json:"title_jpn"`
	Category string           `json:"category"`
	Thumb    string           `json:"thumb"`
	Uploader string           `json:"uploader"`
	Posted   ehFlexibleString `json:"posted"`
	Rating   ehFlexibleString `json:"rating"`
	Expunged bool             `json:"expunged"`
	Tags     []string         `json:"tags"`
	Error    string           `json:"error"`
}

func (p *ehentaiProvider) mapGallery(gallery ehGallery, ref ehGalleryRef) (ComicMetadata, bool) {
	if gallery.GID <= 0 || gallery.Title == "" && gallery.TitleJPN == "" {
		return ComicMetadata{}, false
	}
	title := gallery.Title
	if p.cfg.PreferOriginalTitle && gallery.TitleJPN != "" {
		title = gallery.TitleJPN
	}
	if title == "" {
		title = gallery.TitleJPN
	}
	title = strings.TrimSpace(stdhtml.UnescapeString(title))
	if title == "" || len(title) > ehMaxTitleBytes {
		return ComicMetadata{}, false
	}

	tags := make([]string, 0, len(gallery.Tags)+2)
	seenTags := make(map[string]struct{})
	var authors, groups []string
	for _, rawTag := range gallery.Tags {
		if len(tags) >= ehMaxGalleryTags-2 {
			break
		}
		tag := sanitizeEHTag(rawTag)
		if tag == "" {
			continue
		}
		appendUniqueEH(&tags, seenTags, tag)
		lower := strings.ToLower(tag)
		switch {
		case strings.HasPrefix(lower, "artist:"):
			appendUniqueString(&authors, strings.TrimSpace(tag[len("artist:"):]))
		case strings.HasPrefix(lower, "group:"):
			appendUniqueString(&groups, strings.TrimSpace(tag[len("group:"):]))
		}
	}
	if category := sanitizeEHTag("category:" + strings.ToLower(strings.TrimSpace(gallery.Category))); category != "category:" {
		appendUniqueEH(&tags, seenTags, category)
	}
	siteHost := "e-hentai.org"
	source := config.EHentaiSitePublic
	if ref.Site == config.EHentaiSiteRestricted {
		siteHost = "exhentai.org"
		source = config.EHentaiSiteRestricted
	}
	appendUniqueEH(&tags, seenTags, fmt.Sprintf("source:https://%s/g/%d/%s", siteHost, ref.GID, ref.Token))

	metadata := ComicMetadata{
		Title:     title,
		Author:    strings.Join(authors, ", "),
		Publisher: strings.Join(groups, ", "),
		Language:  languageFromEHTags(tags),
		Genre:     strings.Join(tags, ", "),
		CoverURL:  safeEHCoverURL(gallery.Thumb),
		Source:    source,
	}
	if posted, err := strconv.ParseInt(string(gallery.Posted), 10, 64); err == nil && posted > 0 {
		year := time.Unix(posted, 0).UTC().Year()
		if year >= 1900 && year <= time.Now().UTC().Year()+1 {
			metadata.Year = &year
		}
	}
	if rating, err := strconv.ParseFloat(string(gallery.Rating), 64); err == nil && rating > 0 && rating <= 5 {
		maxRating := float64(5)
		metadata.ExternalRating = &rating
		metadata.ExternalRatingMax = &maxRating
		metadata.ExternalRatingSource = source
	}
	return metadata, true
}

func readEHBody(resp *http.Response, limit int64) ([]byte, error) {
	if resp.ContentLength > limit {
		return nil, errEHResponseTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, errEHRemote
	}
	if int64(len(body)) > limit {
		return nil, errEHResponseTooLarge
	}
	return body, nil
}

func classifyEHStatus(status int) error {
	switch {
	case status == http.StatusTooManyRequests:
		return errEHRateLimited
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return errEHAuthentication
	case status < 200 || status >= 300:
		return errEHRemote
	default:
		return nil
	}
}

func classifyEHPage(body []byte) error {
	lower := strings.ToLower(string(body))
	switch {
	case strings.Contains(lower, "your ip address has been"):
		return errEHTemporarilyBanned
	case strings.Contains(lower, "this page requires you to log on"), strings.Contains(lower, "please log on to access"):
		return errEHAuthentication
	case strings.Contains(lower, "sad panda"):
		return errEHAuthentication
	default:
		return nil
	}
}

func safeEHError(err error) string {
	switch {
	case err == nil:
		return "unknown error"
	case errors.Is(err, errEHInvalidQuery):
		return "invalid query"
	case errors.Is(err, errEHAuthentication):
		return "authentication required or rejected"
	case errors.Is(err, errEHRateLimited):
		return "rate limited"
	case errors.Is(err, errEHTemporarilyBanned):
		return "temporarily banned for excessive requests"
	case errors.Is(err, errEHForbiddenRedirect):
		return "unsafe redirect rejected"
	case errors.Is(err, errEHResponseTooLarge):
		return "response too large"
	case errors.Is(err, errEHInvalidResponse), errors.Is(err, errEHInvalidGallery):
		return "invalid response"
	case errors.Is(err, errEHRemote):
		return "remote request failed"
	default:
		// Configuration errors contain variable names but never values.
		return err.Error()
	}
}

func originOf(u *url.URL) string {
	if u == nil {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

func ehGalleryKey(gid int64, token string) string {
	return strconv.FormatInt(gid, 10) + ":" + strings.ToLower(token)
}

func sanitizeEHTag(value string) string {
	value = strings.TrimSpace(stdhtml.UnescapeString(value))
	if value == "" || len(value) > ehMaxTagBytes {
		return ""
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f || char == ',' {
			return ""
		}
	}
	return value
}

func appendUniqueEH(values *[]string, seen map[string]struct{}, value string) {
	key := strings.ToLower(value)
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	*values = append(*values, value)
}

func appendUniqueString(values *[]string, value string) {
	if value == "" {
		return
	}
	for _, existing := range *values {
		if strings.EqualFold(existing, value) {
			return
		}
	}
	*values = append(*values, value)
}

func languageFromEHTags(tags []string) string {
	for _, tag := range tags {
		if !strings.HasPrefix(strings.ToLower(tag), "language:") {
			continue
		}
		value := strings.ToLower(strings.TrimSpace(tag[len("language:"):]))
		switch value {
		case "translated", "rewrite":
			continue
		case "english":
			return "en"
		case "japanese":
			return "ja"
		case "chinese":
			return "zh"
		case "korean":
			return "ko"
		case "french":
			return "fr"
		case "german":
			return "de"
		case "spanish":
			return "es"
		case "italian":
			return "it"
		case "portuguese":
			return "pt"
		case "russian":
			return "ru"
		}
	}
	return ""
}

func safeEHCoverURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) > ehMaxCoverURLBytes {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || u.Port() != "" && u.Port() != "443" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host != "ehgt.org" && !strings.HasSuffix(host, ".ehgt.org") && host != "e-hentai.org" && host != "exhentai.org" {
		return ""
	}
	return u.String()
}
