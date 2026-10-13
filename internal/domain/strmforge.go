package domain

import (
	"context"
	"time"
)

const (
	StrmForgeStatusIdle     = "idle"
	StrmForgeStatusScanning = "scanning"
)

// StrmForgeScanFailure 记录单个目录读取失败；不中断整轮扫描。
type StrmForgeScanFailure struct {
	DirPath string `json:"dir_path"`
	Message string `json:"message"`
}

// StrmForgeScanResult 是一次扫描的落库结果。
// 库里的列是 TEXT（SQLite 没有原生 JSON 类型，声明 JSON 反而拿到 NUMERIC affinity
// 会把 '0123' 这类数字串转成整数），序列化与反序列化只在 store 层做，
// 其他层只看到这个结构体。
type StrmForgeScanResult struct {
	Mode        string                 `json:"mode"`
	Added       int                    `json:"added"`
	Resurrected int                    `json:"resurrected"`
	Changed     int                    `json:"changed"`
	Removed     int                    `json:"removed"`
	Pending     int                    `json:"pending"`
	Incomplete  bool                   `json:"incomplete,omitempty"`
	Failures    []StrmForgeScanFailure `json:"failures,omitempty"`
	// Error 是 error 而不是 string：库里存文本，读回时重新构造 error。
	// 本结构体不直接对外序列化，HTTP 响应由 api 层转成 DTO。
	Error error `json:"-"`
}

type StrmForgeTask struct {
	ID                string
	TaskName          string
	AccountID         int64
	SourceDirectory   string
	SourceDirectoryID string
	MaxDepth          int
	Status            string
	LastScanAt        time.Time
	LastScanResult    *StrmForgeScanResult
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// StrmForgeNode 是扫描到的一个文件节点。
// 快照只收文件，空目录不入库；目录结构由 RelPath 的路径层级隐含。
type StrmForgeNode struct {
	TaskID       string
	RelPath      string
	ParentPath   string
	Name         string
	Size         int64
	ModifiedTime time.Time
	IsActive     bool
	LastSeenAt   time.Time
	DeletedAt    time.Time
}

type StrmForgeTaskRepository interface {
	Create(ctx context.Context, task *StrmForgeTask) error
	Delete(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (*StrmForgeTask, error)
	List(ctx context.Context) ([]*StrmForgeTask, error)
	ListByAccount(ctx context.Context, accountID int64) ([]*StrmForgeTask, error)
	UpdateScanState(ctx context.Context, id, status string, lastScanAt time.Time, result *StrmForgeScanResult) error
}

// StrmForgeNodeRepository 管理目录快照。
// MarkSeenBatch 同时负责「首次出现」与「墓碑复活」；MarkDeletedBatch 只打墓碑，
// 物理删除留给用户确认后调用 PurgeNodes。
type StrmForgeNodeRepository interface {
	ListActiveNodes(ctx context.Context, taskID string) ([]*StrmForgeNode, error)
	ListDeletedNodes(ctx context.Context, taskID string) ([]*StrmForgeNode, error)
	ListNodesPage(ctx context.Context, taskID string, query StrmForgeNodeQuery) (*StrmForgeNodePage, error)
	CountActiveNodes(ctx context.Context, taskID string) (int, error)
	MarkSeenBatch(ctx context.Context, taskID string, nodes []*StrmForgeNode) error
	MarkDeletedBatch(ctx context.Context, taskID string, relPaths []string, deletedAt time.Time) error
	PurgeNodes(ctx context.Context, taskID string, relPaths []string) (int64, error)
	RestoreNodes(ctx context.Context, taskID string, relPaths []string) (int64, error)
}

// StrmForgeNodeQuery 是扫描结果列表的查询条件。
type StrmForgeNodeQuery struct {
	// Keyword 按路径模糊匹配，为空则不过滤。
	Keyword string
	// Limit 每页条数，<=0 时取默认值。
	Limit int
	// Offset 跳过的条数。
	Offset int
}

// StrmForgeNodePage 是一页扫描结果。
type StrmForgeNodePage struct {
	Nodes []*StrmForgeNode
	// Total 过滤后的总条数。
	Total int
	// HasMore 是否还有下一页。
	HasMore bool
}
