package store_test

import (
	"context"
	"testing"
	"time"

	"litepan/internal/domain"
	"litepan/internal/store"
)

func newNodeTask(t *testing.T, repos *store.StrmForgeRepos, accountID int64) string {
	t.Helper()
	task := &domain.StrmForgeTask{TaskName: "节点测试", AccountID: accountID, SourceDirectory: "/库"}
	if err := repos.Tasks.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task.ID
}

func TestStrmForgeNodesTombstoneLifecycle(t *testing.T) {
	ctx := context.Background()
	s, repos := newStrmForgeRepos(t)
	taskID := newNodeTask(t, repos, newStrmForgeAccount(t, s.Accounts))

	base := time.Unix(1700000000, 0)
	if err := repos.Nodes.MarkSeenBatch(ctx, taskID, []*domain.StrmForgeNode{
		{TaskID: taskID, RelPath: "电影/A.mkv", ParentPath: "电影", Name: "A.mkv", Size: 10, ModifiedTime: base, LastSeenAt: base},
		{TaskID: taskID, RelPath: "电影/B.mkv", ParentPath: "电影", Name: "B.mkv", Size: 20, ModifiedTime: base, LastSeenAt: base},
	}); err != nil {
		t.Fatalf("MarkSeenBatch: %v", err)
	}

	count, err := repos.Nodes.CountActiveNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("CountActiveNodes: %v", err)
	}
	if count != 2 {
		t.Fatalf("活跃数 = %d，期望 2", count)
	}

	// 打墓碑：行仍在，只是不可见。
	deletedAt := base.Add(time.Hour)
	if err := repos.Nodes.MarkDeletedBatch(ctx, taskID, []string{"电影/B.mkv"}, deletedAt); err != nil {
		t.Fatalf("MarkDeletedBatch: %v", err)
	}
	count, err = repos.Nodes.CountActiveNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("CountActiveNodes: %v", err)
	}
	if count != 1 {
		t.Fatalf("打墓碑后活跃数 = %d，期望 1", count)
	}
	deleted, err := repos.Nodes.ListDeletedNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("ListDeletedNodes: %v", err)
	}
	if len(deleted) != 1 || deleted[0].RelPath != "电影/B.mkv" {
		t.Fatalf("墓碑内容 = %+v", deleted)
	}
	if !deleted[0].DeletedAt.Equal(deletedAt) {
		t.Fatalf("deleted_at = %v，期望 %v", deleted[0].DeletedAt, deletedAt)
	}

	// 再次 MarkSeen 应复活墓碑行。
	if err := repos.Nodes.MarkSeenBatch(ctx, taskID, []*domain.StrmForgeNode{
		{TaskID: taskID, RelPath: "电影/B.mkv", ParentPath: "电影", Name: "B.mkv", Size: 22, ModifiedTime: base, LastSeenAt: deletedAt},
	}); err != nil {
		t.Fatalf("复活 MarkSeenBatch: %v", err)
	}
	deleted, err = repos.Nodes.ListDeletedNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("ListDeletedNodes: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("复活后不应有墓碑：%+v", deleted)
	}
	count, err = repos.Nodes.CountActiveNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("CountActiveNodes: %v", err)
	}
	if count != 2 {
		t.Fatalf("复活后活跃数 = %d，期望 2", count)
	}

	// 物理删除。
	n, err := repos.Nodes.PurgeNodes(ctx, taskID, []string{"电影/B.mkv"})
	if err != nil {
		t.Fatalf("PurgeNodes: %v", err)
	}
	if n != 1 {
		t.Fatalf("物理删除数 = %d，期望 1", n)
	}
	count, err = repos.Nodes.CountActiveNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("CountActiveNodes: %v", err)
	}
	if count != 1 {
		t.Fatalf("删除后活跃数 = %d，期望 1", count)
	}
}

// MarkSeenBatch 更新已存在节点的大小与修改时间，但不新增行。
func TestStrmForgeNodesMarkSeenUpdatesInPlace(t *testing.T) {
	ctx := context.Background()
	s, repos := newStrmForgeRepos(t)
	taskID := newNodeTask(t, repos, newStrmForgeAccount(t, s.Accounts))

	base := time.Unix(1700000000, 0)
	node := &domain.StrmForgeNode{TaskID: taskID, RelPath: "A.mkv", Name: "A.mkv", Size: 10, ModifiedTime: base, LastSeenAt: base}
	if err := repos.Nodes.MarkSeenBatch(ctx, taskID, []*domain.StrmForgeNode{node}); err != nil {
		t.Fatalf("首次写入: %v", err)
	}
	later := base.Add(time.Hour)
	node.Size = 999
	node.ModifiedTime = later
	node.LastSeenAt = later
	if err := repos.Nodes.MarkSeenBatch(ctx, taskID, []*domain.StrmForgeNode{node}); err != nil {
		t.Fatalf("二次写入: %v", err)
	}

	active, err := repos.Nodes.ListActiveNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("ListActiveNodes: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("行数 = %d，期望 1（应原地更新）", len(active))
	}
	if active[0].Size != 999 || !active[0].ModifiedTime.Equal(later) {
		t.Fatalf("字段未更新：%+v", active[0])
	}
}

// 任务删除时节点应被级联清掉。
func TestStrmForgeNodesCascadeOnTaskDelete(t *testing.T) {
	ctx := context.Background()
	s, repos := newStrmForgeRepos(t)
	taskID := newNodeTask(t, repos, newStrmForgeAccount(t, s.Accounts))

	if err := repos.Nodes.MarkSeenBatch(ctx, taskID, []*domain.StrmForgeNode{
		{TaskID: taskID, RelPath: "A.mkv", Name: "A.mkv", LastSeenAt: time.Unix(1, 0)},
	}); err != nil {
		t.Fatalf("MarkSeenBatch: %v", err)
	}
	if err := repos.Tasks.Delete(ctx, taskID); err != nil {
		t.Fatalf("删除任务: %v", err)
	}
	count, err := repos.Nodes.CountActiveNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("CountActiveNodes: %v", err)
	}
	if count != 0 {
		t.Fatalf("任务删除后应级联清空节点，实际残留 %d", count)
	}
}

// 同名 rel_path 在不同任务下互不影响。
func TestStrmForgeNodesIsolatedPerTask(t *testing.T) {
	ctx := context.Background()
	s, repos := newStrmForgeRepos(t)
	accountID := newStrmForgeAccount(t, s.Accounts)
	first := newNodeTask(t, repos, accountID)
	second := newNodeTask(t, repos, accountID)

	node := func(taskID string) *domain.StrmForgeNode {
		return &domain.StrmForgeNode{TaskID: taskID, RelPath: "同名.mkv", Name: "同名.mkv", LastSeenAt: time.Unix(1, 0)}
	}
	if err := repos.Nodes.MarkSeenBatch(ctx, first, []*domain.StrmForgeNode{node(first)}); err != nil {
		t.Fatalf("写第一个任务: %v", err)
	}
	if err := repos.Nodes.MarkSeenBatch(ctx, second, []*domain.StrmForgeNode{node(second)}); err != nil {
		t.Fatalf("写第二个任务: %v", err)
	}
	if err := repos.Nodes.MarkDeletedBatch(ctx, first, []string{"同名.mkv"}, time.Unix(2, 0)); err != nil {
		t.Fatalf("删除第一个任务的节点: %v", err)
	}
	firstActive, err := repos.Nodes.CountActiveNodes(ctx, first)
	if err != nil {
		t.Fatalf("count first: %v", err)
	}
	secondActive, err := repos.Nodes.CountActiveNodes(ctx, second)
	if err != nil {
		t.Fatalf("count second: %v", err)
	}
	if firstActive != 0 || secondActive != 1 {
		t.Fatalf("任务间应隔离：first=%d second=%d", firstActive, secondActive)
	}
}
