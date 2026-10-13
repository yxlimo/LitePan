package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"litepan/internal/domain"
	"litepan/internal/store"
)

// errCredential 用来验证错误在库往返后仍是 error 而非字符串。
var errCredential = errors.New("网盘凭据失效")

func newStrmForgeAccount(t *testing.T, s interface {
	Create(ctx context.Context, a *domain.Account) (int64, error)
}) int64 {
	t.Helper()
	id, err := s.Create(context.Background(), &domain.Account{
		Name:       "映射测试账号",
		DriverType: "123_open",
		Config:     `{}`,
		IsActive:   true,
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	return id
}

func TestStrmForgeTaskCRUD(t *testing.T) {
	ctx := context.Background()
	s, repos := newStrmForgeRepos(t)
	accountID := newStrmForgeAccount(t, s.Accounts)

	task := &domain.StrmForgeTask{
		TaskName:          "电影映射",
		AccountID:         accountID,
		SourceDirectory:   "/媒体库/电影",
		SourceDirectoryID: "12345",
	}
	if err := repos.Tasks.Create(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}
	if task.ID == "" {
		t.Fatal("expected generated id")
	}
	if task.Status != domain.StrmForgeStatusIdle {
		t.Fatalf("expected default status idle, got %q", task.Status)
	}

	got, err := repos.Tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TaskName != "电影映射" || got.SourceDirectory != "/媒体库/电影" || got.SourceDirectoryID != "12345" {
		t.Fatalf("unexpected task: %+v", got)
	}
	if got.AccountID != accountID {
		t.Fatalf("unexpected account id: %d", got.AccountID)
	}

	list, err := repos.Tasks.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 task, got %d", len(list))
	}

	byAccount, err := repos.Tasks.ListByAccount(ctx, accountID)
	if err != nil {
		t.Fatalf("list by account: %v", err)
	}
	if len(byAccount) != 1 {
		t.Fatalf("expected 1 task for account, got %d", len(byAccount))
	}

	other, err := repos.Tasks.ListByAccount(ctx, accountID+999)
	if err != nil {
		t.Fatalf("list by other account: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("expected no tasks for other account, got %d", len(other))
	}

	if err := repos.Tasks.Delete(ctx, task.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repos.Tasks.Get(ctx, task.ID); err == nil {
		t.Fatal("expected not found after delete")
	}
	if err := repos.Tasks.Delete(ctx, task.ID); err == nil {
		t.Fatal("expected not found on second delete")
	}
}

func TestStrmForgeTaskCascadeOnAccountDelete(t *testing.T) {
	ctx := context.Background()
	s, repos := newStrmForgeRepos(t)
	accountID := newStrmForgeAccount(t, s.Accounts)

	task := &domain.StrmForgeTask{
		TaskName:        "剧集映射",
		AccountID:       accountID,
		SourceDirectory: "/媒体库/剧集",
	}
	if err := repos.Tasks.Create(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Accounts.Delete(ctx, accountID); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	list, err := repos.Tasks.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected cascade delete, got %d tasks", len(list))
	}
}

// 扫描结果在 store 层做序列化，往返后必须是等价的结构体。
func TestStrmForgeTaskScanResultRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, repos := newStrmForgeRepos(t)
	accountID := newStrmForgeAccount(t, s.Accounts)
	task := &domain.StrmForgeTask{TaskName: "扫描结果", AccountID: accountID, SourceDirectory: "/库"}
	if err := repos.Tasks.Create(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}

	want := &domain.StrmForgeScanResult{
		Mode: "diff", Added: 3, Resurrected: 1, Changed: 2, Removed: 4, Pending: 4,
		Incomplete: true,
		Failures: []domain.StrmForgeScanFailure{
			{DirPath: "剧集/Silo", Message: "网盘限流"},
		},
	}
	finished := time.Unix(1700000000, 0)
	if err := repos.Tasks.UpdateScanState(ctx, task.ID, domain.StrmForgeStatusIdle, finished, want); err != nil {
		t.Fatalf("UpdateScanState: %v", err)
	}

	got, err := repos.Tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LastScanResult == nil {
		t.Fatal("扫描结果未读回")
	}
	if !reflect.DeepEqual(got.LastScanResult, want) {
		t.Fatalf("往返后不一致：\n got %+v\nwant %+v", got.LastScanResult, want)
	}
	if !got.LastScanAt.Equal(finished) {
		t.Fatalf("last_scan_at = %v，期望 %v", got.LastScanAt, finished)
	}
}

// 错误信息也走同一结构，不再把裸错误文本塞进这一列。
func TestStrmForgeTaskScanResultCarriesError(t *testing.T) {
	ctx := context.Background()
	s, repos := newStrmForgeRepos(t)
	accountID := newStrmForgeAccount(t, s.Accounts)
	task := &domain.StrmForgeTask{TaskName: "扫描失败", AccountID: accountID, SourceDirectory: "/库"}
	if err := repos.Tasks.Create(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}
	want := &domain.StrmForgeScanResult{Mode: "full", Error: errCredential}
	if err := repos.Tasks.UpdateScanState(ctx, task.ID, domain.StrmForgeStatusIdle, time.Unix(1700000000, 0), want); err != nil {
		t.Fatalf("UpdateScanState: %v", err)
	}
	got, err := repos.Tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LastScanResult == nil || got.LastScanResult.Error == nil {
		t.Fatalf("错误信息未按结构往返：%+v", got.LastScanResult)
	}
	// 读回来的是 error 而不是字符串，但消息内容要一致。
	if got.LastScanResult.Error.Error() != errCredential.Error() {
		t.Fatalf("错误消息 = %q，期望 %q", got.LastScanResult.Error, errCredential.Error())
	}
}

// nil 结果落成空串，读回仍是 nil（从未扫描）。
func TestStrmForgeTaskScanResultNilStaysNil(t *testing.T) {
	ctx := context.Background()
	s, repos := newStrmForgeRepos(t)
	accountID := newStrmForgeAccount(t, s.Accounts)
	task := &domain.StrmForgeTask{TaskName: "未扫描", AccountID: accountID, SourceDirectory: "/库"}
	if err := repos.Tasks.Create(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := repos.Tasks.UpdateScanState(ctx, task.ID, domain.StrmForgeStatusScanning, time.Time{}, nil); err != nil {
		t.Fatalf("UpdateScanState: %v", err)
	}
	got, err := repos.Tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LastScanResult != nil {
		t.Fatalf("nil 结果应读回 nil，实际 %+v", got.LastScanResult)
	}
	if got.Status != domain.StrmForgeStatusScanning {
		t.Fatalf("状态 = %q，期望 scanning", got.Status)
	}
}

// newStrmForgeRepos 建一套内存库，同时返回通用 Store（建账号用）与 STRM 映射仓储。
func newStrmForgeRepos(t *testing.T) (*store.Store, *store.StrmForgeRepos) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(db), store.NewStrmForgeRepos(db)
}
