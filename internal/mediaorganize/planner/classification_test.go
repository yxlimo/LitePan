package planner_test

import (
	"context"
	"fmt"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/mediaorganize/classification"
	"litepan/internal/mediaorganize/moplan"
	"litepan/internal/mediaorganize/planner"
)

type classificationStub struct {
	available bool
	decision  classification.Decision
}

func (s classificationStub) Available() bool { return s.available }

func (s classificationStub) Classify(context.Context, classification.Request) (classification.Decision, error) {
	return s.decision, nil
}

func TestMoveClassificationBuildsRelativeCategoryPath(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root":  {{ID: "movie", Name: "阿凡达 (2009)", IsDir: true}},
		"movie": {{ID: "f1", Name: "阿凡达.2009.mkv"}},
	}}
	tmdb := &mockTMDB{searchFn: func(string, *int) []map[string]any {
		return []map[string]any{{"id": 19995, "title": "阿凡达", "release_date": "2009-12-18"}}
	}}
	p := planner.New(context.Background(), fs, 1, planner.TaskConfig{
		TargetDirectoryID: "root",
		TargetRootID:      "target",
		ActionType:        "move",
		UseTMDB:           true,
		Recursive:         true,
	}, planner.Settings{"mo_tmdb_api_key": "test-key"}, "task", tmdb, nil, nil, nil)
	p.SetClassificationEnhancer(classificationStub{available: true, decision: classification.Decision{
		Applied: true, Matched: true, Template: "genre", Category: "科幻", RelativeSegments: []string{"电影", "科幻"},
	}})
	plan, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	movieDir := findEnsureDir(plan.Actions, "电影")
	genreDir := findEnsureDir(plan.Actions, "科幻")
	workDir := findWorkDir(plan.Actions)
	if movieDir == nil || movieDir.TargetParentID != "target" {
		t.Fatalf("电影分类目录异常: %+v", movieDir)
	}
	if genreDir == nil || genreDir.TargetParentID != "ref:"+movieDir.ID {
		t.Fatalf("科幻分类目录异常: %+v", genreDir)
	}
	if workDir == nil || workDir.TargetParentID != "ref:"+genreDir.ID {
		t.Fatalf("作品目录未挂到分类目录: %+v", workDir)
	}
	if workDir.Metadata["classification_category"] != "科幻" {
		t.Fatalf("计划未保存分类快照: %+v", workDir.Metadata)
	}
}

func TestMoveClassificationBuildsMissingFallbackDirectory(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root":  {{ID: "movie", Name: "未知地区影片 (2026)", IsDir: true}},
		"movie": {{ID: "f1", Name: "未知地区影片.2026.mkv"}},
	}}
	tmdb := &mockTMDB{searchFn: func(string, *int) []map[string]any {
		return []map[string]any{{"id": 10086, "title": "未知地区影片", "release_date": "2026-01-01"}}
	}}
	p := planner.New(context.Background(), fs, 1, planner.TaskConfig{
		TargetDirectoryID: "root",
		TargetRootID:      "target",
		ActionType:        "move",
		UseTMDB:           true,
		Recursive:         true,
	}, planner.Settings{"mo_tmdb_api_key": "test-key"}, "task", tmdb, nil, nil, nil)
	p.SetClassificationEnhancer(classificationStub{available: true, decision: classification.Decision{
		Applied: true, Matched: true, Template: "region", Category: "其他", RelativeSegments: []string{"电影", "其他"},
	}})

	plan, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	movieDir := findEnsureDir(plan.Actions, "电影")
	fallbackDir := findEnsureDir(plan.Actions, "其他")
	workDir := findWorkDir(plan.Actions)
	if movieDir == nil || movieDir.TargetParentID != "target" {
		t.Fatalf("未生成一级分类目录动作: %+v", movieDir)
	}
	if fallbackDir == nil || fallbackDir.TargetParentID != "ref:"+movieDir.ID {
		t.Fatalf("未生成指定兜底目录动作: %+v", fallbackDir)
	}
	if workDir == nil || workDir.TargetParentID != "ref:"+fallbackDir.ID {
		t.Fatalf("作品目录未放入指定兜底目录: %+v", workDir)
	}
}

// 分类参与但没命中时，不能把作品目录直接甩到 move 目标根（那往往就是网盘根）。
// 应按媒体类型兜底进 电影/，并在诊断里登记，让用户知道这条是靠兜底归的类。
func TestMoveClassificationUnmatchedFallsBackToMediaKind(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root":     {{ID: "category", Name: "原目录分类", IsDir: true}},
		"category": {{ID: "movie", Name: "未知影片 (2020)", IsDir: true}},
		"movie":    {{ID: "f1", Name: "未知影片.2020.mkv"}},
	}}
	p := planner.New(context.Background(), fs, 1, planner.TaskConfig{
		TargetDirectoryID: "root",
		TargetRootID:      "target",
		ActionType:        "move",
		UseTMDB:           false,
		Recursive:         true,
	}, nil, "task", nil, nil, nil, nil)
	p.SetClassificationEnhancer(classificationStub{available: true, decision: classification.Decision{
		Applied: true, Template: "region", DegradedReason: "no_rule_matched",
	}})
	plan, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	movieDir := findEnsureDir(plan.Actions, "电影")
	if movieDir == nil || movieDir.TargetParentID != "target" {
		t.Fatalf("未命中时未生成媒体类型兜底目录: %+v", plan.Actions)
	}
	workDir := findWorkDir(plan.Actions)
	if workDir == nil {
		t.Fatalf("未生成作品目录: %+v", plan.Actions)
	}
	if workDir.TargetParentID != "ref:"+movieDir.ID {
		t.Fatalf("作品目录应落在媒体类型兜底目录下，实际父目录 %q", workDir.TargetParentID)
	}
	if findEnsureDir(plan.Actions, "原目录分类") != nil {
		t.Fatalf("分类器明确未命中时不应镜像源目录: %+v", plan.Actions)
	}
	entries, _ := plan.Diagnostics["needs_classification"].([]map[string]any)
	if len(entries) != 1 {
		t.Fatalf("应登记 1 条 needs_classification 诊断，实际 %+v", plan.Diagnostics["needs_classification"])
	}
	if entries[0]["degraded_reason"] != "no_rule_matched" {
		t.Fatalf("诊断应带降级原因: %+v", entries[0])
	}
}

// 分类不可用（增强器为 nil）时按「分类器没参与」处理：保留源目录已有的分类层级，
// 而不是直接落到 move 目标根。这里用剧集（不触发独立电影提升路径）验证。
func TestMoveClassificationUnavailableKeepsSourceCategoryHierarchy(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root":     {{ID: "category", Name: "原目录分类", IsDir: true}},
		"category": {{ID: "show", Name: "脱口秀和Ta的朋友们 (2024)", IsDir: true}},
		"show": {
			{ID: "s1", Name: "Season 01", IsDir: true},
			{ID: "f1", Name: "脱口秀和Ta的朋友们.S01E01.mkv"},
		},
	}}
	p := planner.New(context.Background(), fs, 1, planner.TaskConfig{
		TargetDirectoryID: "root",
		TargetRootID:      "target",
		ActionType:        "move",
		UseTMDB:           false,
		Recursive:         true,
	}, nil, "task", nil, nil, nil, nil)
	// 不注入分类增强器
	plan, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	categoryDir := findEnsureDir(plan.Actions, "原目录分类")
	if categoryDir == nil {
		t.Fatalf("分类不可用时应保留源目录分类层级: %+v", plan.Actions)
	}
	if categoryDir.TargetParentID != "target" {
		t.Fatalf("源目录分类层级应挂在 move 目标根下，实际 %q", categoryDir.TargetParentID)
	}
	if plan.Diagnostics["classification_degraded_count"] != 1 {
		t.Fatalf("分类不可用应记录降级诊断: %+v", plan.Diagnostics)
	}
}

// batchMixedClassificationStub 模拟 region 模板：只有拿到 TMDB 详情才可能命中二级，
// 没匹配到 TMDB 的组会返回 Matched=false（degraded）。
type batchMixedClassificationStub struct{}

func (batchMixedClassificationStub) Available() bool { return true }

func (batchMixedClassificationStub) Classify(_ context.Context, req classification.Request) (classification.Decision, error) {
	if req.TMDBID == "" {
		return classification.Decision{
			Applied: true, Template: "region", MediaKind: req.MediaType,
			DegradedReason: "tmdb_detail_unavailable",
		}, nil
	}
	return classification.Decision{
		Applied: true, Matched: true, Template: "region", MediaKind: req.MediaType,
		Category: "国产", RelativeSegments: []string{"电影", "国产"},
	}, nil
}

// 批量处理时分类成败不同，不应该表现为「有的进分类目录、有的落到任务根目录」。
// 三部影片里两部匹配到 TMDB、第三部没匹配上，三者的作品目录都必须在 电影/ 之下。
func TestMoveClassificationBatchMixedMatchNeverLandsOnMoveRoot(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {
			{ID: "d1", Name: "阿凡达 (2009)", IsDir: true},
			{ID: "d2", Name: "流浪地球 (2019)", IsDir: true},
			{ID: "d3", Name: "某未知片子 (2018)", IsDir: true},
		},
		"d1": {{ID: "f1", Name: "阿凡达.2009.mkv"}},
		"d2": {{ID: "f2", Name: "流浪地球.2019.mkv"}},
		"d3": {{ID: "f3", Name: "某未知片子.2018.mkv"}},
	}}
	tmdb := &mockTMDB{searchFn: func(query string, _ *int) []map[string]any {
		switch query {
		case "阿凡达":
			return []map[string]any{{"id": 19995, "title": "阿凡达", "release_date": "2009-12-18"}}
		case "流浪地球":
			return []map[string]any{{"id": 348350, "title": "流浪地球", "release_date": "2019-02-05"}}
		}
		return nil
	}}
	p := planner.New(context.Background(), fs, 1, planner.TaskConfig{
		TargetDirectoryID: "root",
		TargetRootID:      "target",
		ActionType:        "move",
		UseTMDB:           true,
		Recursive:         true,
	}, planner.Settings{"mo_tmdb_api_key": "test-key"}, "task", tmdb, nil, nil, nil)
	p.SetClassificationEnhancer(batchMixedClassificationStub{})

	plan, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	matchedWorkDirs, unmatchedWorkDirs := 0, 0
	for i := range plan.Actions {
		a := &plan.Actions[i]
		isWork, _ := a.Metadata["is_work_dir"].(bool)
		if !isWork {
			continue
		}
		if a.TargetParentID == "target" {
			t.Fatalf("作品目录 %q 直接挂在 move 目标根（用户看到的就是落到任务根目录）: %+v", a.TargetName, a)
		}
		if a.Metadata["classification_matched"] == true {
			matchedWorkDirs++
		} else {
			unmatchedWorkDirs++
		}
	}
	if matchedWorkDirs != 2 {
		t.Fatalf("应有 2 个分类命中的作品目录，实际 %d; actions=%+v", matchedWorkDirs, plan.Actions)
	}
	if unmatchedWorkDirs != 1 {
		t.Fatalf("应有 1 个分类未命中的作品目录，实际 %d; actions=%+v", unmatchedWorkDirs, plan.Actions)
	}
	// 命中的进 电影/国产，未命中的兜底进 电影/
	if findEnsureDir(plan.Actions, "国产") == nil {
		t.Fatalf("匹配成功的作品应进入二级分类目录: %+v", plan.Actions)
	}
	if findEnsureDir(plan.Actions, "电影") == nil {
		t.Fatalf("未匹配成功的作品应兜底进入 电影/: %+v", plan.Actions)
	}
}

func findEnsureDir(actions []moplan.PlanAction, name string) *moplan.PlanAction {
	for i := range actions {
		if actions[i].Kind == moplan.ActionKindEnsureDir && actions[i].TargetName == name {
			return &actions[i]
		}
	}
	return nil
}

func findWorkDir(actions []moplan.PlanAction) *moplan.PlanAction {
	for i := range actions {
		if actions[i].Metadata["is_work_dir"] == true {
			return &actions[i]
		}
	}
	return nil
}

// TestSingleSeasonDirUnderShowParent 上层已有片名目录时，内层单季文件夹应作为季目录处理
// （改名 Season 02），而不是再建一层片名目录。
func TestSingleSeasonDirUnderShowParent(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {{ID: "show", Name: "脱口秀和Ta的朋友们", IsDir: true}},
		"show": {{ID: "d1", Name: "脱口秀和Ta的朋友们.第二季[全26集][国语配音/中文字幕].2024.2160p.WEB-DL.H265.AAC-ColorTV", IsDir: true}},
		"d1": {
			{ID: "f1", Name: "脱口秀和Ta的朋友们.S02E01.2160p.WEB-DL.mkv"},
			{ID: "f2", Name: "脱口秀和Ta的朋友们.S02E02.2160p.WEB-DL.mkv"},
		},
	}}
	tmdb := &mockTMDB{
		searchFn: func(query string, _ *int) []map[string]any {
			if query == "脱口秀和Ta的朋友们" {
				return []map[string]any{{"id": 999, "name": "脱口秀和Ta的朋友们", "first_air_date": "2024-06-01"}}
			}
			return nil
		},
	}
	plan, err := newTestPlanner(fs, tmdb, "root").Build()
	if err != nil {
		t.Fatal(err)
	}
	dirRenamed := ""
	seasonDir := ""
	for _, a := range plan.Actions {
		if a.SourceID == "d1" && fmt.Sprint(a.Metadata["kind_label"]) == "season_dir_rename" {
			seasonDir = a.TargetName
		}
		if a.SourceID == "show" && fmt.Sprint(a.Metadata["kind_label"]) == "dir_rename" {
			dirRenamed = a.TargetName
		}
	}
	if dirRenamed != "脱口秀和Ta的朋友们 (2024) {tmdb-999}" {
		t.Fatalf("上层片名目录应标准化为带年份+tmdb 的片名，实际 %q", dirRenamed)
	}
	if seasonDir != "Season 02" {
		t.Fatalf("内层单季文件夹应改名 Season 02，实际 %q", seasonDir)
	}
}

// TestSingleSeasonDirWithSeasonChild 单季作品根内已有标准 Season 02 子目录时，
// 外层改名片名、季目录保持、文件只改名不重复建目录。
func TestSingleSeasonDirWithSeasonChild(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {{ID: "d1", Name: "脱口秀和Ta的朋友们.第二季[全26集][国语配音/中文字幕].2024.2160p.WEB-DL.H265.AAC-ColorTV", IsDir: true}},
		"d1":   {{ID: "s2", Name: "Season 02", IsDir: true}},
		"s2": {
			{ID: "f1", Name: "脱口秀和Ta的朋友们.S02E01.2160p.WEB-DL.mkv"},
			{ID: "f2", Name: "脱口秀和Ta的朋友们.S02E02.2160p.WEB-DL.mkv"},
		},
	}}
	tmdb := &mockTMDB{
		searchFn: func(query string, _ *int) []map[string]any {
			if query == "脱口秀和Ta的朋友们" {
				return []map[string]any{{"id": 999, "name": "脱口秀和Ta的朋友们", "first_air_date": "2024-06-01"}}
			}
			return nil
		},
	}
	plan, err := newTestPlanner(fs, tmdb, "root").Build()
	if err != nil {
		t.Fatal(err)
	}
	dirRenamed := ""
	seasonRenamed := ""
	seasonEnsured := 0
	for _, a := range plan.Actions {
		if a.SourceID == "d1" && fmt.Sprint(a.Metadata["kind_label"]) == "dir_rename" {
			dirRenamed = a.TargetName
		}
		if a.SourceID == "s2" && fmt.Sprint(a.Metadata["kind_label"]) == "season_dir_rename" {
			seasonRenamed = a.TargetName
		}
		if a.Kind == "ensure_dir" {
			seasonEnsured++
		}
	}
	if dirRenamed != "脱口秀和Ta的朋友们 (2024) {tmdb-999}" {
		t.Fatalf("外层应改名片名，实际 %q", dirRenamed)
	}
	if seasonRenamed != "" {
		t.Fatalf("已有标准 Season 02 不应重复改名，实际 %q", seasonRenamed)
	}
	if seasonEnsured != 0 {
		t.Fatalf("不应重复创建季目录: %d", seasonEnsured)
	}
}
