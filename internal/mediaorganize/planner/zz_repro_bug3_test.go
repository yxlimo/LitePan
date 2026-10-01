//go:build bugrepro

package planner_test

import (
	"context"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/mediaorganize/classification"
	"litepan/internal/mediaorganize/planner"
	"litepan/internal/mediaorganize/rules"
)

func runPlan(t *testing.T, label string, fs *mockFS, tmdb *mockTMDB, cfg planner.TaskConfig, enh classification.Enhancer) {
	t.Helper()
	p := planner.New(context.Background(), fs, 1, cfg,
		planner.Settings{"mo_tmdb_api_key": "k"}, "task", tmdb, func(string) {}, nil, func() error { return nil })
	if enh != nil {
		p.SetClassificationEnhancer(enh)
	}
	plan, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("---- %s ----", label)
	for i := range plan.Actions {
		a := plan.Actions[i]
		t.Logf("  [%s] %-20s src=%-14s -> %s/%s  meta=%v", a.ID, a.Kind, a.SourceName, a.TargetParentID, a.TargetName, pickMeta(a.Metadata))
	}
	if nm, ok := plan.Diagnostics["needs_match"]; ok {
		t.Logf("  needs_match: %v", nm)
	}
}

func scatterFS() *mockFS {
	return &mockFS{dirs: map[string][]domain.FileItem{
		"root": {{ID: "f1", Name: "阿凡达 (2009).mkv"}},
	}}
}

func avaTMDB() *mockTMDB {
	return &mockTMDB{searchFn: func(string, *int) []map[string]any {
		return []map[string]any{{"id": 19995, "title": "阿凡达", "release_date": "2009-12-18"}}
	}}
}

// V1: rename 模式，根目录散落电影
func TestBug3_V1_RenameMode(t *testing.T) {
	runPlan(t, "V1 rename 模式 / 根目录散落电影 / 无分类", scatterFS(), avaTMDB(), planner.TaskConfig{
		TargetDirectoryID: "root", ActionType: "rename", UseTMDB: true, Recursive: true,
	}, nil)
}

// V2: move 模式但任务没有配置 TargetRootID（旧任务/未填移动根）
func TestBug3_V2_NoTargetRoot(t *testing.T) {
	runPlan(t, "V2 move 模式 / TargetRootID 为空 / 分类命中", scatterFS(), avaTMDB(), planner.TaskConfig{
		TargetDirectoryID: "root", TargetRootID: "", ActionType: "move", UseTMDB: true, Recursive: true,
	}, classifyByType(true))
}

// V3: move 模式但分类功能未开启
func TestBug3_V3_ClassificationOff(t *testing.T) {
	runPlan(t, "V3 move 模式 / 分类增强器 Available=false", scatterFS(), avaTMDB(), planner.TaskConfig{
		TargetDirectoryID: "root", TargetRootID: "target", ActionType: "move", UseTMDB: true, Recursive: true,
	}, classificationStub{available: false})
}

// V4: move 模式，分类应用了但没匹配上
func TestBug3_V4_ClassificationUnmatched(t *testing.T) {
	runPlan(t, "V4 move 模式 / 分类 Applied 但 Matched=false", scatterFS(), avaTMDB(), planner.TaskConfig{
		TargetDirectoryID: "root", TargetRootID: "target", ActionType: "move", UseTMDB: true, Recursive: true,
	}, classificationStub{available: true, decision: classification.Decision{
		Applied: true, Matched: false, Template: "region", DegradedReason: "tmdb_detail_unavailable",
	}})
}

// V5: move 模式，文件散落在“电影/”这类通用目录里
func TestBug3_V5_UnderGenericMediaDir(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root":  {{ID: "d1", Name: "电影", IsDir: true}},
		"d1":    {{ID: "f1", Name: "阿凡达 (2009).mkv"}},
		"target": nil,
	}}
	runPlan(t, "V5 move 模式 / 文件在 电影/ 通用目录下", fs, avaTMDB(), planner.TaskConfig{
		TargetDirectoryID: "root", TargetRootID: "target", ActionType: "move", UseTMDB: true, Recursive: true,
	}, classifyByType(true))
}

// V6: move 模式，文件散落在剧集目录树下（触发 promotedMovieParent）
func TestBug3_V6_PromotedFromTVTree(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {{ID: "show", Name: "某剧 (2020)", IsDir: true}},
		"show": {{ID: "d1", Name: "阿凡达 (2009)", IsDir: true}},
		"d1":   {{ID: "f1", Name: "阿凡达.2009.mkv"}},
	}}
	runPlan(t, "V6 move 模式 / 剧集目录下的独立电影（promoted）", fs, avaTMDB(), planner.TaskConfig{
		TargetDirectoryID: "root", TargetRootID: "target", ActionType: "move", UseTMDB: true, Recursive: true,
	}, classifyByType(true))
}

// V7: move 模式，根目录散落 + 分类功能被关掉（Available=false）→ 应该退化成任务根目录
func TestBug3_V7_MoveNoClassifier(t *testing.T) {
	runPlan(t, "V7 move 模式 / 完全没注入分类增强器（模拟手动匹配重规划）", scatterFS(), avaTMDB(), planner.TaskConfig{
		TargetDirectoryID: "root", TargetRootID: "target", ActionType: "move", UseTMDB: true, Recursive: true,
	}, nil)
}

// ---------------------------------------------------------------------------
// Bug 4：年份被当成硬性红线
// ---------------------------------------------------------------------------

func TestBug4_YearHardGate(t *testing.T) {
	// 文件名写的是 2010，TMDB 实际是 2009（跨年上映很常见）
	results := []map[string]any{
		{"id": 19995, "title": "阿凡达", "release_date": "2009-12-18"},
	}
	year := 2010
	t.Logf("PickTMDBMatchForYear(2010, tmdb 2009) => %v",
		rules.PickTMDBMatchForYear(results, &year, "movie", "阿凡达"))
	t.Logf("PickTMDBSearchMatchForYear(2010, tmdb 2009) => %v",
		rules.PickTMDBSearchMatchForYear(results, &year, "movie", "阿凡达"))
	t.Logf("PickTMDBMatchForYear(nil 年份) => %v",
		rules.PickTMDBMatchForYear(results, nil, "movie", "阿凡达"))
	t.Logf("PickUniqueTMDBAdjacentYearMatch(2010) => %v",
		rules.PickUniqueTMDBAdjacentYearMatch(results, &year, "movie", "阿凡达"))

	// 端到端：文件名带 (2010)，TMDB 只有 2009 的结果
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {{ID: "d1", Name: "阿凡达 (2010)", IsDir: true}},
		"d1":   {{ID: "f1", Name: "阿凡达.2010.1080p.mkv"}},
	}}
	runPlan(t, "V8 端到端：文件名 2010 vs TMDB 2009", fs,
		&mockTMDB{searchFn: func(_ string, y *int) []map[string]any {
			if y != nil && *y == 2010 {
				return nil // 模拟 TMDB 按年份搜索查不到
			}
			return []map[string]any{{"id": 19995, "title": "阿凡达", "release_date": "2009-12-18"}}
		}},
		planner.TaskConfig{TargetDirectoryID: "root", TargetRootID: "target", ActionType: "move", UseTMDB: true, Recursive: true},
		classifyByType(true))
}
