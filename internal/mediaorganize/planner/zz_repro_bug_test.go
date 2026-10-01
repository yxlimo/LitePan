//go:build bugrepro

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

// 复现脚本：把计划里所有 ensure_dir / relocate 的目标链打印出来，
// 观察文件最终落在哪一层。

func dumpPlan(t *testing.T, label string, plan *moplan.Plan) {
	t.Logf("---- %s ----", label)
	byID := map[string]moplanActionShim{}
	for i := range plan.Actions {
		a := plan.Actions[i]
		byID[a.ID] = moplanActionShim{parent: a.TargetParentID, name: a.TargetName, kind: a.Kind}
		t.Logf("  [%s] kind=%-20s src=%-10s(%s) -> target=%s/%s  meta=%v",
			a.ID, a.Kind, a.SourceID, a.SourceName, a.TargetParentID, a.TargetName, pickMeta(a.Metadata))
	}
	_ = byID
}

type moplanActionShim struct{ parent, name, kind string }

func pickMeta(m map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"is_work_dir", "classification_applied", "classification_matched", "classification_category", "tmdb_id", "media_kind"} {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type classificationFnStub struct {
	available bool
	fn        func(classification.Request) classification.Decision
}

func (s classificationFnStub) Available() bool { return s.available }

func (s classificationFnStub) Classify(_ context.Context, req classification.Request) (classification.Decision, error) {
	return s.fn(req), nil
}

func classifyByType(enabled bool) classificationStub {
	return classificationStub{available: enabled, decision: classification.Decision{
		Applied: true, Matched: true, Template: "media", Category: "电影",
		RelativeSegments: []string{"电影"},
	}}
}

// ---------------------------------------------------------------------------
// Bug 3：扫描根目录下散落的 movie-name.mkv
// ---------------------------------------------------------------------------

func TestBug3_ScatteredMovieInScanRoot(t *testing.T) {
	// 任务：move，目标根 = "target"；源目录 root 下直接躺着 阿凡达 (2009).mkv
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {{ID: "f1", Name: "阿凡达 (2009).mkv"}},
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
	}, planner.Settings{"mo_tmdb_api_key": "k"}, "task", tmdb, nil, nil, nil)
	p.SetClassificationEnhancer(classifyByType(true))
	plan, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	dumpPlan(t, "Bug3 根目录散落电影 + 分类开启", plan)
	for i := range plan.Actions {
		a := plan.Actions[i]
		if a.SourceID == "f1" {
			t.Logf("  >>> 文件 f1 的 relocate: target=%s/%s (workdir=%v)", a.TargetParentID, a.TargetName, pickMeta(a.Metadata))
		}
	}
}

// 同一场景但没有分类（TMDB 未匹配）
func TestBug3_ScatteredMovieNoTMDB(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {{ID: "f1", Name: "阿凡达 (2009).mkv"}},
	}}
	tmdb := &mockTMDB{searchFn: func(string, *int) []map[string]any { return nil }}
	p := planner.New(context.Background(), fs, 1, planner.TaskConfig{
		TargetDirectoryID: "root",
		TargetRootID:      "target",
		ActionType:        "move",
		UseTMDB:           true,
		Recursive:         true,
	}, planner.Settings{"mo_tmdb_api_key": "k"}, "task", tmdb, nil, nil, nil)
	p.SetClassificationEnhancer(classifyByType(true))
	plan, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	dumpPlan(t, "Bug3 根目录散落电影 + TMDB 无结果", plan)
	for i := range plan.Actions {
		a := plan.Actions[i]
		if a.SourceID == "f1" {
			t.Logf("  >>> 文件 f1 的 relocate: target=%s/%s", a.TargetParentID, a.TargetName)
		}
	}
	t.Logf("  diagnostics: %v", plan.Diagnostics["needs_match"])
}

// ---------------------------------------------------------------------------
// Bug 2：批量多个影片，部分匹配成功部分失败
// ---------------------------------------------------------------------------

func TestBug2_BatchMixedMatch(t *testing.T) {
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
		if query == "阿凡达" {
			return []map[string]any{{"id": 19995, "title": "阿凡达", "release_date": "2009-12-18"}}
		}
		if query == "流浪地球" {
			return []map[string]any{{"id": 348350, "title": "流浪地球", "release_date": "2019-02-05"}}
		}
		return nil
	}}
	// 模拟 region 模板：只有拿到 tmdb 详情才可能命中二级；没匹配到的组 Matched=false
	enh := classificationFnStub{available: true}
	enh.fn = func(req classification.Request) classification.Decision {
		if req.TMDBID == "" {
			return classification.Decision{Applied: true, Template: "region", DegradedReason: "tmdb_detail_unavailable"}
		}
		return classification.Decision{Applied: true, Matched: true, Template: "region",
			Category: "国产", RelativeSegments: []string{"电影", "国产"}}
	}
	p := planner.New(context.Background(), fs, 1, planner.TaskConfig{
		TargetDirectoryID: "root",
		TargetRootID:      "target",
		ActionType:        "move",
		UseTMDB:           true,
		Recursive:         true,
	}, planner.Settings{"mo_tmdb_api_key": "k"}, "task", tmdb, nil, nil, nil)
	p.SetClassificationEnhancer(enh)
	plan, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	dumpPlan(t, "Bug2 批量：一个匹配一个不匹配", plan)
	for i := range plan.Actions {
		a := plan.Actions[i]
		if a.Kind == "relocate" {
			t.Logf("  >>> %s -> %s/%s", a.SourceName, a.TargetParentID, a.TargetName)
		}
	}
}

// ---------------------------------------------------------------------------
// Bug 1：手动匹配后重规划，replanner 没有注入分类增强器
// ---------------------------------------------------------------------------

func TestBug1_ManualMatchReplanLosesClassification(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root":   {{ID: "movie", Name: "阿凡达 (2009)", IsDir: true}},
		"movie":  {{ID: "f1", Name: "阿凡达.2009.mkv"}},
		"target": {},
	}}
	tmdb := &mockTMDB{
		searchFn: func(string, *int) []map[string]any { return nil },
		lookupFn: func(id string) map[string]any {
			return map[string]any{"id": 19995, "title": "阿凡达", "release_date": "2009-12-18"}
		},
	}
	cfg := planner.TaskConfig{
		TargetDirectoryID: "root",
		TargetRootID:      "target",
		ActionType:        "move",
		UseTMDB:           true,
		Recursive:         true,
	}
	// 复现 service_binding.go replanMatchedGroup 的做法：planner.New 之后直接 ReplanMatchedGroup
	p := planner.New(context.Background(), fs, 1, cfg,
		planner.Settings{"mo_tmdb_api_key": "k"}, "task", tmdb, func(string) {}, nil, func() error { return nil })
	plan, err := p.ReplanMatchedGroup(planner.ManualMatchGroup{
		GroupUID:  "movie|movie|阿凡达 (2009)|阿凡达",
		MediaKind: "movie",
		DirID:     "movie",
		DirName:   "阿凡达 (2009)",
		Title:     "阿凡达",
	}, map[string]any{"id": 19995, "title": "阿凡达", "release_date": "2009-12-18"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("---- Bug1 手动匹配重规划（未注入分类增强器）----")
	for i := range plan.Actions {
		a := plan.Actions[i]
		t.Logf("  [%s] kind=%-20s src=%s -> %s/%s meta=%v", a.ID, a.Kind, a.SourceName, a.TargetParentID, a.TargetName, pickMeta(a.Metadata))
	}
	fmt.Println()
}
