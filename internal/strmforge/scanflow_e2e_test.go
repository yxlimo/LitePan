//go:build integration

// Package strmforge_test 的端到端流程测试。
//
// 这组测试刻意跑真实的 litepan 二进制、全部经 HTTP 接口操作，覆盖驱动注册、
// 鉴权、数据库迁移与扫描全链路，而不是在进程内拼装 handler。
//
// 运行方式（需要 build tag integration）：
//
//	mise run test:scanflow
//
// 基线库是固化产物：第一个用例跑完后把数据目录与素材指纹一并存下，
// 之后的每次运行都直接复用它，不再重复跑全量扫描。若基线缺失会自动重建，
// 若素材变了会因指纹不符而中止，避免用过期的基线跑出一堆假差异。
//
// 环境变量：
//
//	STRMFORGE_E2E_DB        必需的临时目录；测试在其下生成 empty.db、baseline/ 与 work-corpus/
//	STRMFORGE_E2E_CORPUS    素材目录，默认 tests/rel（仓库根下）
//	STRMFORGE_E2E_REBUILD   设为 1 时强制重建基线
//
// 账号与登录属于前置条件，不在测试范围内：每个用例的数据目录都由测试自己
// 建好，并直接写入管理员口令与 localfs 账号；任务、扫描、待确认删除等业务
// 动作一律走 API。
package strmforge_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"litepan/internal/domain"
	"litepan/internal/store"
	"litepan/pkg/security"
)

const (
	e2eAdminUser  = "admin"
	e2eAdminPass  = "ScanFlowTest-2026!"
	e2eAccountNam = "扫描流程测试库"
	e2eScanDepth  = 4
)

// ---------------------------------------------------------------- 测试骨架

// server 是一次用例里跑起来的服务实例。
type server struct {
	t       *testing.T
	baseURL string
	client  *http.Client
	dataDir string
	corpus  string
	cmd     *exec.Cmd
	logs    *os.File
}

var e2eBinary string

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "集成测试准备失败:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	dir, err := os.MkdirTemp("", "strmforge-e2e-bin-")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	e2eBinary = filepath.Join(dir, "litepan")
	// 不加 fuse 标签：localfs 用不到，也免去 /dev/fuse 依赖。
	build := exec.Command("go", "build", "-o", e2eBinary, "./cmd/litepan")
	build.Dir = repoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("构建二进制失败: %w\n%s", err, out)
	}
	return m.Run(), nil
}

func repoRoot() string {
	// 测试文件位于 internal/strmforge/，仓库根在上两级。
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// e2eDBPath 读环境变量指定的临时库路径；未设置直接失败，避免误用开发库。
func e2eDBPath(t *testing.T) string {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("STRMFORGE_E2E_DB"))
	if raw == "" {
		t.Fatal("必须设置 STRMFORGE_E2E_DB 指向可写的临时数据库文件，例如 STRMFORGE_E2E_DB=/tmp/forge-e2e/empty.db")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		t.Fatalf("解析 STRMFORGE_E2E_DB: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("创建库目录: %v", err)
	}
	return abs
}

func e2eCorpus(t *testing.T) string {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("STRMFORGE_E2E_CORPUS"))
	if raw == "" {
		raw = filepath.Join(repoRoot(), "tests", "rel")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		t.Fatalf("解析 STRMFORGE_E2E_CORPUS: %v", err)
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		t.Fatalf("素材目录不可用（%s）：先执行 mise run testdata:rel 准备素材", abs)
	}
	return abs
}

// baselineDir 是基线的存放目录：首轮全量扫描之后固化下来，后续用例只读它。
func baselineDir(db string) string {
	return filepath.Join(filepath.Dir(db), "baseline")
}

// baselineDB 是固化下来的基线库文件。
func baselineDB(db string) string {
	return filepath.Join(baselineDir(db), "litepan.db")
}

// baselineDataDir 是固化下来的基线数据目录。
func baselineDataDir(db string) string {
	return filepath.Dir(baselineDB(db))
}

// baselineStamp 记录基线对应的素材指纹，防止素材换了还拿旧基线跑。
func baselineStamp(db string) string {
	return filepath.Join(baselineDir(db), "corpus.json")
}

// baselineReady 判断基线是否已固化。
func baselineReady(db string) bool {
	if os.Getenv("STRMFORGE_E2E_REBUILD") != "" {
		return false
	}
	for _, p := range []string{baselineDB(db), baselineStamp(db)} {
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	return true
}

// corpusFingerprint 概括素材的规模：文件数与总字节数。
func corpusFingerprint(t *testing.T, corpus string) string {
	t.Helper()
	paths := walkRelPaths(t, corpus)
	var total int64
	for _, rel := range paths {
		if info, err := os.Stat(filepath.Join(corpus, rel)); err == nil {
			total += info.Size()
		}
	}
	raw, err := json.Marshal(struct {
		Files int   `json:"files"`
		Bytes int64 `json:"bytes"`
	}{Files: len(paths), Bytes: total})
	if err != nil {
		t.Fatalf("计算素材指纹: %v", err)
	}
	return string(raw)
}

// verifyBaselineMatchesCorpus 素材变了就中止：旧基线会把「素材本身的差异」
// 误报成扫描差异，排查起来很费时间。
func verifyBaselineMatchesCorpus(t *testing.T, dbPath, corpus string) {
	t.Helper()
	if !baselineReady(dbPath) {
		return
	}
	saved, err := os.ReadFile(baselineStamp(dbPath))
	if err != nil {
		t.Fatalf("读基线指纹: %v", err)
	}
	if want := corpusFingerprint(t, corpus); string(saved) != want {
		t.Fatalf("素材已变化，基线过期（基线 %s，素材 %s）。\n重建基线：删除 %s 或设 STRMFORGE_E2E_REBUILD=1",
			saved, want, baselineDir(dbPath))
	}
}

// ---------------------------------------------------------------- 用例入口

// TestStrmForgeScanFlow 串起六个场景。
//
// 第一个用例负责产出基线，基线一旦固化就与它再无关系：之后的每次运行都直接
// 复用基线，01 会被跳过。基线缺失时自动重建；素材变了则因指纹不符而中止。
func TestStrmForgeScanFlow(t *testing.T) {
	corpus := e2eCorpus(t)
	dbPath := e2eDBPath(t)

	// 基线是固化产物，不随本次运行变化：先校验它与当前素材是否匹配。
	verifyBaselineMatchesCorpus(t, dbPath, corpus)

	t.Run("01_空库全量扫描并固化基线", func(t *testing.T) {
		if baselineReady(dbPath) {
			t.Skipf("基线已固化（%s），本次直接复用；需要重建请设 STRMFORGE_E2E_REBUILD=1", baselineDir(dbPath))
		}
		// 重建前先清掉旧基线，避免残留文件混进来。
		if err := os.RemoveAll(baselineDir(dbPath)); err != nil {
			t.Fatalf("清理旧基线: %v", err)
		}

		env := newE2EEnv(t, dbPath, corpus, "")
		taskID := env.taskID

		job := env.scan(taskID)
		t.Logf("扫描 %d 个目录、%d 个文件（mode=%s）", job.VisitedDirs, job.TotalFiles, job.Mode)
		if len(job.Failures) > 0 {
			t.Fatalf("扫描不应有失败目录：%+v", job.Failures)
		}

		got := env.nodes(taskID)
		want := walkRelPaths(t, env.corpus)
		assertSameSet(t, "全量扫描结果", got, want)
		t.Logf("全量扫描命中 %d 个文件，与磁盘一致", len(want))

		// 固化基线：数据目录逐字节复制，另存一份素材指纹。
		env.stop()
		if err := copyDir(t, env.dataDir, baselineDataDir(dbPath)); err != nil {
			t.Fatalf("固化基线数据目录: %v", err)
		}
		if err := os.WriteFile(baselineStamp(dbPath), []byte(corpusFingerprint(t, corpus)), 0o644); err != nil {
			t.Fatalf("写基线指纹: %v", err)
		}
		// 空库不留存：每次用到空库时都是现建的。
		if err := os.RemoveAll(env.dataDir); err != nil {
			t.Fatalf("删除空库目录: %v", err)
		}
		t.Logf("基线已固化到 %s", baselineDir(dbPath))
	})

	// 02～06 只依赖基线，与 01 是否执行无关。
	t.Run("02_删除文件走墓碑再确认删除", func(t *testing.T) { testDeleteThenConfirm(t, corpus, dbPath) })
	t.Run("03_移走文件后恢复并标记误报", func(t *testing.T) { testMoveAwayThenRestore(t, corpus, dbPath) })
	t.Run("04_新增文件后扫描", func(t *testing.T) { testAddFiles(t, corpus, dbPath) })
	t.Run("05_重命名文件", func(t *testing.T) { testRenameFile(t, corpus, dbPath) })
	t.Run("06_移动文件到其他目录", func(t *testing.T) { testMoveFile(t, corpus, dbPath) })
}

// ---------------------------------------------------------------- 六个场景

func testDeleteThenConfirm(t *testing.T, corpus, dbPath string) {
	env := newE2EEnv(t, dbPath, corpus, baselineDataDir(dbPath))
	taskID := env.taskID

	movieRel, tvRel := pickOneEach(t, env.corpus, "movie", "tv")
	for _, rel := range []string{movieRel, tvRel} {
		if err := os.Remove(filepath.Join(env.corpus, rel)); err != nil {
			t.Fatalf("删除文件 %s: %v", rel, err)
		}
	}

	env.scan(taskID)
	assertPending(t, env, taskID, []string{movieRel, tvRel})

	// 打墓碑的文件会从活跃结果里消失，转而出现在待确认列表里。
	// 这是有意的：结果代表「当前确实在库里的文件」，待确认代表「在不在还没定」。
	assertMissing(t, "墓碑后应从活跃结果中消失", env.nodes(taskID), movieRel, tvRel)

	env.confirmDeletions(taskID, []string{movieRel, tvRel}, false)

	after := env.nodes(taskID)
	assertMissing(t, "确认删除后结果里应消失", after, movieRel, tvRel)
	assertSameSet(t, "确认删除后结果", after, walkRelPaths(t, env.corpus))

	env.stop()
}

func testMoveAwayThenRestore(t *testing.T, corpus, dbPath string) {
	env := newE2EEnv(t, dbPath, corpus, baselineDataDir(dbPath))
	taskID := env.taskID

	movieRel, tvRel := pickOneEach(t, env.corpus, "movie", "tv")
	parked := t.TempDir()
	for _, rel := range []string{movieRel, tvRel} {
		dst := filepath.Join(parked, filepath.Base(rel))
		if err := os.Rename(filepath.Join(env.corpus, rel), dst); err != nil {
			t.Fatalf("移走 %s: %v", rel, err)
		}
	}

	env.scan(taskID)
	assertPending(t, env, taskID, []string{movieRel, tvRel})

	// 文件放回后标记误报：墓碑应恢复为活跃。
	for _, rel := range []string{movieRel, tvRel} {
		src := filepath.Join(parked, filepath.Base(rel))
		if err := os.Rename(src, filepath.Join(env.corpus, rel)); err != nil {
			t.Fatalf("放回 %s: %v", rel, err)
		}
	}
	env.restoreDeletions(taskID, []string{movieRel, tvRel}, false)

	assertSameSet(t, "标记误报后结果", env.nodes(taskID), walkRelPaths(t, env.corpus))
	// 恢复后再扫一轮，不应再产生任何墓碑。
	env.scan(taskID)
	assertSameSet(t, "恢复后重扫结果", env.nodes(taskID), walkRelPaths(t, env.corpus))
	assertNoPending(t, env, taskID)

	env.stop()
}

func testAddFiles(t *testing.T, corpus, dbPath string) {
	env := newE2EEnv(t, dbPath, corpus, baselineDataDir(dbPath))
	taskID := env.taskID

	added := []string{
		"tv/新加剧集.S01E01.mkv",
		"movie/新加电影.2024.mkv",
	}
	for _, rel := range added {
		full := filepath.Join(env.corpus, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("建目录 %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte("strmforge e2e fixture"), 0o644); err != nil {
			t.Fatalf("写文件 %s: %v", rel, err)
		}
	}

	env.scan(taskID)

	got := env.nodes(taskID)
	assertContains(t, "新增文件应出现在结果里", got, added)
	assertSameSet(t, "新增后结果", got, walkRelPaths(t, env.corpus))

	env.stop()
}

func testRenameFile(t *testing.T, corpus, dbPath string) {
	env := newE2EEnv(t, dbPath, corpus, baselineDataDir(dbPath))
	taskID := env.taskID

	oldRel := pickOne(t, env.corpus, "tv")
	newRel := filepath.Join(filepath.Dir(oldRel), "改过名字的"+filepath.Base(oldRel))
	if err := os.Rename(filepath.Join(env.corpus, oldRel), filepath.Join(env.corpus, newRel)); err != nil {
		t.Fatalf("重命名 %s: %v", oldRel, err)
	}

	env.scan(taskID)

	// 重命名按「删一个 + 加一个」处理：旧路径进墓碑，新路径进结果。
	assertPending(t, env, taskID, []string{oldRel})
	got := env.nodes(taskID)
	assertContains(t, "重命名后新路径应在结果里", got, []string{newRel})
	assertSameSet(t, "重命名后结果", got, walkRelPaths(t, env.corpus))

	env.stop()
}

func testMoveFile(t *testing.T, corpus, dbPath string) {
	env := newE2EEnv(t, dbPath, corpus, baselineDataDir(dbPath))
	taskID := env.taskID

	oldRel := pickOne(t, env.corpus, "movie")
	newRel := filepath.Join("movie", "归档", filepath.Base(oldRel))
	if err := os.MkdirAll(filepath.Join(env.corpus, "movie", "归档"), 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	if err := os.Rename(filepath.Join(env.corpus, oldRel), filepath.Join(env.corpus, newRel)); err != nil {
		t.Fatalf("移动 %s: %v", oldRel, err)
	}

	env.scan(taskID)

	// 移动同样按「删一个 + 加一个」处理。
	assertPending(t, env, taskID, []string{oldRel})
	got := env.nodes(taskID)
	assertContains(t, "移动后新路径应在结果里", got, []string{newRel})
	assertSameSet(t, "移动后结果", got, walkRelPaths(t, env.corpus))

	env.stop()
}

// ---------------------------------------------------------------- 环境搭建

// e2eEnv 是一次用例的运行环境：独立数据目录、独立素材副本、真实服务进程。
type e2eEnv struct {
	*server
	taskID string
}

// newE2EEnv 准备数据目录与素材副本并拉起服务。
// fromDir 非空时以它为模板拷贝（基线库），否则从零建库并注入前置条件。
func newE2EEnv(t *testing.T, dbPath, corpus, fromDir string) *e2eEnv {
	t.Helper()

	// 工作素材目录是固定路径而不是 t.TempDir()：基线库里保存的任务
	// source_directory_id 指向它，后续用例从基线起手时才能继续指向同一个目录。
	// 每个用例开始前重置为原始素材，因此用例之间的删改互不影响。
	work := filepath.Join(filepath.Dir(dbPath), "work-corpus")
	if err := os.RemoveAll(work); err != nil {
		t.Fatalf("重置工作素材目录: %v", err)
	}
	if err := copyTree(t, corpus, work); err != nil {
		t.Fatalf("拷贝素材: %v", err)
	}

	dataDir := filepath.Join(t.TempDir(), "data")
	if fromDir != "" {
		// 数据库必须真复制：硬链接会让所有用例共享同一个 inode，
		// 上一个用例的写入会污染基线。
		if err := copyDir(t, fromDir, dataDir); err != nil {
			t.Fatalf("拷贝基线数据目录: %v", err)
		}
	} else {
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			t.Fatalf("建数据目录: %v", err)
		}
		// localfs 会校验列举路径落在账号 root_path 之内，所以这里用工作副本。
		if err := seedPrereqs(t, dataDir, work); err != nil {
			t.Fatalf("写入前置条件: %v", err)
		}
	}

	srv := startServer(t, dataDir)
	srv.corpus = work
	env := &e2eEnv{server: srv}

	// 基线库里已有任务；新库则通过 API 建一个。
	env.taskID = env.firstTaskID()
	if env.taskID == "" {
		env.taskID = env.createScanTask("集成测试")
	}
	return env
}

// seedPrereqs 写入管理员口令与 localfs 账号。这两项属于测试前置条件，
// 不是本组测试要验证的业务流程，因此直接落库。
func seedPrereqs(t *testing.T, dataDir, corpus string) error {
	t.Helper()
	ctx := t.Context()
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(dataDir, "litepan.db")})
	if err != nil {
		return fmt.Errorf("打开库: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("迁移: %w", err)
	}
	s := store.New(db)
	if err := s.Configs.Set(ctx, "admin_username", e2eAdminUser); err != nil {
		return fmt.Errorf("写管理员账号: %w", err)
	}
	if err := s.Configs.Set(ctx, "admin_password", security.HashPassword(e2eAdminPass)); err != nil {
		return fmt.Errorf("写管理员口令: %w", err)
	}
	cfg, err := json.Marshal(map[string]string{"root_path": corpus})
	if err != nil {
		return err
	}
	if _, err := s.Accounts.Create(ctx, &domain.Account{
		Name:       e2eAccountNam,
		DriverType: "localfs",
		Config:     string(cfg),
		IsActive:   true,
	}); err != nil {
		return fmt.Errorf("建 localfs 账号: %w", err)
	}
	return nil
}

// startServer 拉起真实二进制并完成登录，返回带 Cookie 的客户端。
func startServer(t *testing.T, dataDir string) *server {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dataDir, "strm"), 0o755); err != nil {
		t.Fatalf("建 strm 目录: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "server.log")
	logs, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("建日志文件: %v", err)
	}

	port := freePort(t)
	cmd := exec.Command(e2eBinary)
	cmd.Env = append(os.Environ(),
		"LITEPAN_DATA_DIR="+dataDir,
		fmt.Sprintf("LITEPAN_LISTEN=:%d", port),
		"LITEPAN_LOG_LEVEL=warn",
		"TZ=Asia/Shanghai",
	)
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动服务: %v", err)
	}

	srv := &server{
		t:       t,
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		dataDir: dataDir,
		cmd:     cmd,
		logs:    logs,
	}
	srv.client = newHTTPClient(t, srv.baseURL)
	srv.waitReady()
	srv.login()
	t.Cleanup(srv.stop)
	return srv
}

func (s *server) stop() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
		s.cmd = nil
	}
	if s.logs != nil {
		name := s.logs.Name()
		_ = s.logs.Close()
		if data, err := os.ReadFile(name); err == nil && len(data) > 0 {
			s.t.Logf("服务端日志:\n%s", data)
		}
		s.logs = nil
	}
}

func (s *server) waitReady() {
	s.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := s.client.PostForm(s.baseURL+"/api/auth/login", url.Values{
			"username": {e2eAdminUser}, "password": {e2eAdminPass},
		})
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.t.Fatalf("服务在 30 秒内没有就绪（%s）", s.baseURL)
}

func (s *server) login() {
	s.t.Helper()
	resp, err := s.client.PostForm(s.baseURL+"/api/auth/login", url.Values{
		"username": {e2eAdminUser}, "password": {e2eAdminPass},
	})
	if err != nil {
		s.t.Fatalf("登录失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		s.t.Fatalf("登录返回 %d", resp.StatusCode)
	}
}

func newHTTPClient(t *testing.T, baseURL string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("建 CookieJar: %v", err)
	}
	return &http.Client{
		Jar:     jar,
		Timeout: 120 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("取空闲端口: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// ---------------------------------------------------------------- API 调用

type apiTask struct {
	ID                string `json:"id"`
	TaskName          string `json:"task_name"`
	AccountID         int64  `json:"account_id"`
	SourceDirectory   string `json:"source_directory"`
	SourceDirectoryID string `json:"source_directory_id"`
	MaxDepth          int    `json:"max_depth"`
}

type apiNode struct {
	RelPath string `json:"rel_path"`
}

type apiScanResult struct {
	Mode       string `json:"mode"`
	Added      int    `json:"added"`
	Changed    int    `json:"changed"`
	Removed    int    `json:"removed"`
	Pending    int    `json:"pending"`
	Incomplete bool   `json:"incomplete"`
	Error      string `json:"error"`
}

type apiScanJob struct {
	State            string         `json:"state"`
	Mode             string         `json:"mode"`
	Added            int            `json:"added"`
	Changed          int            `json:"changed"`
	Removed          int            `json:"removed"`
	PendingDeletions int            `json:"pending_deletions"`
	Incomplete       bool           `json:"incomplete"`
	Error            string         `json:"error"`
	VisitedDirs      int            `json:"visited_dirs"`
	TotalFiles       int            `json:"total_files"`
	LastScanResult   *apiScanResult `json:"last_scan_result"`
	Failures         []struct {
		DirPath string `json:"dir_path"`
		Message string `json:"message"`
	} `json:"failures"`
}

// do 发一次请求并解出信封里的 data。
func (s *server) do(method, path string, body any, out any) int {
	s.t.Helper()
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			s.t.Fatalf("编码请求体: %v", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, s.baseURL+path, reader)
	if err != nil {
		s.t.Fatalf("构造请求: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatalf("读响应: %v", err)
	}
	if resp.StatusCode >= 400 && out == nil {
		s.t.Fatalf("%s %s 返回 %d：%s", method, path, resp.StatusCode, raw)
	}
	if out != nil && raw != nil {
		var env struct {
			Success bool            `json:"success"`
			Data    json.RawMessage `json:"data"`
			Message string          `json:"message"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			s.t.Fatalf("解析 %s %s 响应: %v\n%s", method, path, err, raw)
		}
		if err := json.Unmarshal(env.Data, out); err != nil {
			s.t.Fatalf("解析 %s %s 的 data: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

// firstTaskID 取库里已有的第一个任务；基线库里应已存在。
func (e *e2eEnv) firstTaskID() string {
	e.t.Helper()
	var tasks []apiTask
	e.do(http.MethodGet, "/api/strmforge/tasks", nil, &tasks)
	if len(tasks) == 0 {
		return ""
	}
	return tasks[0].ID
}

func (e *e2eEnv) createScanTask(name string) string {
	e.t.Helper()
	var accounts []struct {
		ID         int64  `json:"id"`
		DriverType string `json:"driver_type"`
	}
	e.do(http.MethodGet, "/api/admin/accounts", nil, &accounts)
	accountID := int64(0)
	for _, acc := range accounts {
		if acc.DriverType == "localfs" {
			accountID = acc.ID
			break
		}
	}
	if accountID == 0 {
		e.t.Fatal("找不到 localfs 账号")
	}

	// localfs 的目录 id 就是绝对路径，扫描子目录必须带上它。
	var task apiTask
	body := map[string]any{
		"task_name":           name,
		"account_id":          accountID,
		"source_directory":    "/",
		"source_directory_id": e.corpus,
		"max_depth":           e2eScanDepth,
	}
	e.do(http.MethodPost, "/api/strmforge/tasks", body, &task)
	if task.ID == "" {
		e.t.Fatal("创建任务没有返回 ID")
	}
	return task.ID
}

// scan 触发后台扫描并轮询到结束。
func (e *e2eEnv) scan(taskID string) apiScanJob {
	e.t.Helper()
	var started apiScanJob
	e.do(http.MethodPost, "/api/strmforge/tasks/"+taskID+"/scan", nil, &started)

	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		var job apiScanJob
		e.do(http.MethodGet, "/api/strmforge/tasks/"+taskID+"/scan", nil, &job)
		if job.State != "" && job.State != "running" {
			if job.Error != "" {
				e.t.Fatalf("扫描失败: %s", job.Error)
			}
			return job
		}
		time.Sleep(150 * time.Millisecond)
	}
	e.t.Fatal("扫描超时未结束")
	return apiScanJob{}
}

// nodes 拉取全部扫描结果（自动翻页）。
func (e *e2eEnv) nodes(taskID string) []string {
	e.t.Helper()
	var out []string
	for offset := 0; ; offset += 100 {
		var page struct {
			Nodes []apiNode `json:"nodes"`
			Total int       `json:"total"`
		}
		e.do(http.MethodGet, fmt.Sprintf("/api/strmforge/tasks/%s/nodes?limit=100&offset=%d", taskID, offset), nil, &page)
		for _, n := range page.Nodes {
			out = append(out, n.RelPath)
		}
		if len(out) >= page.Total || len(page.Nodes) == 0 {
			return out
		}
	}
}

func (e *e2eEnv) pendingPaths(taskID string) []string {
	e.t.Helper()
	var nodes []apiNode
	e.do(http.MethodGet, "/api/strmforge/tasks/"+taskID+"/deletions", nil, &nodes)
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.RelPath)
	}
	return out
}

func (e *e2eEnv) confirmDeletions(taskID string, relPaths []string, all bool) {
	e.t.Helper()
	body := map[string]any{"rel_paths": relPaths, "all": all}
	if code := e.do(http.MethodPost, "/api/strmforge/tasks/"+taskID+"/deletions/confirm", body, nil); code != http.StatusOK {
		e.t.Fatalf("确认删除返回 %d", code)
	}
}

func (e *e2eEnv) restoreDeletions(taskID string, relPaths []string, all bool) {
	e.t.Helper()
	body := map[string]any{"rel_paths": relPaths, "all": all}
	if code := e.do(http.MethodPost, "/api/strmforge/tasks/"+taskID+"/deletions/restore", body, nil); code != http.StatusOK {
		e.t.Fatalf("标记误报返回 %d", code)
	}
}

// ---------------------------------------------------------------- 断言与工具

func assertSameSet(t *testing.T, what string, got, want []string) {
	t.Helper()
	g, w := sortedCopy(got), sortedCopy(want)
	if len(g) != len(w) {
		t.Fatalf("%s：数量不符，得到 %d，期望 %d\n多出: %v\n缺少: %v", what, len(g), len(w), diff(g, w), diff(w, g))
	}
	for i := range g {
		if g[i] != w[i] {
			t.Fatalf("%s：第 %d 项不同，得到 %q，期望 %q", what, i, g[i], w[i])
		}
	}
}

func assertContains(t *testing.T, what string, got, want []string) {
	t.Helper()
	set := make(map[string]struct{}, len(got))
	for _, g := range got {
		set[g] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			t.Fatalf("%s：缺少 %q", what, w)
		}
	}
}

func assertMissing(t *testing.T, what string, got []string, unwanted ...string) {
	t.Helper()
	set := make(map[string]struct{}, len(got))
	for _, g := range got {
		set[g] = struct{}{}
	}
	for _, u := range unwanted {
		if _, ok := set[u]; ok {
			t.Fatalf("%s：不应出现 %q", what, u)
		}
	}
}

func assertPending(t *testing.T, e *e2eEnv, taskID string, want []string) {
	t.Helper()
	assertSameSet(t, "待确认删除", e.pendingPaths(taskID), want)
}

func assertNoPending(t *testing.T, e *e2eEnv, taskID string) {
	t.Helper()
	if got := e.pendingPaths(taskID); len(got) != 0 {
		t.Fatalf("不应有待确认项，实际 %v", got)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func diff(a, b []string) []string {
	set := make(map[string]struct{}, len(b))
	for _, x := range b {
		set[x] = struct{}{}
	}
	var out []string
	for _, x := range a {
		if _, ok := set[x]; !ok {
			out = append(out, x)
		}
	}
	return out
}

// walkRelPaths 列出目录下所有文件的相对路径，作为扫描结果的对照基准。
func walkRelPaths(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("遍历素材目录 %s: %v", root, err)
	}
	return out
}

// pickOneEach 各挑一个指定子目录下的文件。
func pickOneEach(t *testing.T, root, dirA, dirB string) (string, string) {
	t.Helper()
	all := sortedCopy(walkRelPaths(t, root))
	find := func(dir string) string {
		for _, rel := range all {
			if strings.HasPrefix(rel, dir+"/") {
				return rel
			}
		}
		t.Fatalf("素材里没有 %s 下的文件", dir)
		return ""
	}
	return find(dirA), find(dirB)
}

func pickOne(t *testing.T, root, dir string) string {
	t.Helper()
	for _, rel := range sortedCopy(walkRelPaths(t, root)) {
		if strings.HasPrefix(rel, dir+"/") {
			return rel
		}
	}
	t.Fatalf("素材里没有 %s 下的文件", dir)
	return ""
}

// copyDir 逐字节复制目录树。用于数据库这类会被写入的内容。
func copyDir(t *testing.T, src, dst string) error {
	t.Helper()
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// copyTree 复制目录树。优先建硬链接：这些用例只做删、改名、移动和新增，
// 不会原地改写文件内容，硬链接足够安全且比复制 234 MB 快一个数量级。
// 只可用于素材目录，不可用于会被写入的数据库。
func copyTree(t *testing.T, src, dst string) error {
	t.Helper()
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.Link(path, target); err == nil {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(target, data, 0o644)
	})
}
