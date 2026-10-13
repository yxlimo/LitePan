package strmforge_test

import (
	"context"
	"testing"
	"time"

	"litepan/internal/domain"
	"litepan/internal/strmforge"
)

// runScanAndWait 触发扫描并等待后台任务结束，返回最终状态。
func runScanAndWait(t *testing.T, env *testEnv, taskID string) *strmforge.ScanJob {
	t.Helper()
	if _, err := env.svc.StartScan(context.Background(), taskID); err != nil {
		t.Fatalf("StartScan: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job := env.svc.ScanStatus(taskID)
		if job != nil && job.State != strmforge.ScanStateRunning {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("扫描超时未结束，状态 = %+v", env.svc.ScanStatus(taskID))
	return nil
}

func activePaths(t *testing.T, env *testEnv, taskID string) []string {
	t.Helper()
	nodes, err := env.repos.Nodes.ListActiveNodes(context.Background(), taskID)
	if err != nil {
		t.Fatalf("ListActiveNodes: %v", err)
	}
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, node.RelPath)
	}
	return out
}

func newTask(t *testing.T, env *testEnv) *domain.StrmForgeTask {
	t.Helper()
	task, err := env.svc.CreateTask(context.Background(), &domain.StrmForgeTask{
		TaskName:          "扫描",
		AccountID:         env.accountID,
		SourceDirectory:   "/媒体库",
		SourceDirectoryID: "root",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func TestScanFullThenDiffThenTombstoneThenPurge(t *testing.T) {
	ctx := context.Background()
	base := time.Unix(1700000000, 0)
	env := newTestEnv(t)
	task := newTask(t, env)

	// 第一轮：空库，走全量。
	env.files.putDir("root",
		env.files.dir("d1", "电影"),
		env.files.file("f1", "根.mkv", 10, base),
	)
	env.files.putDir("d1",
		env.files.file("f2", "A.mkv", 20, base),
		env.files.file("f3", "B.mkv", 30, base),
	)

	job := runScanAndWait(t, env, task.ID)
	if job.Mode != "full" {
		t.Fatalf("首次扫描 mode = %q，期望 full", job.Mode)
	}
	if job.Added != 3 || job.Removed != 0 {
		t.Fatalf("首次扫描结果 = %+v", job)
	}
	if got := activePaths(t, env, task.ID); len(got) != 3 {
		t.Fatalf("活跃节点 = %v", got)
	}

	// 第二轮：新增一个、删除一个。
	env.files.putDir("root", env.files.dir("d1", "电影"), env.files.file("f1", "根.mkv", 10, base))
	env.files.putDir("d1", env.files.file("f2", "A.mkv", 20, base), env.files.file("f4", "C.mkv", 40, base))

	job = runScanAndWait(t, env, task.ID)
	if job.Mode != "diff" {
		t.Fatalf("第二次扫描 mode = %q，期望 diff", job.Mode)
	}
	if job.Added != 1 || job.Removed != 1 {
		t.Fatalf("第二次扫描结果 = %+v，期望 1 新增 1 删除", job)
	}
	if job.Pending != 1 {
		t.Fatalf("待确认删除数 = %d，期望 1", job.Pending)
	}

	// 墓碑待确认：行还在库里，只是 is_active=0。
	pending, err := env.svc.ListPendingDeletions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingDeletions: %v", err)
	}
	if len(pending) != 1 || pending[0].RelPath != "电影/B.mkv" {
		t.Fatalf("待确认项 = %+v", pending)
	}
	if got := activePaths(t, env, task.ID); len(got) != 3 {
		t.Fatalf("墓碑不应算活跃：%v", got)
	}

	// 确认删除后物理移除。
	removed, err := env.svc.ConfirmDeletions(ctx, task.ID, nil, true)
	if err != nil {
		t.Fatalf("ConfirmDeletions: %v", err)
	}
	if removed != 1 {
		t.Fatalf("删除数量 = %d，期望 1", removed)
	}
	pending, err = env.svc.ListPendingDeletions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingDeletions: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("确认后应无待删除项：%+v", pending)
	}
}

// 删除后又出现应算复活，不算新增。
func TestScanResurrectTombstone(t *testing.T) {
	ctx := context.Background()
	base := time.Unix(1700000000, 0)
	env := newTestEnv(t)
	task := newTask(t, env)

	env.files.putDir("root", env.files.file("f1", "A.mkv", 10, base))
	runScanAndWait(t, env, task.ID)

	// 删掉文件 → 打墓碑。
	env.files.putDir("root")
	job := runScanAndWait(t, env, task.ID)
	if job.Removed != 1 {
		t.Fatalf("应打 1 个墓碑：%+v", job)
	}

	// 文件回来 → 复活，不进新增。
	env.files.putDir("root", env.files.file("f1", "A.mkv", 10, base))
	job = runScanAndWait(t, env, task.ID)
	if job.Resurrected != 1 {
		t.Fatalf("应复活 1 个：%+v", job)
	}
	if job.Added != 0 {
		t.Fatalf("复活不应算新增：%+v", job)
	}
	if got := activePaths(t, env, task.ID); len(got) != 1 {
		t.Fatalf("复活后应重新活跃：%v", got)
	}
	pending, err := env.svc.ListPendingDeletions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingDeletions: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("复活后不应还有待确认项：%+v", pending)
	}
}

// 规模保护：本轮读到的节点远少于库中数量时，不做删除判定。
func TestScanIncompleteSkipsRemoval(t *testing.T) {
	base := time.Unix(1700000000, 0)
	env := newTestEnv(t)
	task := newTask(t, env)

	items := make([]domain.FileItem, 0, 10)
	for i := 0; i < 10; i++ {
		items = append(items, env.files.file(
			string(rune('a'+i))+".mkv", string(rune('A'+i))+".mkv", int64(i+1), base))
	}
	env.files.putDir("root", items...)
	runScanAndWait(t, env, task.ID)

	// 目录只返回 1 个条目（模拟限流导致的部分结果），远少于库中 10 个。
	env.files.putDir("root", items[0])
	job := runScanAndWait(t, env, task.ID)

	if !job.Incomplete {
		t.Fatalf("应判定为拉取不完整：%+v", job)
	}
	if job.Removed != 0 {
		t.Fatalf("不完整扫描不应产生删除：%+v", job)
	}
	if got := activePaths(t, env, task.ID); len(got) != 10 {
		t.Fatalf("库中节点应原样保留：%v", got)
	}
}

func TestScanRejectsTaskWithoutDirectory(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	task, err := env.svc.CreateTask(ctx, &domain.StrmForgeTask{
		TaskName:          "无目录",
		AccountID:         env.accountID,
		SourceDirectory:   " ",
		SourceDirectoryID: "",
	})
	if err == nil {
		t.Fatalf("未选目录应在创建时报错：%+v", task)
	}
}

func TestStartScanRejectsUnknownTask(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.svc.StartScan(context.Background(), "nope"); err == nil {
		t.Fatal("不存在的任务应报错")
	}
}

// 回归：轮询方看到 state=done 时，last_scan_at 与 last_scan_result 必须已落库。
func TestScanPersistsStateBeforeReportingDone(t *testing.T) {
	ctx := context.Background()
	base := time.Unix(1700000000, 0)
	env := newTestEnv(t)
	task := newTask(t, env)
	env.files.putDir("root", env.files.file("f1", "A.mkv", 10, base))

	if _, err := env.svc.StartScan(ctx, task.ID); err != nil {
		t.Fatalf("StartScan: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job := env.svc.ScanStatus(task.ID)
		if job == nil || job.State == strmforge.ScanStateRunning {
			time.Sleep(time.Millisecond)
			continue
		}
		if job.State != strmforge.ScanStateDone {
			t.Fatalf("扫描未成功：%+v", job)
		}
		got, err := env.svc.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if got.LastScanAt.IsZero() || got.LastScanResult == nil {
			t.Fatalf("state=done 时结果尚未落库：last_scan_at=%v result=%+v", got.LastScanAt, got.LastScanResult)
		}
		if got.LastScanResult.Added != 1 || got.LastScanResult.Mode != "full" {
			t.Fatalf("落库结果内容不对：%+v", got.LastScanResult)
		}
		return
	}
	t.Fatal("扫描超时未结束")
}

func TestScanPersistsStateOnTask(t *testing.T) {
	ctx := context.Background()
	base := time.Unix(1700000000, 0)
	env := newTestEnv(t)
	task := newTask(t, env)
	env.files.putDir("root", env.files.file("f1", "A.mkv", 10, base))
	runScanAndWait(t, env, task.ID)

	got, err := env.svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != domain.StrmForgeStatusIdle {
		t.Fatalf("扫描后状态 = %q，期望 idle", got.Status)
	}
	if got.LastScanAt.IsZero() {
		t.Fatal("扫描后应记录 last_scan_at")
	}
	if got.LastScanResult == nil {
		t.Fatal("扫描后应记录 last_scan_result")
	}
	if got.LastScanResult.Error != nil {
		t.Fatalf("扫描成功不应带错误信息：%+v", got.LastScanResult)
	}
}

func TestConfirmDeletionsIgnoresUnknownPaths(t *testing.T) {
	ctx := context.Background()
	base := time.Unix(1700000000, 0)
	env := newTestEnv(t)
	task := newTask(t, env)
	env.files.putDir("root", env.files.file("f1", "A.mkv", 10, base))
	runScanAndWait(t, env, task.ID)
	env.files.putDir("root")
	runScanAndWait(t, env, task.ID)

	if _, err := env.svc.ConfirmDeletions(ctx, task.ID, []string{"不存在.mkv"}, false); err == nil {
		t.Fatal("确认不存在的路径应报错")
	}
	// 空路径列表且未声明 all 时必须拒绝，避免误清全部墓碑。
	if _, err := env.svc.ConfirmDeletions(ctx, task.ID, nil, false); err == nil {
		t.Fatal("空路径列表应报错，必须显式传 all=true")
	}
	if _, err := env.svc.RestoreDeletions(ctx, task.ID, nil, false); err == nil {
		t.Fatal("恢复的空路径列表同样应报错")
	}
	if _, err := env.svc.ConfirmDeletions(ctx, task.ID, nil, true); err != nil {
		t.Fatalf("显式确认全部应成功: %v", err)
	}
}

// 扫描整体失败时，错误必须写进 last_scan_result 的 Error 字段，
// 而不是只出现在日志里或只挂在内存 job 上。
func TestScanFailurePersistsError(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	task := newTask(t, env)
	env.files.putDir("root", env.files.file("f1", "A.mkv", 10, time.Unix(1, 0)))
	env.files.blockDir("root")

	if _, err := env.svc.StartScan(ctx, task.ID); err != nil {
		t.Fatalf("StartScan: %v", err)
	}
	// 等扫描真正进入列举阶段再中止，确保是扫描失败而不是尚未开始。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if job := env.svc.ScanStatus(task.ID); job != nil && job.Progress != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !env.svc.StopScan(task.ID) {
		t.Fatal("扫描未在运行，无法中止")
	}

	waitDone := func() *strmforge.ScanJob {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			job := env.svc.ScanStatus(task.ID)
			if job != nil && job.State != strmforge.ScanStateRunning {
				return job
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("扫描超时未结束：%+v", env.svc.ScanStatus(task.ID))
		return nil
	}
	job := waitDone()
	if job.State != strmforge.ScanStateFailed {
		t.Fatalf("中止后状态 = %q，期望 failed", job.State)
	}
	if job.Err == nil {
		t.Fatal("内存 job 上没有 Err")
	}

	got, err := env.svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.LastScanResult == nil {
		t.Fatal("失败扫描也应写入结果结构")
	}
	if got.LastScanResult.Error == nil {
		t.Fatalf("last_scan_result 里没有 Error 字段：%+v", got.LastScanResult)
	}
	if got.LastScanResult.Error.Error() != job.Err.Error() {
		t.Fatalf("落库错误 = %q，期望 %q", got.LastScanResult.Error, job.Err.Error())
	}
}
