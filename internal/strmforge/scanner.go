package strmforge

import (
	"context"
	"strings"
	"time"

	"litepan/internal/domain"
)

// 扫描护栏：避免异常目录把内存和请求数拖垮。
// 递归深度上限来自任务配置（MaxDepth），这里只兜住目录数与文件数。
const (
	maxScanDirs  = 20000
	maxScanFiles = 500000
)

// FileLister 只要求目录列举，用窄接口方便测试替身。
type FileLister interface {
	List(ctx context.Context, accountID int64, parentID string, forceRefresh bool) ([]domain.FileItem, error)
}

// ScanFailure 记录单个目录读取失败；不中断整轮扫描。
// 失败项要进落库的扫描结果，所以类型定义在 domain，这里保留别名。
type ScanFailure = domain.StrmForgeScanFailure

// ScanProgress 是扫描过程中的进度快照。
type ScanProgress struct {
	VisitedDirs int    `json:"visited_dirs"`
	Files       int    `json:"files"`
	CurrentPath string `json:"current_path"`
}

type Scanner struct {
	files FileLister
}

func NewScanner(files FileLister) *Scanner {
	if files == nil {
		panic("strmforge: files 不能为空")
	}
	return &Scanner{files: files}
}

type walkFrame struct {
	dirID   string
	relPath string
	depth   int
}

// Scan 逐目录深度优先遍历扫描根，返回本轮读到的文件节点。
// 空目录不入库：只有文件进 seen，目录结构由 RelPath 的层级隐含。
func (s *Scanner) Scan(ctx context.Context, task *domain.StrmForgeTask, onProgress func(ScanProgress)) (map[string]SeenNode, []ScanFailure, error) {
	if task == nil {
		return nil, nil, domain.Errorf(domain.CodeValidation, "无效 STRM 映射任务")
	}
	if task.SourceDirectory == "" && task.SourceDirectoryID == "" {
		return nil, nil, domain.Errorf(domain.CodeValidation, "任务未设置扫描目录")
	}
	seen := make(map[string]SeenNode)
	var failures []ScanFailure
	progress := ScanProgress{}
	visited := 0

	stack := []walkFrame{{dirID: task.SourceDirectoryID}}
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, failures, err
		}
		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if frame.depth > task.MaxDepth {
			failures = append(failures, ScanFailure{DirPath: frame.relPath, Message: "已达扫描深度上限，未继续下钻"})
			continue
		}
		if visited >= maxScanDirs {
			return nil, failures, domain.Errorf(domain.CodeInternal, "扫描目录数超过上限 %d，已中止", maxScanDirs)
		}

		// forceRefresh 必须为 true：扫描的目的是发现新增与删除，若命中目录缓存
		// 拿到的是过期视图，会把「网盘已删」误判成「无变化」。
		// 防风控靠本扫描的目录数上限、单目录失败记账与规模保护，不靠缓存遮丑。
		items, err := s.files.List(ctx, task.AccountID, frame.dirID, true)
		if err != nil {
			failures = append(failures, ScanFailure{DirPath: frame.relPath, Message: err.Error()})
			continue
		}
		visited++

		for _, item := range items {
			name := strings.TrimSpace(item.Name)
			if name == "" {
				continue
			}
			relPath := name
			if frame.relPath != "" {
				relPath = frame.relPath + "/" + name
			}
			if item.IsDir {
				stack = append(stack, walkFrame{dirID: item.ID, relPath: relPath, depth: frame.depth + 1})
				continue
			}
			if len(seen) >= maxScanFiles {
				return nil, failures, domain.Errorf(domain.CodeInternal, "扫描文件数超过上限 %d，已中止", maxScanFiles)
			}
			seen[relPath] = SeenNode{
				RelPath:    relPath,
				ParentPath: frame.relPath,
				Name:       name,
				Size:       item.Size,
				// 统一到秒：快照表存的是 Unix 秒，若保留驱动返回的纳秒精度，
				// 与库中读回的值永远不相等，会把所有文件都误判为「已变更」。
				ModifiedTime: item.ModTime.Truncate(time.Second),
			}
			progress.Files++
		}
		progress.VisitedDirs = visited
		progress.CurrentPath = frame.relPath
		if onProgress != nil {
			onProgress(progress)
		}
	}
	return seen, failures, nil
}
