package strmforge

import (
	"encoding/json/v2"

	"litepan/internal/domain"
)

// ScanJob 的对外形态。Err 在代码里是 error，jsonv2 会把它按具体类型的导出字段
// 抖出来（errors.New 得到 {}，指针类型得到 {"Msg":"x"}），所以这里换成文本。
type scanJobJSON struct {
	TaskID      string                        `json:"task_id"`
	State       string                        `json:"state"`
	Mode        string                        `json:"mode"`
	StartedAt   any                           `json:"started_at"`
	FinishedAt  any                           `json:"finished_at,omitempty"`
	VisitedDirs int                           `json:"visited_dirs"`
	TotalFiles  int                           `json:"total_files"`
	Added       int                           `json:"added"`
	Resurrected int                           `json:"resurrected"`
	Changed     int                           `json:"changed"`
	Removed     int                           `json:"removed"`
	Pending     int                           `json:"pending_deletions"`
	Incomplete  bool                          `json:"incomplete"`
	Failures    []domain.StrmForgeScanFailure `json:"failures,omitempty"`
	Error       string                        `json:"error,omitempty"`
	Progress    *ScanProgress                 `json:"progress,omitempty"`
}

// MarshalJSON 让扫描状态自己负责序列化，调用方直接 json.Marshal 即可。
func (j ScanJob) MarshalJSON() ([]byte, error) {
	wire := scanJobJSON{
		TaskID:      j.TaskID,
		State:       j.State,
		Mode:        j.Mode,
		StartedAt:   j.StartedAt,
		VisitedDirs: j.VisitedDirs,
		TotalFiles:  j.TotalFiles,
		Added:       j.Added,
		Resurrected: j.Resurrected,
		Changed:     j.Changed,
		Removed:     j.Removed,
		Pending:     j.Pending,
		Incomplete:  j.Incomplete,
		Failures:    j.Failures,
		Progress:    j.Progress,
	}
	// 指针：扫描未结束时为 nil，序列化时才真正省略这个字段；
	// time.Time 是结构体，omitempty 对它无效。
	if j.FinishedAt != nil {
		wire.FinishedAt = *j.FinishedAt
	}
	if j.Err != nil {
		wire.Error = j.Err.Error()
	}
	return json.Marshal(wire)
}
