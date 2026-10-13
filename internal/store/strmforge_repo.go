package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"

	"litepan/internal/domain"
)

type strmForgeTaskRepo struct{ db *DB }

func (r *strmForgeTaskRepo) Create(ctx context.Context, task *domain.StrmForgeTask) error {
	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	if task.Status == "" {
		task.Status = domain.StrmForgeStatusIdle
	}
	_, err := r.db.write.ExecContext(ctx,
		`INSERT INTO strmforge_tasks
		  (id, task_name, account_id, source_directory, source_directory_id, max_depth, status)
		 VALUES (?,?,?,?,?,?,?)`,
		task.ID, task.TaskName, task.AccountID, task.SourceDirectory,
		task.SourceDirectoryID, task.MaxDepth, task.Status)
	if err != nil {
		return wrapDB(err)
	}
	return nil
}

func (r *strmForgeTaskRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.write.ExecContext(ctx, `DELETE FROM strmforge_tasks WHERE id=?`, id)
	if err != nil {
		return wrapDB(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return wrapDB(err)
	}
	if n == 0 {
		return domain.Errf(domain.CodeNotFound)
	}
	return nil
}

func (r *strmForgeTaskRepo) Get(ctx context.Context, id string) (*domain.StrmForgeTask, error) {
	row := r.db.read.QueryRowContext(ctx, selectStrmForgeTaskCols+` WHERE id=?`, id)
	return scanStrmForgeTask(row)
}

func (r *strmForgeTaskRepo) List(ctx context.Context) ([]*domain.StrmForgeTask, error) {
	rows, err := r.db.read.QueryContext(ctx, selectStrmForgeTaskCols+` ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	return scanStrmForgeTaskRows(rows)
}

func (r *strmForgeTaskRepo) ListByAccount(ctx context.Context, accountID int64) ([]*domain.StrmForgeTask, error) {
	rows, err := r.db.read.QueryContext(ctx, selectStrmForgeTaskCols+` WHERE account_id=? ORDER BY created_at DESC, id DESC`, accountID)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	return scanStrmForgeTaskRows(rows)
}

// UpdateScanState 记录扫描状态与结果；lastScanAt 为零值时只更新状态与结果。
func (r *strmForgeTaskRepo) UpdateScanState(ctx context.Context, id, status string, lastScanAt time.Time, result *domain.StrmForgeScanResult) error {
	// 结果自带 Value()，直接当 SQL 参数传，不用预先编码；
	// nil 传零值结构体，Value() 会写成空串（列是 NOT NULL）。
	value := domain.StrmForgeScanResult{}
	if result != nil {
		value = *result
	}
	var (
		res  sql.Result
		qerr error
	)
	if lastScanAt.IsZero() {
		res, qerr = r.db.write.ExecContext(ctx,
			`UPDATE strmforge_tasks SET status=?, last_scan_result=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			status, value, id)
	} else {
		res, qerr = r.db.write.ExecContext(ctx,
			`UPDATE strmforge_tasks SET status=?, last_scan_at=?, last_scan_result=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			status, lastScanAt.Unix(), value, id)
	}
	if qerr != nil {
		return wrapDB(qerr)
	}
	if n, aerr := res.RowsAffected(); aerr != nil {
		return wrapDB(aerr)
	} else if n == 0 {
		return domain.Errf(domain.CodeNotFound)
	}
	return nil
}

const selectStrmForgeTaskCols = `SELECT id, task_name, account_id, source_directory, source_directory_id, max_depth, status,
       last_scan_at, last_scan_result, created_at, updated_at
FROM strmforge_tasks`

func scanStrmForgeTaskRows(rows *sql.Rows) ([]*domain.StrmForgeTask, error) {
	var out []*domain.StrmForgeTask
	for rows.Next() {
		task, err := scanStrmForgeTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, wrapDB(rows.Err())
}

func scanStrmForgeTask(s rowScanner) (*domain.StrmForgeTask, error) {
	var (
		task       domain.StrmForgeTask
		lastScan   sql.NullInt64
		lastResult domain.StrmForgeScanResult
		createdAt  sql.NullString
		updatedAt  sql.NullString
	)
	// lastResult 自带 Scan()，直接从一个 TEXT 列读进结构体。
	err := s.Scan(
		&task.ID, &task.TaskName, &task.AccountID, &task.SourceDirectory,
		&task.SourceDirectoryID, &task.MaxDepth, &task.Status, &lastScan, &lastResult,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, wrapDB(err)
	}
	if lastScan.Valid {
		task.LastScanAt = time.Unix(lastScan.Int64, 0)
	}
	// 空串读成零值，对应「从未扫描」，保持 LastScanResult 为 nil。
	if !lastResult.IsZero() {
		task.LastScanResult = &lastResult
	}
	task.CreatedAt = parseTS(createdAt)
	task.UpdatedAt = parseTS(updatedAt)
	return &task, nil
}
