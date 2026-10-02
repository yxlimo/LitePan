package rules

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

func FindTMDBIDInName(name string) string {
	if name == "" {
		return ""
	}
	for _, re := range tmdbTagPatterns {
		if m := re.FindStringSubmatch(name); len(m) >= 2 {
			return m[1]
		}
	}
	return ""
}

func ExtractTMDBDisplayFields(result map[string]any, mediaType string) (id, title, original string, year *int) {
	_ = mediaType
	if len(result) == 0 {
		return "", "", "", nil
	}
	releaseDate := strVal(result["release_date"])
	if releaseDate == "" {
		releaseDate = strVal(result["first_air_date"])
	}
	if len(releaseDate) >= 4 {
		if y, err := parseInt(releaseDate[:4]); err == nil {
			year = intPtr(y)
		}
	}
	id = strings.TrimSpace(toString(result["id"]))
	title = strings.TrimSpace(strVal(result["title"]))
	if title == "" {
		title = strings.TrimSpace(strVal(result["name"]))
	}
	original = strings.TrimSpace(strVal(result["original_title"]))
	if original == "" {
		original = strings.TrimSpace(strVal(result["original_name"]))
	}
	return id, title, original, year
}

func IsTMDBTitleCompatible(query, resultTitle, resultOriginal string) bool {
	q := strings.TrimSpace(query)
	if q == "" {
		return false
	}
	ql := strings.ToLower(q)
	candidates := []string{strings.TrimSpace(resultTitle), strings.TrimSpace(resultOriginal)}
	for _, t := range candidates {
		if t != "" && ql == strings.ToLower(t) {
			return true
		}
	}

	enWords := enWordRe.FindAllString(ql, -1)
	if len(enWords) > 0 {
		blob := strings.ToLower(strings.Join(candidates, " "))
		for _, w := range enWords {
			if strings.Contains(blob, w) {
				return true
			}
		}
	}

	cnQ := hanOnlyRe.ReplaceAllString(q, "")
	if len([]rune(cnQ)) == 1 {
		// 单个汉字信息量过低，“一”“这”等字会误命中大量无关片名。
		return false
	}
	if len([]rune(cnQ)) >= 2 {
		for _, t := range candidates {
			cnT := hanOnlyRe.ReplaceAllString(t, "")
			if cnT == "" {
				continue
			}
			if cnQ == cnT {
				return true
			}
			if strings.HasPrefix(cnT, cnQ) && len(cnT) > len(cnQ) {
				extra := cnT[len([]rune(cnQ)):]
				if hanOnlyRe.MatchString(extra) {
					return false
				}
			}
			if strings.HasSuffix(cnT, cnQ) && len(cnT) > len(cnQ) {
				prefix := cnT[:len(cnT)-len([]rune(cnQ))]
				if prefix != "" && docuPrefixRe.MatchString(prefix) {
					return false
				}
			}
			if strings.Contains(cnT, cnQ) {
				return true
			}
		}
		if len([]rune(cnQ)) <= 4 {
			return false
		}
		runes := []rune(cnQ)
		bigrams := make([]string, 0, len(runes)-1)
		for i := 0; i < len(runes)-1; i++ {
			bigrams = append(bigrams, string(runes[i:i+2]))
		}
		for _, t := range candidates {
			cnT := hanOnlyRe.ReplaceAllString(t, "")
			if cnT == "" || cnT == cnQ {
				continue
			}
			if strings.HasPrefix(cnT, cnQ) && len(cnT) > len(cnQ) {
				continue
			}
			hits := 0
			for _, bg := range bigrams {
				if strings.Contains(cnT, bg) {
					hits++
				}
			}
			if hits >= max(2, len(bigrams)/2) {
				return true
			}
		}
		return false
	}

	if len(ql) <= 1 {
		return true
	}
	for _, t := range candidates {
		tl := strings.ToLower(t)
		if tl != "" && (strings.Contains(ql, tl) || strings.Contains(tl, ql)) {
			return true
		}
	}
	return false
}

// tmdbYearMismatchMargin 年份不命中时，候选标题评分需要拉开的最小分差。
// 分差不足说明「有多个看起来都像的候选」，属于歧义，应交人工选择而不是硬猜。
const tmdbYearMismatchMargin = 0.2

func PickTMDBMatchForYear(results []map[string]any, expectedYear *int, mediaType, queryTitle string) map[string]any {
	if len(results) == 0 {
		return nil
	}
	qt := strings.TrimSpace(queryTitle)
	compatible := func(item map[string]any) bool {
		if qt == "" {
			return true
		}
		_, t, o, _ := ExtractTMDBDisplayFields(item, mediaType)
		return IsTMDBTitleCompatible(qt, t, o)
	}
	if expectedYear != nil {
		for _, item := range results {
			_, _, _, resultYear := ExtractTMDBDisplayFields(item, mediaType)
			if resultYear != nil && *resultYear == *expectedYear && compatible(item) {
				return item
			}
		}
		// 年份不是红线：文件名里的年份经常是错的（上错年、不同版本、不同来源标注），
		// 年份不同就彻底放弃匹配会形成死循环——匹配失败 -> 名字被「修正」成错年份
		// -> 之后每次整理重复失败。这里降级为加权项。
		return pickTMDBYearMismatchMatch(results, *expectedYear, mediaType, qt, compatible)
	}
	if qt != "" {
		for _, item := range results {
			if compatible(item) {
				return item
			}
		}
		return nil
	}
	return results[0]
}

// pickTMDBYearMismatchMatch 在年份不命中的候选里挑一个「标题唯一强匹配」的：
// 1) 先走最严格的相邻年份 + 片名强相等 + 唯一（PickUniqueTMDBAdjacentYearMatch）
// 2) 再退到标题兼容候选里按 ScoreTitleForTMDB 打分，只有 top1 与 top2 分差足够大才接受
// 3) 都不满足则判歧义，返回 nil
func pickTMDBYearMismatchMatch(
	results []map[string]any,
	expectedYear int,
	mediaType, queryTitle string,
	compatible func(map[string]any) bool,
) map[string]any {
	if queryTitle == "" {
		return nil
	}
	if m := PickUniqueTMDBAdjacentYearMatch(results, &expectedYear, mediaType, queryTitle); m != nil {
		return m
	}
	candidates := collectTMDBCompatibleCandidates(results, expectedYear, mediaType, queryTitle, compatible)
	if len(candidates) == 0 {
		return nil
	}
	sortTMDBScoredCandidates(candidates)
	// 分差不足说明「有多个看起来都像的候选」，属于歧义，交给人工选择而不是硬猜。
	if len(candidates) > 1 && candidates[0].score-candidates[1].score < tmdbYearMismatchMargin {
		return nil
	}
	return candidates[0].item
}

// tmdbScoredCandidate 是「标题兼容」的候选及其标题评分。
type tmdbScoredCandidate struct {
	year    *int
	yearGap int
	score   float64
	item    map[string]any
}

func collectTMDBCompatibleCandidates(
	results []map[string]any,
	expectedYear int,
	mediaType, queryTitle string,
	compatible func(map[string]any) bool,
) []tmdbScoredCandidate {
	candidates := make([]tmdbScoredCandidate, 0, len(results))
	seen := map[string]struct{}{}
	for _, item := range results {
		if !compatible(item) {
			continue
		}
		id, title, original, year := ExtractTMDBDisplayFields(item, mediaType)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		best := ScoreTitleForTMDB(title)
		if s := ScoreTitleForTMDB(original); s > best {
			best = s
		}
		if best <= 0 {
			continue
		}
		gap := int(^uint(0) >> 1) // 无年份的候选视为最不接近
		if year != nil {
			gap = absInt(*year - expectedYear)
		}
		candidates = append(candidates, tmdbScoredCandidate{year: year, yearGap: gap, score: best, item: item})
	}
	return candidates
}

// sortTMDBScoredCandidates 标题分高者优先；同分时年份更接近者优先。
func sortTMDBScoredCandidates(candidates []tmdbScoredCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].yearGap < candidates[j].yearGap
	})
}

// CollectTMDBYearMismatchCandidates 收集「标题兼容但年份与声明年份不符」的候选。
// 用于计划预览区分「TMDB 真的没有这部片子」和「有很像的候选但年份对不上」，
// 让 needs_match 的文案带上可一键采纳的候选。
func CollectTMDBYearMismatchCandidates(
	results []map[string]any,
	expectedYear *int,
	mediaType, queryTitle string,
) []map[string]any {
	if expectedYear == nil || len(results) == 0 || strings.TrimSpace(queryTitle) == "" {
		return nil
	}
	compatible := func(item map[string]any) bool {
		_, t, o, _ := ExtractTMDBDisplayFields(item, mediaType)
		return IsTMDBTitleCompatible(strings.TrimSpace(queryTitle), t, o)
	}
	candidates := collectTMDBCompatibleCandidates(results, *expectedYear, mediaType, queryTitle, compatible)
	out := make([]map[string]any, 0, len(candidates))
	for _, c := range candidates {
		if c.year != nil && *c.year == *expectedYear {
			continue
		}
		out = append(out, c.item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// PickTMDBSearchMatchRelaxed 年份完全不参与打分：片名强相等且候选唯一才接受。
//
// 用于「去掉年份重新搜索之后」的最后一次尝试——此前那个分支仍复用同一个年份门槛，
// 等于白搜一遍：搜回来的结果照样被年份挡掉。
func PickTMDBSearchMatchRelaxed(results []map[string]any, mediaType, queryTitle string) map[string]any {
	queryKey := strongTMDBTitleKey(queryTitle)
	if len(results) == 0 || queryKey == "" {
		return nil
	}
	var picked map[string]any
	pickedID := ""
	for _, item := range results {
		id, title, original, _ := ExtractTMDBDisplayFields(item, mediaType)
		if id == "" {
			continue
		}
		if strongTMDBTitleKey(title) != queryKey && strongTMDBTitleKey(original) != queryKey {
			continue
		}
		if picked != nil && pickedID != id {
			// 多个同片名候选（例如重映版/不同年份版本），判歧义交给人工选择。
			return nil
		}
		picked, pickedID = item, id
	}
	return picked
}

// PickTMDBSearchMatchForYear 先做严格标题匹配，只有年份明确且一致时才放宽译名/别名候选的标题校验。
// PickTMDBSearchMatchExactYear 只接受年份完全相等的候选，不做任何放宽。
//
// 用于「带年份的第一次搜索」：TMDB 在这次查询里已经按年份过滤过，候选集不完整，
// 此时判断「±1 年候选是否唯一」会得到偏乐观的结论。需要严格年份门禁的调用方
// （如 strmscrape 需要据此标记 doubt）应该用这个函数，而不是 PickTMDBSearchMatchForYear。
func PickTMDBSearchMatchExactYear(results []map[string]any, expectedYear *int, mediaType, queryTitle string) map[string]any {
	if len(results) == 0 {
		return nil
	}
	qt := strings.TrimSpace(queryTitle)
	if expectedYear != nil {
		for _, item := range results {
			_, _, _, resultYear := ExtractTMDBDisplayFields(item, mediaType)
			if resultYear != nil && *resultYear == *expectedYear &&
				(qt == "" || titleCompatibleWith(qt, item, mediaType)) {
				return item
			}
		}
		return nil
	}
	if qt != "" {
		for _, item := range results {
			if titleCompatibleWith(qt, item, mediaType) {
				return item
			}
		}
		return nil
	}
	return results[0]
}

func titleCompatibleWith(queryTitle string, item map[string]any, mediaType string) bool {
	_, t, o, _ := ExtractTMDBDisplayFields(item, mediaType)
	return IsTMDBTitleCompatible(queryTitle, t, o)
}

func PickTMDBSearchMatchForYear(results []map[string]any, expectedYear *int, mediaType, queryTitle string) map[string]any {
	if selected := PickTMDBMatchForYear(results, expectedYear, mediaType, queryTitle); selected != nil {
		return selected
	}
	if expectedYear == nil {
		return nil
	}
	for _, item := range results {
		_, _, _, resultYear := ExtractTMDBDisplayFields(item, mediaType)
		if resultYear != nil && *resultYear == *expectedYear {
			return item
		}
	}
	return nil
}

// PickUniqueTMDBAdjacentYearMatch 仅接受唯一、片名强相等且相差一年的候选，不使用别名放宽。
func PickUniqueTMDBAdjacentYearMatch(results []map[string]any, expectedYear *int, mediaType, queryTitle string) map[string]any {
	if expectedYear == nil || strings.TrimSpace(queryTitle) == "" {
		return nil
	}
	queryKey := strongTMDBTitleKey(queryTitle)
	if queryKey == "" {
		return nil
	}
	matches := make(map[string]map[string]any, 2)
	for _, item := range results {
		id, title, original, resultYear := ExtractTMDBDisplayFields(item, mediaType)
		if id == "" || resultYear == nil || absInt(*resultYear-*expectedYear) != 1 {
			continue
		}
		if strongTMDBTitleKey(title) != queryKey && strongTMDBTitleKey(original) != queryKey {
			continue
		}
		matches[id] = item
	}
	if len(matches) != 1 {
		return nil
	}
	for _, item := range matches {
		return item
	}
	return nil
}

func strongTMDBTitleKey(title string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, strings.TrimSpace(title))
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func BuildTMDBMatchAttempts(groupTitle string, groupYear *int, dirName string, fileParses []ParsedMedia) []TMDBMatchAttempt {
	dirParsed := ParsedMedia{}
	if dirName != "" {
		dirParsed = NormalizeParsedMedia(ParseDirName(dirName))
	}
	dirTitle := strings.TrimSpace(dirParsed.Title)
	dirYear := dirParsed.Year

	fileTitles := make([]string, 0, len(fileParses))
	fileYears := make([]int, 0, len(fileParses))
	for _, parsed := range fileParses {
		fp := NormalizeParsedMedia(parsed)
		if ft := strings.TrimSpace(fp.Title); ft != "" {
			fileTitles = append(fileTitles, ft)
		}
		if fp.Year != nil {
			fileYears = append(fileYears, *fp.Year)
		}
	}

	fileTitle := ""
	if len(fileTitles) > 0 {
		fileTitle = PickBestTitleForTMDB(fileTitles...)
	}
	var fileYear *int
	if len(fileYears) > 0 {
		fileYear = intPtr(fileYears[0])
	}
	mergedTitle := PickBestTitleForTMDB(dirTitle, fileTitle, groupTitle)
	mergedYear := dirYear
	if mergedYear == nil {
		mergedYear = fileYear
	}
	if mergedYear == nil {
		mergedYear = groupYear
	}

	attempts := make([]TMDBMatchAttempt, 0, 8)
	add := tmdbAttemptAdder(&attempts)

	add(mergedTitle, mergedYear, "合并")
	if fileTitle != "" {
		y := fileYear
		if y == nil {
			y = mergedYear
		}
		add(fileTitle, y, "文件")
	}
	if dirTitle != "" && ScoreTitleForTMDB(dirTitle) >= 0.45 {
		y := dirYear
		if y == nil {
			y = mergedYear
		}
		add(dirTitle, y, "目录")
	}
	if groupTitle != "" {
		y := groupYear
		if y == nil {
			y = mergedYear
		}
		add(groupTitle, y, "默认")
	}

	snapshot := append([]TMDBMatchAttempt(nil), attempts...)
	for _, item := range snapshot {
		cnCore := ExtractChineseTitleCore(item.Title)
		if len([]rune(cnCore)) >= 2 && cnCore != item.Title {
			if hasNumericSuffixAfterChineseCore(item.Title, cnCore) {
				continue
			}
			add(cnCore, item.Year, item.Source+"-中文")
		}
	}
	return attempts
}

func hasNumericSuffixAfterChineseCore(title, core string) bool {
	suffix := strings.TrimLeft(strings.TrimPrefix(title, core), " ._-")
	return suffix != "" && suffix[0] >= '0' && suffix[0] <= '9'
}

func ScoreTitleForTMDB(title string) float64 {
	raw := strings.TrimSpace(title)
	if raw == "" {
		return 0
	}
	score := 1.0
	if titleNoiseRe.MatchString(raw) {
		score -= 0.85
	}
	if resolutionInTitleRe.MatchString(raw) {
		score -= 0.35
	}
	if sceneKeywordsRe.MatchString(raw) {
		score -= 0.25
	}
	tokenCount := len(tokenSplitRe.Split(raw, -1))
	if tokenCount > 10 {
		score -= 0.25
	} else if tokenCount > 7 {
		score -= 0.15
	}
	if len(raw) > 48 {
		score -= 0.1
	}
	if pureChineseTitleRe.MatchString(raw) {
		score += 0.2
	}
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

func ExtractChineseTitleCore(title string) string {
	raw := strings.TrimSpace(title)
	m := chineseTitleCoreRe.FindStringSubmatch(raw)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

func PickBestTitleForTMDB(candidates ...string) string {
	cleaned := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if t := strings.TrimSpace(c); t != "" {
			cleaned = append(cleaned, t)
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	if len(cleaned) == 1 {
		return cleaned[0]
	}
	type scored struct {
		score float64
		title string
	}
	scoredList := make([]scored, len(cleaned))
	for i, title := range cleaned {
		scoredList[i] = scored{score: ScoreTitleForTMDB(title), title: title}
	}
	best := scoredList[0]
	for _, item := range scoredList[1:] {
		if item.score > best.score {
			best = item
		}
	}
	if best.score >= 0.45 {
		return best.title
	}
	for _, item := range scoredList {
		if item.score >= 0.35 {
			return item.title
		}
	}
	return cleaned[0]
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

var (
	hanOnlyRe    = regexp.MustCompile(`[^\p{Han}]`)
	docuPrefixRe = regexp.MustCompile(`(舞台剧|纪录片|歌剧|幕后|制作纪录)`)
	tokenSplitRe = regexp.MustCompile(`[\s._\-]+`)
)

// tmdbAttemptAdder 保留首次候选及其来源，按标题和年份去重。
func tmdbAttemptAdder(attempts *[]TMDBMatchAttempt) func(string, *int, string) {
	seen := map[string]struct{}{}
	return func(title string, year *int, source string) {
		t := strings.TrimSpace(title)
		if t == "" {
			return
		}
		yKey := "nil"
		if year != nil {
			yKey = strconv.Itoa(*year)
		}
		key := strings.ToLower(t) + "|" + yKey
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		*attempts = append(*attempts, TMDBMatchAttempt{Title: t, Year: year, Source: source})
	}
}
