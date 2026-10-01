//go:build bugrepro

package planner_test

import (
	"context"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/mediaorganize/planner"
)

// Bug3 追查 A：任务扫描的就是网盘根目录（TargetDirectoryID 为空）
func TestBug3_RootScanRoot(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"": {{ID: "f1", Name: "阿凡达 (2009).mkv"}},
	}}
	for _, at := range []string{"move", "rename"} {
		runPlan(t, "根目录扫描 / action="+at, fs, avaTMDB(), planner.TaskConfig{
			TargetDirectoryID: "", TargetRootID: "", ActionType: at, UseTMDB: true, Recursive: true,
		}, classifyByType(true))
	}
}

// Bug3 追查 B：根目录散落两个会归一化成同一作品目录名的文件
func TestBug3_DuplicateWorkDirName(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {
			{ID: "f1", Name: "阿凡达 (2009).mkv"},
			{ID: "f2", Name: "阿凡达.2009.1080p.WEB-DL.mkv"},
		},
	}}
	runPlan(t, "两个散落文件归一到同一作品目录", fs, avaTMDB(), planner.TaskConfig{
		TargetDirectoryID: "root", TargetRootID: "target", ActionType: "move", UseTMDB: true, Recursive: true,
	}, classifyByType(true))
}

// Bug3 追查 C：根目录散落剧集文件
func TestBug3_ScatteredEpisode(t *testing.T) {
	fs := &mockFS{dirs: map[string][]domain.FileItem{
		"root": {
			{ID: "f1", Name: "漫长的季节.S01E01.1080p.mkv"},
			{ID: "f2", Name: "漫长的季节.S01E02.1080p.mkv"},
		},
	}}
	tmdb := &mockTMDB{searchFn: func(_ string, _ *int) []map[string]any {
		return []map[string]any{{"id": 211220, "title": "漫长的季节", "first_air_date": "2023-04-22"}}
	}}
	runPlan(t, "根目录散落剧集", fs, tmdb, planner.TaskConfig{
		TargetDirectoryID: "root", TargetRootID: "target", ActionType: "move", UseTMDB: true, Recursive: true,
	}, classifyByType(true))
	_ = context.Background()
}
