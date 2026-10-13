package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"litepan/internal/domain"
	"litepan/internal/store"
	"litepan/internal/strmforge"
)

// strmForgeFixture 起一套内存库 + 真实路由，覆盖 STRM 映射的 HTTP 接口。
type strmForgeFixture struct {
	router    chi.Router
	accountID int64
	svc       *strmforge.Service
	files     *stubFileLister
}

func newStrmForgeFixture(t *testing.T) *strmForgeFixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("打开内存库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移: %v", err)
	}
	s := store.New(db)
	accountID, err := s.Accounts.Create(ctx, &domain.Account{
		Name:       "映射账号",
		DriverType: "123_open",
		Config:     `{}`,
		IsActive:   true,
	})
	if err != nil {
		t.Fatalf("创建账号: %v", err)
	}
	files := newStubFileLister()
	repos := store.NewStrmForgeRepos(db)
	svc := strmforge.NewService(repos.Tasks, repos.Nodes, files, nil)
	router := chi.NewRouter()
	NewStrmForgeHandler(svc).Register(router)
	return &strmForgeFixture{router: router, accountID: accountID, svc: svc, files: files}
}

// stubFileLister 是可控的目录列举替身；测试可随时增删条目来制造变化。
type stubFileLister struct {
	mu      sync.Mutex
	modTime time.Time
	tree    map[string][]domain.FileItem
}

func newStubFileLister() *stubFileLister {
	ts := time.Unix(1700000000, 0)
	return &stubFileLister{
		tree: map[string][]domain.FileItem{
			"": {
				{ID: "d1", Name: "电影", IsDir: true},
				{ID: "d2", Name: "剧集", IsDir: true},
				{ID: "f0", Name: "根.mkv", Size: 10, ModTime: ts},
			},
			"d1": {
				{ID: "f1", Name: "A.mkv", Size: 20, ModTime: ts},
				{ID: "f2", Name: "B.mkv", Size: 30, ModTime: ts},
			},
			"d2": {},
		},
	}
}

// remove 删除某个目录下的一个条目，用来制造「网盘上文件消失」。
func (l *stubFileLister) remove(dirID, name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	items := l.tree[dirID]
	for i, item := range items {
		if item.Name == name {
			l.tree[dirID] = append(items[:i], items[i+1:]...)
			return
		}
	}
}

func (l *stubFileLister) List(_ context.Context, _ int64, parentID string, _ bool) ([]domain.FileItem, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tree[parentID], nil
}

func (f *strmForgeFixture) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *strmForgeFixture) createTask(t *testing.T, name string) strmForgeTaskDTO {
	t.Helper()
	body := `{"task_name":"` + name + `","account_id":` + strconv.FormatInt(f.accountID, 10) +
		`,"source_directory":"/媒体库","source_directory_id":""}`
	rec := f.do(t, http.MethodPost, "/strmforge/tasks", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建任务失败：%d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data strmForgeTaskDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析创建响应: %v", err)
	}
	return out.Data
}

func (f *strmForgeFixture) scanAndWait(t *testing.T, taskID string) {
	t.Helper()
	rec := f.do(t, http.MethodPost, "/strmforge/tasks/"+taskID+"/scan", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("触发扫描失败：%d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec := f.do(t, http.MethodGet, "/strmforge/tasks/"+taskID+"/scan", "")
		var out struct {
			Data struct {
				State string `json:"state"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("解析扫描状态: %v", err)
		}
		if out.Data.State != "" && out.Data.State != "running" {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("扫描超时未结束")
}

func TestStrmForgeTaskEndpoints(t *testing.T) {
	f := newStrmForgeFixture(t)

	rec := f.do(t, http.MethodGet, "/strmforge/tasks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("列表状态码 = %d，响应 %s", rec.Code, rec.Body.String())
	}
	var empty struct {
		Data []strmForgeTaskDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil {
		t.Fatalf("解析列表响应: %v", err)
	}
	if len(empty.Data) != 0 {
		t.Fatalf("初始列表应为空，实际 %d 条", len(empty.Data))
	}

	created := f.createTask(t, "电影映射")
	if created.ID == "" || created.TaskName != "电影映射" || created.SourceDirectory != "/媒体库" {
		t.Fatalf("创建响应字段不符: %+v", created)
	}
	if created.Status != domain.StrmForgeStatusIdle {
		t.Fatalf("状态 = %q，期望 idle", created.Status)
	}
	if created.MaxDepth != strmforge.DefaultMaxDepth {
		t.Fatalf("未指定深度时应补默认值，实际 %d", created.MaxDepth)
	}

	rec = f.do(t, http.MethodDelete, "/strmforge/tasks/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("删除状态码 = %d，响应 %s", rec.Code, rec.Body.String())
	}
	rec = f.do(t, http.MethodGet, "/strmforge/tasks", "")
	var afterDelete struct {
		Data []strmForgeTaskDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &afterDelete); err != nil {
		t.Fatalf("解析删除后列表: %v", err)
	}
	if len(afterDelete.Data) != 0 {
		t.Fatalf("删除后列表应为空，实际 %d 条", len(afterDelete.Data))
	}
}

func TestStrmForgeTaskCreateValidationError(t *testing.T) {
	f := newStrmForgeFixture(t)
	rec := f.do(t, http.MethodPost, "/strmforge/tasks",
		`{"task_name":"","account_id":0,"source_directory":""}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("校验失败应返回错误状态码，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析错误响应: %v", err)
	}
	if resp.Success {
		t.Fatalf("错误响应 success 应为 false: %s", rec.Body.String())
	}
	if !strings.Contains(resp.Message, "任务名称不能为空") {
		t.Fatalf("错误文案 = %q", resp.Message)
	}
}

func TestStrmForgeCreateTaskMaxDepthLimit(t *testing.T) {
	f := newStrmForgeFixture(t)
	body := `{"task_name":"深层","account_id":` + strconv.FormatInt(f.accountID, 10) +
		`,"source_directory":"/媒体库","max_depth":` + strconv.Itoa(strmforge.MaxDepthLimit+1) + `}`
	rec := f.do(t, http.MethodPost, "/strmforge/tasks", body)
	if rec.Code == http.StatusOK {
		t.Fatalf("深度超限应被拒绝：%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "扫描深度") {
		t.Fatalf("错误文案应提到扫描深度：%s", rec.Body.String())
	}
}

// 扫描结果列表：一个文件一行，含路径、大小与修改时间。
func TestStrmForgeListNodes(t *testing.T) {
	f := newStrmForgeFixture(t)
	task := f.createTask(t, "结果页")
	f.scanAndWait(t, task.ID)

	rec := f.do(t, http.MethodGet, "/strmforge/tasks/"+task.ID+"/nodes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("列表状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	var page struct {
		Data strmForgeNodePageDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析节点列表: %v", err)
	}
	if page.Data.Total != 3 {
		t.Fatalf("文件总数 = %d，期望 3（根 1 + 电影 2）", page.Data.Total)
	}
	if len(page.Data.Nodes) != 3 {
		t.Fatalf("返回条数 = %d", len(page.Data.Nodes))
	}
	first := page.Data.Nodes[0]
	if first.RelPath != "根.mkv" || first.ParentPath != "" || first.Name != "根.mkv" {
		t.Fatalf("首行字段不符：%+v", first)
	}
	if page.Data.NextOffset != nil {
		t.Fatalf("没有下一页时 next_offset 应为 null：%v", *page.Data.NextOffset)
	}

	// 关键字过滤
	rec = f.do(t, http.MethodGet, "/strmforge/tasks/"+task.ID+"/nodes?q=B.mkv", "")
	var filtered struct {
		Data strmForgeNodePageDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("解析过滤结果: %v", err)
	}
	if filtered.Data.Total != 1 || len(filtered.Data.Nodes) != 1 {
		t.Fatalf("过滤结果 = %+v", filtered.Data)
	}
	if filtered.Data.Nodes[0].RelPath != "电影/B.mkv" {
		t.Fatalf("过滤命中路径 = %q", filtered.Data.Nodes[0].RelPath)
	}

	// 分页
	rec = f.do(t, http.MethodGet, "/strmforge/tasks/"+task.ID+"/nodes?limit=2", "")
	var paged struct {
		Data strmForgeNodePageDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &paged); err != nil {
		t.Fatalf("解析分页结果: %v", err)
	}
	if len(paged.Data.Nodes) != 2 || paged.Data.Total != 3 {
		t.Fatalf("分页结果 = %+v", paged.Data)
	}
	if paged.Data.NextOffset == nil || *paged.Data.NextOffset != 2 {
		t.Fatalf("next_offset = %v，期望 2", paged.Data.NextOffset)
	}
}

// 误报：用户判定文件仍在时，墓碑应恢复成活跃。
func TestStrmForgeDeletionRestoreAsFalseReport(t *testing.T) {
	f := newStrmForgeFixture(t)
	task := f.createTask(t, "误报")

	f.scanAndWait(t, task.ID)
	f.files.remove("d1", "B.mkv")
	f.scanAndWait(t, task.ID)

	pending := f.waitPending(t, task.ID, 1)
	if pending[0].RelPath != "电影/B.mkv" {
		t.Fatalf("待确认项 = %q", pending[0].RelPath)
	}

	rec := f.do(t, http.MethodPost, "/strmforge/tasks/"+task.ID+"/deletions/restore", `{"rel_paths":[],"all":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("恢复失败：%d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Restored int64 `json:"restored"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析恢复响应: %v", err)
	}
	if out.Data.Restored != 1 {
		t.Fatalf("恢复数量 = %d，期望 1", out.Data.Restored)
	}
	f.waitPending(t, task.ID, 0)

	// 恢复后该文件应回到扫描结果里
	rec = f.do(t, http.MethodGet, "/strmforge/tasks/"+task.ID+"/nodes?q=B.mkv", "")
	var page struct {
		Data strmForgeNodePageDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析节点列表: %v", err)
	}
	if page.Data.Total != 1 {
		t.Fatalf("恢复后应重新出现在结果里，实际 %d 条", page.Data.Total)
	}
}

// 确认删除：墓碑行被物理清除，之后不再出现在结果里。
func TestStrmForgeDeletionConfirmRemovesNode(t *testing.T) {
	f := newStrmForgeFixture(t)
	task := f.createTask(t, "确认删除")

	f.scanAndWait(t, task.ID)
	f.files.remove("d1", "B.mkv")
	f.scanAndWait(t, task.ID)
	f.waitPending(t, task.ID, 1)

	rec := f.do(t, http.MethodPost, "/strmforge/tasks/"+task.ID+"/deletions/confirm", `{"rel_paths":[],"all":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("确认删除失败：%d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Removed int64 `json:"removed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析确认响应: %v", err)
	}
	if out.Data.Removed != 1 {
		t.Fatalf("删除数量 = %d，期望 1", out.Data.Removed)
	}
	f.waitPending(t, task.ID, 0)

	rec = f.do(t, http.MethodGet, "/strmforge/tasks/"+task.ID+"/nodes?q=B.mkv", "")
	var page struct {
		Data strmForgeNodePageDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析节点列表: %v", err)
	}
	if page.Data.Total != 0 {
		t.Fatalf("确认删除后不应再出现在结果里，实际 %d 条", page.Data.Total)
	}
}

// 没有待确认项时，确认与恢复都应报错而不是静默成功。
func TestStrmForgeDeletionActionsRequirePending(t *testing.T) {
	f := newStrmForgeFixture(t)
	task := f.createTask(t, "无待确认")
	f.scanAndWait(t, task.ID)

	for _, path := range []string{"/deletions/confirm", "/deletions/restore"} {
		rec := f.do(t, http.MethodPost, "/strmforge/tasks/"+task.ID+path, `{"rel_paths":[],"all":true}`)
		if rec.Code == http.StatusOK {
			t.Fatalf("%s 在没有待确认项时应报错：%s", path, rec.Body.String())
		}
	}
}

// 空 rel_paths 且没有 all=true 时必须拒绝：批量处理要显式声明意图。
func TestStrmForgeDeletionActionsRequireExplicitAll(t *testing.T) {
	f := newStrmForgeFixture(t)
	task := f.createTask(t, "显式意图")
	f.scanAndWait(t, task.ID)

	f.files.remove("d1", "B.mkv")
	f.scanAndWait(t, task.ID)
	f.waitPending(t, task.ID, 1)

	for _, path := range []string{"/deletions/confirm", "/deletions/restore"} {
		rec := f.do(t, http.MethodPost, "/strmforge/tasks/"+task.ID+path, `{"rel_paths":[]}`)
		if rec.Code == http.StatusOK {
			t.Fatalf("%s 传空 rel_paths 且无 all 时应被拒绝：%s", path, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "all") {
			t.Fatalf("%s 的错误文案应提示 all：%s", path, rec.Body.String())
		}
	}
	// 拒绝之后待确认项仍在，说明没有误删。
	f.waitPending(t, task.ID, 1)

	// 显式 all=true 才能一次处理全部。
	rec := f.do(t, http.MethodPost, "/strmforge/tasks/"+task.ID+"/deletions/confirm", `{"rel_paths":[],"all":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("显式 all=true 应成功：%d %s", rec.Code, rec.Body.String())
	}
	f.waitPending(t, task.ID, 0)
}

// waitPending 轮询直到待确认删除数达到 want。
func (f *strmForgeFixture) waitPending(t *testing.T, taskID string, want int) []*strmForgeNodeDTO {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last []*strmForgeNodeDTO
	for time.Now().Before(deadline) {
		rec := f.do(t, http.MethodGet, "/strmforge/tasks/"+taskID+"/deletions", "")
		var out struct {
			Data []*strmForgeNodeDTO `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("解析待确认列表: %v", err)
		}
		last = out.Data
		if len(last) == want {
			return last
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待待确认删除数=%d 超时，实际 %d 条", want, len(last))
	return nil
}
