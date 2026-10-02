package planner_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

// yearMismatchTMDBStub 模拟「带年份搜不到、去掉年份搜到真实年份」的情况：
// 目录名/文件名里的年份是错的。
type yearMismatchTMDBStub struct{}

func (*yearMismatchTMDBStub) ValidateConnection(context.Context) bool { return true }

func (*yearMismatchTMDBStub) Search(_ context.Context, _ string, year *int, _ string) ([]json.RawMessage, error) {
	if year != nil {
		// 带年份搜不到任何结果。
		return nil, nil
	}
	return []json.RawMessage{
		json.RawMessage(`{"id":19995,"title":"阿凡达","original_title":"Avatar","release_date":"2009-12-18"}`),
	}, nil
}

func (*yearMismatchTMDBStub) Lookup(context.Context, string, string) (json.RawMessage, error) {
	return nil, nil
}

func (*yearMismatchTMDBStub) FetchTVSeasons(context.Context, string) ([]json.RawMessage, error) {
	return nil, nil
}

// Bug 4 端到端：目录名/文件名里的年份是错的（2010）时，
// 应匹配到真实的《阿凡达》(2009) 并把目录名纠正为真实年份，
// 而不是匹配失败后把名字「修正」成错年份、导致之后每次整理重复失败。
func TestYearMismatchCorrectsFolderNameToTMDBYear(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {{ID: "work", Name: "阿凡达 (2010)", IsDir: true}},
		"work": {{ID: "f1", Name: "阿凡达.2010.1080p.mkv"}},
	}}
	p := planner.New(context.Background(), fs, 1, planner.TaskConfig{
		TargetDirectoryID: "root",
		TargetRootID:      "target",
		ActionType:        "move",
		MediaType:         "auto",
		UseTMDB:           true,
		Recursive:         true,
	}, planner.Settings{"mo_tmdb_api_key": "k"}, "task", &yearMismatchTMDBStub{}, nil, nil, nil)

	plan, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	workDir := findWorkDir(plan.Actions)
	if workDir == nil {
		t.Fatalf("未生成作品目录: %+v", plan.Actions)
	}
	if workDir.TargetName != "阿凡达 (2009) {tmdb-19995}" {
		t.Fatalf("目录名应被纠正为 TMDB 真实年份，实际 %q", workDir.TargetName)
	}
	// TMDB 快照与年份提示挂在文件动作上（作品目录动作只带 is_work_dir/source_dir_id）。
	var fileAction *moplan.PlanAction
	for i := range plan.Actions {
		if plan.Actions[i].SourceID == "f1" {
			fileAction = &plan.Actions[i]
		}
	}
	if fileAction == nil {
		t.Fatalf("未生成文件动作: %+v", plan.Actions)
	}
	if !strings.Contains(fileAction.TargetName, "(2009)") {
		t.Fatalf("文件名也应使用 TMDB 真实年份，实际 %q", fileAction.TargetName)
	}
	if fileAction.Metadata["tmdb_id"] != "19995" {
		t.Fatalf("应带上 TMDB ID: %+v", fileAction.Metadata)
	}
	if fileAction.Metadata["year_mismatch"] != true {
		t.Fatalf("应标记 year_mismatch 供 UI 提示: %+v", fileAction.Metadata)
	}
	if fileAction.Metadata["declared_year"] != 2010 {
		t.Fatalf("应记录声明年份: %+v", fileAction.Metadata)
	}
	if fileAction.Metadata["tmdb_year"] != 2009 {
		t.Fatalf("应记录 TMDB 年份: %+v", fileAction.Metadata)
	}
	if fileAction.Metadata["group_old_dir_name"] != "阿凡达 (2010)" {
		t.Fatalf("应保留原目录名以便追溯: %+v", fileAction.Metadata)
	}
	// 匹配成功了就不该再登记 needs_match。
	needs, _ := plan.Diagnostics["needs_match"].([]map[string]any)
	for _, entry := range needs {
		if entry["title"] == "阿凡达" {
			t.Fatalf("年份不符但已匹配，不应登记 needs_match: %+v", entry)
		}
	}
}

// 候选彼此难分高下（同名不同年、无唯一相邻年份）时，仍应判 needs_match，
// 但要带上「年份对不上」的候选，让用户能区分「真的没有」和「年份挡住了」。
func TestYearMismatchBlockedMatchExposesCandidatesInNeedsMatch(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {{ID: "work", Name: "重制版电影 (2020)", IsDir: true}},
		"work": {{ID: "f1", Name: "重制版电影.2020.1080p.mkv"}},
	}}
	p := planner.New(context.Background(), fs, 1, planner.TaskConfig{
		TargetDirectoryID: "root",
		TargetRootID:      "target",
		ActionType:        "move",
		MediaType:         "auto",
		UseTMDB:           true,
		Recursive:         true,
	}, planner.Settings{"mo_tmdb_api_key": "k"}, "task", &ambiguousYearTMDBStub{}, nil, nil, nil)

	plan, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	needs, _ := plan.Diagnostics["needs_match"].([]map[string]any)
	var entry map[string]any
	for _, e := range needs {
		if e["title"] == "重制版电影" {
			entry = e
		}
	}
	if entry == nil {
		t.Fatalf("同分候选应判 needs_match: %+v", plan.Diagnostics["needs_match"])
	}
	if entry["degraded_reason"] != "year_mismatch" {
		t.Fatalf("needs_match 应标注年份原因，实际 %+v", entry)
	}
	cands, _ := entry["year_mismatch_candidates"].([]map[string]any)
	if len(cands) == 0 {
		t.Fatalf("needs_match 应带上年份不符的候选: %+v", entry)
	}
	for _, c := range cands {
		if c["year_gap"] != true || c["tmdb_id"] == "" {
			t.Fatalf("候选应带 tmdb_id 与年份不符标记: %+v", c)
		}
	}
	if !strings.Contains(fmt.Sprint(entry["reason"]), "年份") {
		t.Fatalf("needs_match 文案应点明年份原因: %+v", entry)
	}
}

type ambiguousYearTMDBStub struct{}

func (*ambiguousYearTMDBStub) ValidateConnection(context.Context) bool { return true }

func (*ambiguousYearTMDBStub) Search(_ context.Context, _ string, year *int, _ string) ([]json.RawMessage, error) {
	if year != nil {
		return nil, nil
	}
	// 两个同名不同年份、标题评分相同 => 歧义。
	return []json.RawMessage{
		json.RawMessage(`{"id":5001,"title":"重制版电影","release_date":"2010-01-01"}`),
		json.RawMessage(`{"id":5002,"title":"重制版电影","release_date":"2015-01-01"}`),
	}, nil
}

func (*ambiguousYearTMDBStub) Lookup(context.Context, string, string) (json.RawMessage, error) {
	return nil, nil
}

func (*ambiguousYearTMDBStub) FetchTVSeasons(context.Context, string) ([]json.RawMessage, error) {
	return nil, nil
}
