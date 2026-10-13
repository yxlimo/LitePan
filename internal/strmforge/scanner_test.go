package strmforge_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"litepan/internal/domain"
	"litepan/internal/strmforge"
)

func scanTask() *domain.StrmForgeTask {
	return &domain.StrmForgeTask{
		ID:                "task-1",
		TaskName:          "映射",
		AccountID:         1,
		SourceDirectory:   "/媒体库",
		SourceDirectoryID: "root",
		MaxDepth:          strmforge.DefaultMaxDepth,
	}
}

func TestScannerWalksNestedDirsAndSkipsEmptyDirs(t *testing.T) {
	base := time.Unix(1700000000, 0)
	files := newFakeFiles()
	files.putDir("root",
		files.dir("d-movie", "电影"),
		files.dir("d-empty", "空目录"),
		files.file("f1", "根目录.mkv", 10, base),
	)
	files.putDir("d-movie",
		files.dir("d-series", "流浪地球 (2019)"),
		files.file("f2", "预告.mp4", 20, base),
	)
	files.putDir("d-series", files.file("f3", "流浪地球 (2019).mkv", 30, base))
	files.putDir("d-empty")

	seen, failures, err := strmforge.NewScanner(files).Scan(context.Background(), scanTask(), nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("不该有失败项：%+v", failures)
	}
	want := []string{
		"根目录.mkv",
		"电影/预告.mp4",
		"电影/流浪地球 (2019)/流浪地球 (2019).mkv",
	}
	if len(seen) != len(want) {
		t.Fatalf("扫到 %d 个文件，期望 %d：%v", len(seen), len(want), seen)
	}
	for _, relPath := range want {
		node, ok := seen[relPath]
		if !ok {
			t.Fatalf("缺少 %q，实际 %v", relPath, seen)
		}
		if node.Name == "" || node.Size == 0 {
			t.Fatalf("%q 的字段不完整：%+v", relPath, node)
		}
	}
	// 空目录不入库：既没有文件，也没有独立目录行。
	if _, ok := seen["空目录"]; ok {
		t.Fatal("空目录不应入库")
	}
}

func TestScannerParentPathAndSize(t *testing.T) {
	base := time.Unix(1700000000, 0)
	files := newFakeFiles()
	files.putDir("root", files.dir("d1", "剧集"))
	files.putDir("d1", files.file("f1", "S01E01.mkv", 4096, base))

	seen, _, err := strmforge.NewScanner(files).Scan(context.Background(), scanTask(), nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	node, ok := seen["剧集/S01E01.mkv"]
	if !ok {
		t.Fatalf("缺少节点：%v", seen)
	}
	if node.ParentPath != "剧集" {
		t.Fatalf("ParentPath = %q，期望 剧集", node.ParentPath)
	}
	if node.Name != "S01E01.mkv" || node.Size != 4096 || !node.ModifiedTime.Equal(base) {
		t.Fatalf("节点字段不正确：%+v", node)
	}
}

// 单个目录读取失败只记账，不中断整轮扫描。
func TestScannerRecordsFailureAndContinues(t *testing.T) {
	base := time.Unix(1700000000, 0)
	boom := errors.New("网盘限流")
	files := newFakeFiles()
	files.putDir("root", files.dir("d-bad", "坏的"), files.dir("d-good", "好的"))
	files.putDir("d-bad", files.file("f1", "a.mkv", 1, base))
	files.fail("d-bad", boom)
	files.putDir("d-good", files.file("f2", "b.mkv", 2, base))

	seen, failures, err := strmforge.NewScanner(files).Scan(context.Background(), scanTask(), nil)
	if err != nil {
		t.Fatalf("单目录失败不应中断整轮: %v", err)
	}
	if len(failures) != 1 || failures[0].DirPath != "坏的" || failures[0].Message != boom.Error() {
		t.Fatalf("失败记录不正确：%+v", failures)
	}
	if _, ok := seen["好的/b.mkv"]; !ok {
		t.Fatalf("好目录应照常扫描：%v", seen)
	}
	if _, ok := seen["坏的/a.mkv"]; ok {
		t.Fatal("失败目录下的文件不应入库")
	}
}

func TestScannerReportsProgress(t *testing.T) {
	ts := time.Unix(1, 0)
	files := newFakeFiles()
	files.putDir("root", files.dir("d1", "a"), files.dir("d2", "b"))
	files.putDir("d1", files.file("f1", "x.mkv", 1, ts))
	files.putDir("d2", files.file("f2", "y.mkv", 1, ts))

	var last strmforge.ScanProgress
	count := 0
	_, _, err := strmforge.NewScanner(files).Scan(context.Background(), scanTask(), func(p strmforge.ScanProgress) {
		last = p
		count++
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if count != 3 {
		t.Fatalf("进度回调次数 = %d，期望 3（根 + 两个子目录）", count)
	}
	if last.VisitedDirs != 3 || last.Files != 2 {
		t.Fatalf("进度快照 = %+v", last)
	}
}

// 扫描必须强制刷新：命中目录缓存会拿到过期视图，把「已删」误判为「无变化」。
func TestScannerForcesRefresh(t *testing.T) {
	files := newFakeFiles()
	files.putDir("root", files.dir("d1", "子目录"))
	files.putDir("d1", files.file("f1", "a.mkv", 1, time.Unix(1, 0)))

	if _, _, err := strmforge.NewScanner(files).Scan(context.Background(), scanTask(), nil); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(files.refresh) != 2 {
		t.Fatalf("共应列举 2 个目录，实际 %d", len(files.refresh))
	}
	for i, forced := range files.refresh {
		if !forced {
			t.Fatalf("第 %d 次列举未要求刷新：缓存会让扫描看不到新增与删除", i)
		}
	}
}

// 回归：驱动可能返回纳秒精度的 mtime，而快照表存的是 Unix 秒。
// 若不在入口截断到秒，每次扫描都会把所有文件误判为已变更。
func TestScannerTruncatesSubSecondModifiedTime(t *testing.T) {
	withNanos := time.Unix(1700000000, 572947523)
	files := newFakeFiles()
	files.putDir("root", files.file("f1", "A.mkv", 0, withNanos))

	seen, _, err := strmforge.NewScanner(files).Scan(context.Background(), scanTask(), nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	node, ok := seen["A.mkv"]
	if !ok {
		t.Fatalf("缺少节点：%v", seen)
	}
	if node.ModifiedTime.Nanosecond() != 0 {
		t.Fatalf("ModifiedTime 仍带亚秒精度：%v", node.ModifiedTime)
	}
	if node.ModifiedTime.Unix() != withNanos.Unix() {
		t.Fatalf("截断后秒数应保持不变：%v", node.ModifiedTime)
	}
}

func TestScannerRejectsTaskWithoutDirectory(t *testing.T) {
	task := &domain.StrmForgeTask{ID: "t", AccountID: 1}
	if _, _, err := strmforge.NewScanner(newFakeFiles()).Scan(context.Background(), task, nil); err == nil {
		t.Fatal("未设置扫描目录时应报错")
	}
}

func TestNewScannerPanicsWithoutFiles(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("缺少 files 应 panic：依赖缺失要在启动阶段暴露")
		}
	}()
	strmforge.NewScanner(nil)
}

// 深度上限来自任务配置：扫描根算第 0 层，MaxDepth=1 表示只看根目录与它的直接子目录。
func TestScannerStopsAtTaskMaxDepth(t *testing.T) {
	base := time.Unix(1700000000, 0)
	files := newFakeFiles()
	files.putDir("root", files.dir("d1", "第一层"), files.file("f0", "根.mkv", 1, base))
	files.putDir("d1", files.dir("d2", "第二层"))
	files.putDir("d2", files.dir("d3", "第三层"), files.file("f2", "第二层.mkv", 2, base))
	files.putDir("d3", files.file("f3", "第三层.mkv", 3, base))

	task := scanTask()
	task.MaxDepth = 1

	seen, failures, err := strmforge.NewScanner(files).Scan(context.Background(), task, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, ok := seen["根.mkv"]; !ok {
		t.Fatalf("扫描根的文件应入库：%v", seen)
	}
	if _, ok := seen["第一层/第二层/第二层.mkv"]; ok {
		t.Fatalf("超过上限不应下钻：%v", seen)
	}
	if len(failures) != 1 || failures[0].DirPath != "第一层/第二层" {
		t.Fatalf("截断应记一条失败：%+v", failures)
	}
}
