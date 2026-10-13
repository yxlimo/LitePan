package strmforge

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"litepan/internal/domain"
)

// MaxTaskNameLength 任务名称的最大长度，与前端输入框限制一致。
const MaxTaskNameLength = 10

const (
	// DefaultMaxDepth 扫描目录往下默认递归的层数。
	DefaultMaxDepth = 5
	// MaxDepthLimit 扫描深度的上限，避免配置出失控的全盘扫描。
	MaxDepthLimit = 10
)

// Service 依赖在构造时注入且此后不再改动，因此方法内不做 nil 判断。
type Service struct {
	tasks   domain.StrmForgeTaskRepository
	nodes   domain.StrmForgeNodeRepository
	scanner *Scanner
	log     *slog.Logger

	jobsMu sync.RWMutex
	jobs   map[string]*ScanJob
}

// NewService 缺少必需依赖时直接 panic：依赖缺失属于装配错误，应当在启动阶段暴露，
// 而不是等到请求进来才报「未就绪」。
func NewService(
	tasks domain.StrmForgeTaskRepository,
	nodes domain.StrmForgeNodeRepository,
	files FileLister,
	log *slog.Logger,
) *Service {
	if tasks == nil {
		panic("strmforge: tasks 不能为空")
	}
	if nodes == nil {
		panic("strmforge: nodes 不能为空")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		tasks:   tasks,
		nodes:   nodes,
		scanner: NewScanner(files),
		log:     log,
		jobs:    make(map[string]*ScanJob),
	}
}

func (s *Service) CreateTask(ctx context.Context, task *domain.StrmForgeTask) (*domain.StrmForgeTask, error) {
	normalizeTask(task)
	if err := s.validateTask(ctx, task); err != nil {
		return nil, err
	}
	if err := s.tasks.Create(ctx, task); err != nil {
		return nil, err
	}
	s.log.Info("STRM 映射任务已创建", "task_id", task.ID, "task_name", task.TaskName, "account_id", task.AccountID)
	return s.tasks.Get(ctx, task.ID)
}

func (s *Service) GetTask(ctx context.Context, id string) (*domain.StrmForgeTask, error) {
	return s.tasks.Get(ctx, id)
}

func (s *Service) ListTasks(ctx context.Context) ([]*domain.StrmForgeTask, error) {
	return s.tasks.List(ctx)
}

func (s *Service) DeleteTask(ctx context.Context, id string) error {
	return s.tasks.Delete(ctx, id)
}

// normalizeTask 归一化用户输入：去空白并补默认值。
// 与 validateTask 分开，因为归一化会修改入参，而校验不应该。
func normalizeTask(task *domain.StrmForgeTask) {
	if task == nil {
		return
	}
	task.TaskName = strings.TrimSpace(task.TaskName)
	task.SourceDirectory = strings.TrimSpace(task.SourceDirectory)
	task.SourceDirectoryID = strings.TrimSpace(task.SourceDirectoryID)
	if task.MaxDepth <= 0 {
		task.MaxDepth = DefaultMaxDepth
	}
	if task.Status == "" {
		task.Status = domain.StrmForgeStatusIdle
	}
}

// validateTask 校验用户输入，属于业务校验，与依赖装配无关。
// 只读入参，不做修改。
func (s *Service) validateTask(_ context.Context, task *domain.StrmForgeTask) error {
	if task == nil {
		return domain.Errorf(domain.CodeValidation, "无效 STRM 映射任务")
	}
	name := strings.TrimSpace(task.TaskName)
	if name == "" {
		return domain.Errorf(domain.CodeValidation, "任务名称不能为空")
	}
	if len([]rune(name)) > MaxTaskNameLength {
		return domain.Errorf(domain.CodeValidation, "任务名称不能超过 %d 个字符", MaxTaskNameLength)
	}
	task.TaskName = name
	task.SourceDirectory = strings.TrimSpace(task.SourceDirectory)
	task.SourceDirectoryID = strings.TrimSpace(task.SourceDirectoryID)
	if task.SourceDirectory == "" && task.SourceDirectoryID == "" {
		return domain.Errorf(domain.CodeValidation, "请选择扫描目录")
	}
	if task.MaxDepth > MaxDepthLimit {
		return domain.Errorf(domain.CodeValidation, "扫描深度不能超过 %d 层", MaxDepthLimit)
	}
	return nil
}

// ScanState 是扫描任务的状态。
const (
	ScanStateRunning = "running"
	ScanStateDone    = "done"
	ScanStateFailed  = "failed"
)

// ScanJob 是一次后台扫描的进度与结果。
type ScanJob struct {
	TaskID    string    `json:"task_id"`
	State     string    `json:"state"`
	Mode      string    `json:"mode"`
	StartedAt time.Time `json:"started_at"`
	// 指针：扫描未结束时为 nil，序列化时才会真正省略这个字段；
	// time.Time 是结构体，omitempty 对它无效。
	FinishedAt  *time.Time    `json:"finished_at,omitempty"`
	VisitedDirs int           `json:"visited_dirs"`
	TotalFiles  int           `json:"total_files"`
	Added       int           `json:"added"`
	Resurrected int           `json:"resurrected"`
	Changed     int           `json:"changed"`
	Removed     int           `json:"removed"`
	Pending     int           `json:"pending_deletions"`
	Incomplete  bool          `json:"incomplete"`
	Failures    []ScanFailure `json:"failures,omitempty"`
	// Err 是 error 而不是 string：对外序列化由 api 层转成 DTO，
	// json.Marshal 一个 error 接口只会得到 {}。
	Err      error         `json:"-"`
	Progress *ScanProgress `json:"progress,omitempty"`

	cancel context.CancelFunc
}

func (j *ScanJob) clone() *ScanJob {
	if j == nil {
		return nil
	}
	out := *j
	out.cancel = nil
	if j.FinishedAt != nil {
		finished := *j.FinishedAt
		out.FinishedAt = &finished
	}
	if j.Failures != nil {
		out.Failures = append([]ScanFailure(nil), j.Failures...)
	}
	if j.Progress != nil {
		progress := *j.Progress
		out.Progress = &progress
	}
	return &out
}

// StartScan 触发后台扫描，立即返回；同一任务已有扫描在跑时返回冲突错误。
func (s *Service) StartScan(ctx context.Context, taskID string) (*ScanJob, error) {
	task, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.SourceDirectory == "" && task.SourceDirectoryID == "" {
		return nil, domain.Errorf(domain.CodeValidation, "任务未设置扫描目录，请先编辑扫描目录")
	}
	s.jobsMu.Lock()
	if running, ok := s.jobs[taskID]; ok && running.State == ScanStateRunning {
		s.jobsMu.Unlock()
		return nil, domain.Errorf(domain.CodeValidation, "该任务正在扫描中")
	}
	job := &ScanJob{TaskID: taskID, State: ScanStateRunning, StartedAt: time.Now()}
	s.jobs[taskID] = job
	// 快照必须在放后台任务之前取：clone 直接读 job 字段，而 runScan 里的
	// updateJob 会并发写它（那边持锁，这边读时不持锁）。
	snapshot := job.clone()
	s.jobsMu.Unlock()

	// 开始扫描：置为 scanning，结果清空（nil 写成空串）。
	if err := s.tasks.UpdateScanState(ctx, taskID, domain.StrmForgeStatusScanning, time.Time{}, nil); err != nil {
		// 没有后台任务会跑完它，不留着这条永不结束的 running 记录。
		s.jobsMu.Lock()
		delete(s.jobs, taskID)
		s.jobsMu.Unlock()
		return nil, err
	}
	// 后台执行：扫描耗时随目录规模变化，请求方通过 ScanStatus 查询进度。
	go s.runScan(taskID)
	return snapshot, nil
}

// ScanStatus 返回最近一次扫描的进度与结果。
func (s *Service) ScanStatus(taskID string) *ScanJob {
	s.jobsMu.RLock()
	defer s.jobsMu.RUnlock()
	return s.jobs[taskID].clone()
}

// StopScan 请求中止正在进行的扫描。
func (s *Service) StopScan(taskID string) bool {
	s.jobsMu.Lock()
	job, ok := s.jobs[taskID]
	if !ok || job.State != ScanStateRunning {
		s.jobsMu.Unlock()
		return false
	}
	if job.cancel != nil {
		job.cancel()
	}
	s.jobsMu.Unlock()
	return true
}

func (s *Service) runScan(taskID string) {
	// 扫描必须比请求活得久：HTTP 请求返回后 r.Context() 就会被取消，
	// 所以后台任务用独立的 context，只接受 StopScan 的显式取消。
	ctx, cancel := context.WithCancel(context.Background())
	s.jobsMu.Lock()
	job := s.jobs[taskID]
	job.cancel = cancel
	s.jobsMu.Unlock()
	defer cancel()

	s.updateJob(taskID, func(j *ScanJob) { j.State = ScanStateRunning })
	err := s.scanOnce(ctx, taskID)
	finished := time.Now()

	result := s.scanResult(s.ScanStatus(taskID))
	if err != nil {
		result.Error = err
	}
	// 先落库再置终态：轮询方看到 done 时，last_scan_at 与 last_scan_result
	// 必须已经写好，否则会出现「已完成但查不到结果」的窗口。
	if stateErr := s.tasks.UpdateScanState(context.WithoutCancel(ctx), taskID,
		domain.StrmForgeStatusIdle, finished, result); stateErr != nil {
		s.log.Error("记录 STRM 映射扫描结果失败", "task_id", taskID, "error", stateErr)
	}

	s.jobsMu.Lock()
	j := s.jobs[taskID]
	j.FinishedAt = &finished
	if err != nil {
		j.State = ScanStateFailed
		j.Err = err
	} else {
		j.State = ScanStateDone
	}
	s.jobsMu.Unlock()

	if err != nil {
		s.log.Error("STRM 映射扫描失败", "task_id", taskID, "error", err)
		return
	}
	s.log.Info("STRM 映射扫描完成", "task_id", taskID, "mode", result.Mode,
		"added", result.Added, "changed", result.Changed, "removed", result.Removed)
}

func (s *Service) scanOnce(ctx context.Context, taskID string) error {
	task, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return err
	}
	active, err := s.nodes.ListActiveNodes(ctx, taskID)
	if err != nil {
		return err
	}
	activeByPath := make(map[string]*domain.StrmForgeNode, len(active))
	for _, node := range active {
		activeByPath[node.RelPath] = node
	}
	mode := "diff"
	if len(activeByPath) == 0 {
		mode = "full"
	}
	s.updateJob(taskID, func(j *ScanJob) { j.Mode = mode })

	seen, failures, err := s.scanner.Scan(ctx, task, func(p ScanProgress) {
		s.updateJob(taskID, func(j *ScanJob) { j.Progress = &p })
	})
	if err != nil {
		s.jobsMu.Lock()
		if j := s.jobs[taskID]; j != nil {
			j.Failures = failures
		}
		s.jobsMu.Unlock()
		return err
	}

	now := time.Now()
	diff := DiffNodes(activeByPath, seen)
	// 规模保护：本次覆盖不足库中一半时判定拉取不完整，跳过删除判定。
	if IncompleteScan(len(seen), len(activeByPath)) {
		diff.Incomplete = true
		diff.Removed = nil
	}

	deleted, err := s.nodes.ListDeletedNodes(ctx, taskID)
	if err != nil {
		return err
	}
	deletedSet := make(map[string]struct{}, len(deleted))
	for _, node := range deleted {
		deletedSet[node.RelPath] = struct{}{}
	}
	SplitResurrected(&diff, deletedSet)

	touched := make([]*domain.StrmForgeNode, 0, len(diff.Added)+len(diff.Resurrected)+len(diff.Changed))
	appendSeen := func(paths []string) {
		for _, relPath := range paths {
			node, ok := seen[relPath]
			if !ok {
				continue
			}
			touched = append(touched, &domain.StrmForgeNode{
				TaskID:       taskID,
				RelPath:      node.RelPath,
				ParentPath:   node.ParentPath,
				Name:         node.Name,
				Size:         node.Size,
				ModifiedTime: node.ModifiedTime,
				IsActive:     true,
				LastSeenAt:   now,
			})
		}
	}
	appendSeen(diff.Added)
	appendSeen(diff.Resurrected)
	appendSeen(diff.Changed)
	if err := s.nodes.MarkSeenBatch(ctx, taskID, touched); err != nil {
		return err
	}
	if err := s.nodes.MarkDeletedBatch(ctx, taskID, diff.Removed, now); err != nil {
		return err
	}

	s.jobsMu.Lock()
	j := s.jobs[taskID]
	j.Failures = failures
	j.VisitedDirs = j.Progress.visitedOrZero()
	j.TotalFiles = len(seen)
	j.Added = len(diff.Added)
	j.Resurrected = len(diff.Resurrected)
	j.Changed = len(diff.Changed)
	j.Removed = len(diff.Removed)
	j.Incomplete = diff.Incomplete
	// 本轮新打的墓碑，加上此前还没确认的，一起计入待确认。
	j.Pending = len(diff.Removed) + len(deletedSet) - len(diff.Resurrected)
	s.jobsMu.Unlock()
	return nil
}

func (p *ScanProgress) visitedOrZero() int {
	if p == nil {
		return 0
	}
	return p.VisitedDirs
}

func (s *Service) updateJob(taskID string, fn func(*ScanJob)) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if job, ok := s.jobs[taskID]; ok {
		fn(job)
	}
}

// ListPendingDeletions 列出待用户确认的删除项（墓碑）。
func (s *Service) ListPendingDeletions(ctx context.Context, taskID string) ([]*domain.StrmForgeNode, error) {
	if _, err := s.tasks.Get(ctx, taskID); err != nil {
		return nil, err
	}
	return s.nodes.ListDeletedNodes(ctx, taskID)
}

// ConfirmDeletions 物理删除用户确认的墓碑行。
// relPaths 为空且 all 为 false 时直接报错：批量清除必须显式声明意图，
// 否则一次空数组的调用就会清掉全部墓碑。
func (s *Service) ConfirmDeletions(ctx context.Context, taskID string, relPaths []string, all bool) (int64, error) {
	if _, err := s.tasks.Get(ctx, taskID); err != nil {
		return 0, err
	}
	targets, err := s.resolveDeletionTargets(ctx, taskID, relPaths, all)
	if err != nil {
		return 0, err
	}
	return s.nodes.PurgeNodes(ctx, taskID, targets)
}

// resolveDeletionTargets 把请求里的路径解析成实际待处理的墓碑行。
// all=true 时取全部；否则必须显式给路径，两者都没有就报错。
func (s *Service) resolveDeletionTargets(ctx context.Context, taskID string, relPaths []string, all bool) ([]string, error) {
	pending, err := s.nodes.ListDeletedNodes(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return nil, domain.Errorf(domain.CodeValidation, "没有待确认的删除项")
	}
	if all {
		targets := make([]string, 0, len(pending))
		for _, node := range pending {
			targets = append(targets, node.RelPath)
		}
		return targets, nil
	}
	if len(relPaths) == 0 {
		return nil, domain.Errorf(domain.CodeValidation, "请先选择要处理的条目；如需全部处理请显式传 all=true")
	}
	allowed := make(map[string]struct{}, len(pending))
	for _, node := range pending {
		allowed[node.RelPath] = struct{}{}
	}
	valid := make([]string, 0, len(relPaths))
	for _, relPath := range relPaths {
		relPath = strings.TrimSpace(relPath)
		if _, ok := allowed[relPath]; ok {
			valid = append(valid, relPath)
		}
	}
	if len(valid) == 0 {
		return nil, domain.Errorf(domain.CodeValidation, "所选条目都不在待确认列表中")
	}
	return valid, nil
}

// scanResult 把内存中的 job 快照转成落库用的结果结构体；
// job 已被新一次扫描覆盖时返回 nil，表示「本次没有结果」。
func (s *Service) scanResult(job *ScanJob) *domain.StrmForgeScanResult {
	if job == nil {
		return nil
	}
	result := &domain.StrmForgeScanResult{
		Mode:        job.Mode,
		Added:       job.Added,
		Resurrected: job.Resurrected,
		Changed:     job.Changed,
		Removed:     job.Removed,
		Pending:     job.Pending,
		Incomplete:  job.Incomplete,
		// ScanFailure 是 domain.StrmForgeScanFailure 的别名，元素类型一致。
		Failures: append([]ScanFailure(nil), job.Failures...),
	}
	return result
}

// ListNodes 分页列出扫描结果，是后续 strm 映射的基础数据。
func (s *Service) ListNodes(ctx context.Context, taskID string, query domain.StrmForgeNodeQuery) (*domain.StrmForgeNodePage, error) {
	if _, err := s.tasks.Get(ctx, taskID); err != nil {
		return nil, err
	}
	return s.nodes.ListNodesPage(ctx, taskID, query)
}

// RestoreDeletions 把用户判定为误报的墓碑恢复成活跃。
// 与 ConfirmDeletions 同样要求显式意图：空路径列表必须配 all=true。
func (s *Service) RestoreDeletions(ctx context.Context, taskID string, relPaths []string, all bool) (int64, error) {
	if _, err := s.tasks.Get(ctx, taskID); err != nil {
		return 0, err
	}
	targets, err := s.resolveDeletionTargets(ctx, taskID, relPaths, all)
	if err != nil {
		return 0, err
	}
	return s.nodes.RestoreNodes(ctx, taskID, targets)
}
