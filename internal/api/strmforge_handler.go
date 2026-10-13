package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"litepan/internal/domain"
	"litepan/internal/strmforge"
)

// StrmForgeHandler 是 STRM 映射的 HTTP 处理端，独立于上游的 Handler，
// 避免往通用 Handler 里继续堆字段与方法。
type StrmForgeHandler struct {
	svc *strmforge.Service
}

// NewStrmForgeHandler 缺少依赖时直接 panic：装配错误应当在启动阶段暴露。
func NewStrmForgeHandler(svc *strmforge.Service) *StrmForgeHandler {
	if svc == nil {
		panic("api: strmforge service 不能为空")
	}
	return &StrmForgeHandler{svc: svc}
}

// Register 注册 STRM 映射的路由。调用方需自行挂到需要鉴权的分组下。
func (h *StrmForgeHandler) Register(r chi.Router) {
	r.Route("/strmforge", func(r chi.Router) {
		r.Get("/tasks", h.listTasks)
		r.Post("/tasks", h.createTask)
		r.Get("/tasks/{id}/nodes", h.listNodes)
		r.Get("/tasks/{id}/deletions", h.listDeletions)
		r.Post("/tasks/{id}/scan", h.startScan)
		r.Get("/tasks/{id}/scan", h.getScan)
		r.Post("/tasks/{id}/deletions/confirm", h.confirmDeletions)
		r.Post("/tasks/{id}/deletions/restore", h.restoreDeletions)
		r.Delete("/tasks/{id}", h.deleteTask)
	})
}

type strmForgeTaskDTO struct {
	ID                string                      `json:"id,omitempty"`
	TaskName          string                      `json:"task_name"`
	AccountID         int64                       `json:"account_id"`
	SourceDirectory   string                      `json:"source_directory"`
	SourceDirectoryID string                      `json:"source_directory_id"`
	MaxDepth          int                         `json:"max_depth"`
	Status            string                      `json:"status"`
	CreatedAt         string                      `json:"created_at,omitempty"`
	UpdatedAt         string                      `json:"updated_at,omitempty"`
	LastScanAt        string                      `json:"last_scan_at,omitempty"`
	LastScanResult    *domain.StrmForgeScanResult `json:"last_scan_result,omitempty"`
}

type strmForgeTaskCreateDTO struct {
	TaskName          string `json:"task_name"`
	AccountID         int64  `json:"account_id"`
	SourceDirectory   string `json:"source_directory"`
	SourceDirectoryID string `json:"source_directory_id"`
	// MaxDepth 为 0 时由服务端补默认值。
	MaxDepth int `json:"max_depth"`
}

type strmForgeNodeDTO struct {
	RelPath      string `json:"rel_path"`
	ParentPath   string `json:"parent_path"`
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	ModifiedTime string `json:"modified_time,omitempty"`
	LastSeenAt   string `json:"last_seen_at,omitempty"`
	DeletedAt    string `json:"deleted_at,omitempty"`
}

type strmForgeNodePageDTO struct {
	Nodes []*strmForgeNodeDTO `json:"nodes"`
	Total int                 `json:"total"`
	// NextOffset 下一页的偏移量；没有下一页时为 null。
	NextOffset *int `json:"next_offset"`
}

type strmForgeRelPathsDTO struct {
	// RelPaths 指定要处理的条目；为空时必须配合 All。
	RelPaths []string `json:"rel_paths"`
	// All 显式表示处理全部待确认条目，避免空数组被当成「全部」。
	All bool `json:"all"`
}

func (h *StrmForgeHandler) listTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := h.svc.ListTasks(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]strmForgeTaskDTO, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, toStrmForgeTaskDTO(task))
	}
	writeOK(w, out)
}

func (h *StrmForgeHandler) createTask(w http.ResponseWriter, r *http.Request) {
	var in strmForgeTaskCreateDTO
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	task, err := h.svc.CreateTask(r.Context(), &domain.StrmForgeTask{
		TaskName:          in.TaskName,
		AccountID:         in.AccountID,
		SourceDirectory:   in.SourceDirectory,
		SourceDirectoryID: in.SourceDirectoryID,
		MaxDepth:          in.MaxDepth,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, toStrmForgeTaskDTO(task))
}

func (h *StrmForgeHandler) deleteTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "id")
	if err := h.svc.DeleteTask(r.Context(), taskID); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"id": taskID})
}

// listNodes 扫描结果分页列表，一个文件一行。
func (h *StrmForgeHandler) listNodes(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 0)
	offset := queryInt(r, "offset", 0)
	page, err := h.svc.ListNodes(r.Context(), chi.URLParam(r, "id"), domain.StrmForgeNodeQuery{
		Keyword: r.URL.Query().Get("q"),
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	out := strmForgeNodePageDTO{Total: page.Total, Nodes: make([]*strmForgeNodeDTO, 0, len(page.Nodes))}
	for _, node := range page.Nodes {
		item := &strmForgeNodeDTO{
			RelPath:    node.RelPath,
			ParentPath: node.ParentPath,
			Name:       node.Name,
			Size:       node.Size,
		}
		if !node.ModifiedTime.IsZero() {
			item.ModifiedTime = FormatAPITime(node.ModifiedTime)
		}
		if !node.LastSeenAt.IsZero() {
			item.LastSeenAt = FormatAPITime(node.LastSeenAt)
		}
		out.Nodes = append(out.Nodes, item)
	}
	if page.HasMore {
		next := offset + len(page.Nodes)
		out.NextOffset = &next
	}
	writeOK(w, out)
}

// listDeletions 待确认删除列表：本次扫描没读到的文件。
func (h *StrmForgeHandler) listDeletions(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.svc.ListPendingDeletions(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]*strmForgeNodeDTO, 0, len(nodes))
	for _, node := range nodes {
		item := &strmForgeNodeDTO{
			RelPath:    node.RelPath,
			ParentPath: node.ParentPath,
			Name:       node.Name,
			Size:       node.Size,
		}
		if !node.ModifiedTime.IsZero() {
			item.ModifiedTime = FormatAPITime(node.ModifiedTime)
		}
		if !node.DeletedAt.IsZero() {
			item.DeletedAt = FormatAPITime(node.DeletedAt)
		}
		out = append(out, item)
	}
	writeOK(w, out)
}

// startScan 触发后台扫描，立即返回；进度轮询 GET 同路径。
func (h *StrmForgeHandler) startScan(w http.ResponseWriter, r *http.Request) {
	job, err := h.svc.StartScan(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, job)
}

func (h *StrmForgeHandler) getScan(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "id")
	if _, err := h.svc.GetTask(r.Context(), taskID); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, h.svc.ScanStatus(taskID))
}

// confirmDeletions 物理删除用户确认的墓碑行。
// rel_paths 与 all 二者必须至少给一个，否则拒绝。
func (h *StrmForgeHandler) confirmDeletions(w http.ResponseWriter, r *http.Request) {
	var in strmForgeRelPathsDTO
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	removed, err := h.svc.ConfirmDeletions(r.Context(), chi.URLParam(r, "id"), in.RelPaths, in.All)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"removed": removed})
}

// restoreDeletions 把误报的墓碑恢复成活跃。与 confirmDeletions 同样要求显式意图。
func (h *StrmForgeHandler) restoreDeletions(w http.ResponseWriter, r *http.Request) {
	var in strmForgeRelPathsDTO
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	restored, err := h.svc.RestoreDeletions(r.Context(), chi.URLParam(r, "id"), in.RelPaths, in.All)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"restored": restored})
}

func toStrmForgeTaskDTO(task *domain.StrmForgeTask) strmForgeTaskDTO {
	dto := strmForgeTaskDTO{
		ID:                task.ID,
		TaskName:          task.TaskName,
		AccountID:         task.AccountID,
		SourceDirectory:   task.SourceDirectory,
		SourceDirectoryID: task.SourceDirectoryID,
		MaxDepth:          task.MaxDepth,
		Status:            task.Status,
	}
	if !task.CreatedAt.IsZero() {
		dto.CreatedAt = FormatAPITime(task.CreatedAt)
	}
	if !task.UpdatedAt.IsZero() {
		dto.UpdatedAt = FormatAPITime(task.UpdatedAt)
	}
	if !task.LastScanAt.IsZero() {
		dto.LastScanAt = FormatAPITime(task.LastScanAt)
	}
	dto.LastScanResult = task.LastScanResult
	return dto
}

// queryInt 读取整数查询参数，缺失或非法时返回 fallback。
func queryInt(r *http.Request, name string, fallback int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}
