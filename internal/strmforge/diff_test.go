package strmforge_test

import (
	"strings"
	"testing"
	"time"

	"litepan/internal/domain"
	"litepan/internal/strmforge"
)

func node(relPath string, size int64, modified time.Time) *domain.StrmForgeNode {
	parent, name := "", relPath
	if idx := strings.LastIndex(relPath, "/"); idx >= 0 {
		parent, name = relPath[:idx], relPath[idx+1:]
	}
	return &domain.StrmForgeNode{
		TaskID:       "task",
		RelPath:      relPath,
		ParentPath:   parent,
		Name:         name,
		Size:         size,
		ModifiedTime: modified,
		IsActive:     true,
	}
}

func seen(relPath string, size int64, modified time.Time) strmforge.SeenNode {
	parent, name := "", relPath
	if idx := strings.LastIndex(relPath, "/"); idx >= 0 {
		parent, name = relPath[:idx], relPath[idx+1:]
	}
	return strmforge.SeenNode{
		RelPath:      relPath,
		ParentPath:   parent,
		Name:         name,
		Size:         size,
		ModifiedTime: modified,
	}
}

func activeSet(nodes ...*domain.StrmForgeNode) map[string]*domain.StrmForgeNode {
	out := make(map[string]*domain.StrmForgeNode, len(nodes))
	for _, n := range nodes {
		out[n.RelPath] = n
	}
	return out
}

func TestDiffNodesFullScanOnEmptyStore(t *testing.T) {
	base := time.Unix(1700000000, 0)
	diff := strmforge.DiffNodes(nil, map[string]strmforge.SeenNode{
		"电影/流浪地球.mkv":        seen("电影/流浪地球.mkv", 100, base),
		"剧集/Silo/S01E01.mkv": seen("剧集/Silo/S01E01.mkv", 200, base),
	})
	if len(diff.Added) != 2 {
		t.Fatalf("新增 = %v，期望 2 个", diff.Added)
	}
	if len(diff.Removed) != 0 {
		t.Fatalf("空库不应有删除项，实际 %v", diff.Removed)
	}
	if len(diff.Changed) != 0 || len(diff.Resurrected) != 0 {
		t.Fatalf("首次全量不应产生 changed/resurrected，实际 %+v", diff)
	}
}

func TestDiffNodesAddedOnly(t *testing.T) {
	base := time.Unix(1700000000, 0)
	existing := activeSet(node("剧集/Silo/S01E01.mkv", 200, base))
	diff := strmforge.DiffNodes(existing, map[string]strmforge.SeenNode{
		"剧集/Silo/S01E01.mkv": seen("剧集/Silo/S01E01.mkv", 200, base),
		"剧集/Silo/S01E02.mkv": seen("剧集/Silo/S01E02.mkv", 210, base),
	})
	if len(diff.Added) != 1 || diff.Added[0] != "剧集/Silo/S01E02.mkv" {
		t.Fatalf("新增 = %v", diff.Added)
	}
	if len(diff.Removed) != 0 || len(diff.Changed) != 0 {
		t.Fatalf("只增场景不应有 removed/changed，实际 %+v", diff)
	}
}

func TestDiffNodesRemovedOnly(t *testing.T) {
	base := time.Unix(1700000000, 0)
	existing := activeSet(
		node("剧集/Silo/S01E01.mkv", 200, base),
		node("剧集/Silo/S01E02.mkv", 210, base),
	)
	diff := strmforge.DiffNodes(existing, map[string]strmforge.SeenNode{
		"剧集/Silo/S01E01.mkv": seen("剧集/Silo/S01E01.mkv", 200, base),
	})
	if len(diff.Removed) != 1 || diff.Removed[0] != "剧集/Silo/S01E02.mkv" {
		t.Fatalf("删除 = %v", diff.Removed)
	}
	if len(diff.Added) != 0 {
		t.Fatalf("只删场景不应有新增，实际 %v", diff.Added)
	}
}

// 重命名与移动一律按删除 + 新增处理，不做配对。
func TestDiffNodesRenameIsDeletePlusAdd(t *testing.T) {
	base := time.Unix(1700000000, 0)
	existing := activeSet(node("剧集/Silo/S01E01.mkv", 200, base))
	diff := strmforge.DiffNodes(existing, map[string]strmforge.SeenNode{
		"剧集/Silo/S01E01-改名.mkv": seen("剧集/Silo/S01E01-改名.mkv", 200, base),
	})
	if len(diff.Removed) != 1 || diff.Removed[0] != "剧集/Silo/S01E01.mkv" {
		t.Fatalf("重命名应产生删除项，实际 %v", diff.Removed)
	}
	if len(diff.Added) != 1 || diff.Added[0] != "剧集/Silo/S01E01-改名.mkv" {
		t.Fatalf("重命名应产生新增项，实际 %v", diff.Added)
	}
}

func TestDiffNodesChangedDetectsSizeAndTime(t *testing.T) {
	base := time.Unix(1700000000, 0)
	later := base.Add(time.Hour)

	existing := activeSet(
		node("电影/A.mkv", 100, base),
		node("电影/B.mkv", 100, base),
	)
	diff := strmforge.DiffNodes(existing, map[string]strmforge.SeenNode{
		"电影/A.mkv": seen("电影/A.mkv", 999, base),  // 大小变了
		"电影/B.mkv": seen("电影/B.mkv", 100, later), // 时间变了
	})
	if len(diff.Changed) != 2 {
		t.Fatalf("changed = %v，期望两个", diff.Changed)
	}
	if len(diff.Added) != 0 || len(diff.Removed) != 0 {
		t.Fatalf("修改不应产生增删，实际 %+v", diff)
	}
}

func TestSplitResurrected(t *testing.T) {
	diff := strmforge.NodeDiff{Added: []string{"a.mkv", "b.mkv"}}
	strmforge.SplitResurrected(&diff, map[string]struct{}{"a.mkv": {}})
	if len(diff.Resurrected) != 1 || diff.Resurrected[0] != "a.mkv" {
		t.Fatalf("复活项 = %v", diff.Resurrected)
	}
	if len(diff.Added) != 1 || diff.Added[0] != "b.mkv" {
		t.Fatalf("复活项不应留在新增里，实际 %v", diff.Added)
	}
}

func TestIncompleteScan(t *testing.T) {
	cases := []struct {
		name           string
		seen, existing int
		want           bool
	}{
		{"空库首次", 0, 0, false},
		{"全量覆盖", 10, 10, false},
		{"部分变动", 9, 10, false},
		{"远少于库中（限流/读不全）", 3, 10, true},
		{"库中有内容但本轮读到零", 0, 10, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := strmforge.IncompleteScan(tc.seen, tc.existing); got != tc.want {
				t.Fatalf("IncompleteScan(%d,%d) = %v，期望 %v", tc.seen, tc.existing, got, tc.want)
			}
		})
	}
}
