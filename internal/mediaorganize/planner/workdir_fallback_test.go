package planner_test

import (
	"context"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/mediaorganize/classification"
	"litepan/internal/mediaorganize/moplan"
	"litepan/internal/mediaorganize/planner"
)

type mediaKindClassificationStub struct{}

func (mediaKindClassificationStub) Available() bool { return true }

func (mediaKindClassificationStub) Classify(_ context.Context, req classification.Request) (classification.Decision, error) {
	seg := "电视剧"
	category := "电视剧"
	if req.MediaType == "movie" {
		seg, category = "电影", "电影"
	}
	return classification.Decision{
		Applied: true, Matched: true, Template: "media", MediaKind: req.MediaType,
		Category: category, RelativeSegments: []string{seg},
	}, nil
}

// Bug 3：扫描根目录里散落的 movie.mkv。
// 结论不是「planner 少生成了动作」——作品目录一直都会建——
// 而是分类不可用/未命中时它被建在移动根（往往就是网盘根）下，观感上等于「扔到根目录」。
func TestScatteredMovieInScanRootLandsUnderMediaKindCategory(t *testing.T) {
	for _, tt := range []struct {
		name        string
		classify    bool
		expectSeg   string
		expectMatch bool
	}{
		{name: "分类开启", classify: true, expectSeg: "电影", expectMatch: true},
		{name: "分类不可用（增强器未配置）", classify: false, expectSeg: "电影", expectMatch: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
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
			if tt.classify {
				p.SetClassificationEnhancer(mediaKindClassificationStub{})
			}
			plan, err := p.Build()
			if err != nil {
				t.Fatal(err)
			}
			segDir := findEnsureDir(plan.Actions, tt.expectSeg)
			if segDir == nil || segDir.TargetParentID != "target" {
				t.Fatalf("未生成 %s 兜底分类目录: %+v", tt.expectSeg, plan.Actions)
			}
			workDir := findWorkDir(plan.Actions)
			if workDir == nil {
				t.Fatalf("未生成作品目录: %+v", plan.Actions)
			}
			if workDir.TargetParentID != "ref:"+segDir.ID {
				t.Fatalf("作品目录应挂在 %s/ 下，实际父目录 %q", tt.expectSeg, workDir.TargetParentID)
			}
			if offenders, _ := plan.Diagnostics["work_dir_without_category"].([]map[string]any); len(offenders) != 0 {
				t.Fatalf("作品目录已进分类目录，不应被自检标记: %+v", offenders)
			}
		})
	}
}

// Bug 3-B：rename 模式下位于 /下载/ 的散落文件应建立作品目录，
// 而不是只在原地改名（补全 GenericMediaDirNames 之前是后者）。
func TestRenameModeUnderDownloadDirCreatesWorkDir(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {{ID: "dl", Name: "下载", IsDir: true}},
		"dl":   {{ID: "f1", Name: "阿凡达 (2009).mkv"}},
	}}
	tmdb := &mockTMDB{searchFn: func(string, *int) []map[string]any {
		return []map[string]any{{"id": 19995, "title": "阿凡达", "release_date": "2009-12-18"}}
	}}
	p := planner.New(context.Background(), fs, 1, planner.TaskConfig{
		TargetDirectoryID: "root",
		ActionType:        "rename",
		MediaType:         "auto",
		RenameMarker:      "tmdb",
		UseTMDB:           true,
		Recursive:         true,
	}, planner.Settings{"mo_tmdb_api_key": "k"}, "task", tmdb, nil, nil, nil)
	plan, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	// rename 模式下作品目录不带 is_work_dir 元数据（那是 move 模式的约定），
	// 这里直接断言动作结构：建目录 + 文件移入。
	var workDir *moplan.PlanAction
	for i := range plan.Actions {
		if plan.Actions[i].Kind == moplan.ActionKindEnsureDir {
			workDir = &plan.Actions[i]
			break
		}
	}
	if workDir == nil {
		t.Fatalf("下载目录下的文件应建立作品目录，而不是原地改名: %+v", plan.Actions)
	}
	if workDir.TargetName != "阿凡达 (2009) {tmdb-19995}" {
		t.Fatalf("作品目录名不正确: %q", workDir.TargetName)
	}
	if workDir.TargetParentID != "dl" {
		t.Fatalf("作品目录应建在下载目录下，实际 %q", workDir.TargetParentID)
	}
	relocated := false
	for _, a := range plan.Actions {
		if a.SourceID == "f1" && a.TargetParentID == "ref:"+workDir.ID {
			relocated = true
		}
	}
	if !relocated {
		t.Fatalf("文件应移入新建的作品目录: %+v", plan.Actions)
	}
}

// Bug 3-C：兜底自检。真的出现「作品目录直接挂在移动根且没有分类依据」时，
// 必须写进 work_dir_without_category 诊断，让用户能在计划预览里看到。
//
// 已知仍会命中的场景：作品目录组（key.dirID != ""）直接位于扫描根下，
// 源目录没有分类层级。此时不套用媒体类型兜底，是为了不破坏「已整理内容」的
// 同名冲突检测（目标根下已有同结构目录时不能因为多一层分类目录就漏检冲突）。
// 这类条目交由自检暴露给用户，而不是静默。
func TestWorkDirWithoutCategoryDiagnosticReportsRootedWorkDirs(t *testing.T) {
	for _, tt := range []struct {
		name     string
		dirName  string
		fileName string
	}{
		{name: "电影作品目录直接位于扫描根", dirName: "阿凡达 (2009)", fileName: "阿凡达.2009.mkv"},
		{name: "剧集作品目录直接位于扫描根", dirName: "脱口秀和Ta的朋友们 (2024)", fileName: "脱口秀和Ta的朋友们.S01E01.mkv"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fs := &mockFS{dirs: map[string][]domain.FileItem{
				"root": {{ID: "work", Name: tt.dirName, IsDir: true}},
				"work": {{ID: "f1", Name: tt.fileName}},
			}}
			p := planner.New(context.Background(), fs, 1, planner.TaskConfig{
				TargetDirectoryID: "root",
				TargetRootID:      "target",
				ActionType:        "move",
				MediaType:         "auto",
				UseTMDB:           false,
				Recursive:         true,
			}, nil, "task", nil, nil, nil, nil)
			plan, err := p.Build()
			if err != nil {
				t.Fatal(err)
			}
			offenders, ok := plan.Diagnostics["work_dir_without_category"].([]map[string]any)
			if !ok {
				t.Fatalf("诊断中应始终包含 work_dir_without_category: %+v", plan.Diagnostics)
			}
			if len(offenders) != 1 {
				t.Fatalf("应有 1 个无分类依据的作品目录被自检标记，实际 %d: %+v", len(offenders), offenders)
			}
			if offenders[0]["action_id"] == nil || offenders[0]["action_id"] == "" {
				t.Fatalf("自检条目应带 action_id: %+v", offenders[0])
			}
			if offenders[0]["source_id"] != "work" {
				t.Fatalf("自检条目应带 source_id: %+v", offenders[0])
			}
		})
	}
}

// 走媒体类型兜底的作品目录不应被自检标记为「无分类依据」。
func TestWorkDirWithoutCategoryDiagnosticEmptyWhenFallbackApplies(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {{ID: "f1", Name: "阿凡达 (2009).mkv"}},
	}}
	p := planner.New(context.Background(), fs, 1, planner.TaskConfig{
		TargetDirectoryID: "root",
		TargetRootID:      "target",
		ActionType:        "move",
		UseTMDB:           false,
		Recursive:         true,
	}, nil, "task", nil, nil, nil, nil)
	plan, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	offenders, ok := plan.Diagnostics["work_dir_without_category"].([]map[string]any)
	if !ok {
		t.Fatalf("诊断中应始终包含 work_dir_without_category: %+v", plan.Diagnostics)
	}
	if len(offenders) != 0 {
		t.Fatalf("散落文件已由媒体类型兜底接住，不应被标记: %+v", offenders)
	}
}

// ensureWorkDirAction 复用的目录动作应带上兜底依据，便于前端展示。
func TestWorkDirMetadataKeepsClassificationSnapshot(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {{ID: "show", Name: "脱口秀和Ta的朋友们 (2024)", IsDir: true}},
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
	p.SetClassificationEnhancer(classificationStub{available: false})
	plan, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	workDir := findWorkDir(plan.Actions)
	if workDir == nil {
		t.Fatalf("未生成作品目录: %+v", plan.Actions)
	}
	if workDir.Metadata["is_work_dir"] != true {
		t.Fatalf("作品目录应带 is_work_dir 标记: %+v", workDir.Metadata)
	}
	if _, has := workDir.Metadata["classification_applied"]; has {
		t.Fatalf("分类器未参与时不应写入 classification_applied: %+v", workDir.Metadata)
	}
}

var _ = moplan.ActionKindEnsureDir
