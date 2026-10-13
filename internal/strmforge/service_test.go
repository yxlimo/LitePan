package strmforge_test

import (
	"context"
	"strings"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/store"
	"litepan/internal/strmforge"
)

type testEnv struct {
	svc       *strmforge.Service
	store     *store.Store
	repos     *store.StrmForgeRepos
	files     *fakeFiles
	accountID int64
}

func newTestEnv(t *testing.T) *testEnv {
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
	s := store.New(db)
	accountID, err := s.Accounts.Create(ctx, &domain.Account{
		Name:       "映射账号",
		DriverType: "123_open",
		Config:     `{}`,
		IsActive:   true,
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	files := newFakeFiles()
	repos := store.NewStrmForgeRepos(db)
	svc := strmforge.NewService(repos.Tasks, repos.Nodes, files, nil)
	return &testEnv{svc: svc, store: s, repos: repos, files: files, accountID: accountID}
}

func newTestService(t *testing.T) (*strmforge.Service, int64) {
	env := newTestEnv(t)
	return env.svc, env.accountID
}

func TestCreateTask(t *testing.T) {
	ctx := context.Background()
	svc, accountID := newTestService(t)

	created, err := svc.CreateTask(ctx, &domain.StrmForgeTask{
		TaskName:          "  电影映射  ",
		AccountID:         accountID,
		SourceDirectory:   " /媒体库/电影 ",
		SourceDirectoryID: " 1001 ",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected generated id")
	}
	if created.TaskName != "电影映射" {
		t.Fatalf("expected trimmed task name, got %q", created.TaskName)
	}
	if created.SourceDirectory != "/媒体库/电影" || created.SourceDirectoryID != "1001" {
		t.Fatalf("expected trimmed directory fields, got %+v", created)
	}
	if created.Status != domain.StrmForgeStatusIdle {
		t.Fatalf("expected idle status, got %q", created.Status)
	}

	list, err := svc.ListTasks(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("unexpected list result: %+v", list)
	}

	if err := svc.DeleteTask(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err = svc.ListTasks(ctx)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list after delete, got %d", len(list))
	}
}

func TestCreateTaskValidation(t *testing.T) {
	ctx := context.Background()
	svc, accountID := newTestService(t)

	cases := []struct {
		name string
		task *domain.StrmForgeTask
		want string
	}{
		{"空任务", nil, "无效"},
		{"任务名为空", &domain.StrmForgeTask{AccountID: accountID, SourceDirectory: "/电影"}, "任务名称不能为空"},
		{
			"任务名超长",
			&domain.StrmForgeTask{TaskName: strings.Repeat("映", strmforge.MaxTaskNameLength+1), AccountID: accountID, SourceDirectory: "/电影"},
			"不能超过",
		},
		{"缺少目录", &domain.StrmForgeTask{TaskName: "任务", AccountID: accountID}, "请选择扫描目录"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateTask(ctx, tc.task)
			if err == nil {
				t.Fatalf("expected validation error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestCreateTaskAcceptsDirectoryIDOnly(t *testing.T) {
	ctx := context.Background()
	svc, accountID := newTestService(t)

	if _, err := svc.CreateTask(ctx, &domain.StrmForgeTask{
		TaskName:          "任务",
		AccountID:         accountID,
		SourceDirectoryID: "2002",
	}); err != nil {
		t.Fatalf("expected directory id to satisfy validation: %v", err)
	}
}

// 不存在的账号由数据库外键拦住，service 不再预先查账号。
func TestCreateTaskRejectsUnknownAccountViaForeignKey(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	// 缺失账号 id 与账号不存在，都不在 service 里预检，交给外键拦。
	cases := []struct {
		name      string
		accountID int64
	}{
		{"未选账号", 0},
		{"账号不存在", env.accountID + 999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.svc.CreateTask(ctx, &domain.StrmForgeTask{
				TaskName:        "任务",
				AccountID:       tc.accountID,
				SourceDirectory: "/电影",
			})
			if err == nil {
				t.Fatal("应被外键拦下")
			}
			if !strings.Contains(err.Error(), "FOREIGN KEY") {
				t.Fatalf("期望外键报错，实际：%v", err)
			}
		})
	}
}

// 未填深度时归一化补默认值；超过上限由校验拦下。
func TestCreateTaskNormalizesMaxDepth(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	task, err := env.svc.CreateTask(ctx, &domain.StrmForgeTask{
		TaskName:        "任务",
		AccountID:       env.accountID,
		SourceDirectory: "/电影",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.MaxDepth != strmforge.DefaultMaxDepth {
		t.Fatalf("缺省深度 = %d，期望 %d", task.MaxDepth, strmforge.DefaultMaxDepth)
	}

	deep, err := env.svc.CreateTask(ctx, &domain.StrmForgeTask{
		TaskName:        "深层",
		AccountID:       env.accountID,
		SourceDirectory: "/电影",
		MaxDepth:        strmforge.MaxDepthLimit + 1,
	})
	if err == nil {
		t.Fatalf("深度超过上限应报错，实际建出任务：%+v", deep)
	}
}

// 归一化负责去空白，校验只读不改。
func TestCreateTaskTrimsInputBeforeValidation(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	task, err := env.svc.CreateTask(ctx, &domain.StrmForgeTask{
		TaskName:          "  任务  ",
		AccountID:         env.accountID,
		SourceDirectory:   "  /电影  ",
		SourceDirectoryID: " 1001 ",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.TaskName != "任务" || task.SourceDirectory != "/电影" || task.SourceDirectoryID != "1001" {
		t.Fatalf("入参未被归一化：%+v", task)
	}
}
