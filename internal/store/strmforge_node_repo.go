package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"litepan/internal/domain"
)

type strmForgeNodeRepo struct{ db *DB }

const selectStrmForgeNodeCols = `SELECT task_id, rel_path, parent_path, name, size, modified_time,
       is_active, last_seen_at, deleted_at
FROM strmforge_nodes`

func (r *strmForgeNodeRepo) ListActiveNodes(ctx context.Context, taskID string) ([]*domain.StrmForgeNode, error) {
	return r.listNodes(ctx, selectStrmForgeNodeCols+` WHERE task_id=? AND is_active=1`, taskID)
}

func (r *strmForgeNodeRepo) ListDeletedNodes(ctx context.Context, taskID string) ([]*domain.StrmForgeNode, error) {
	return r.listNodes(ctx, selectStrmForgeNodeCols+` WHERE task_id=? AND is_active=0 ORDER BY deleted_at DESC, rel_path`, taskID)
}

func (r *strmForgeNodeRepo) listNodes(ctx context.Context, query, taskID string) ([]*domain.StrmForgeNode, error) {
	rows, err := r.db.read.QueryContext(ctx, query, taskID)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	var out []*domain.StrmForgeNode
	for rows.Next() {
		node, err := scanStrmForgeNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	return out, wrapDB(rows.Err())
}

func (r *strmForgeNodeRepo) CountActiveNodes(ctx context.Context, taskID string) (int, error) {
	var n int
	err := r.db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM strmforge_nodes WHERE task_id=? AND is_active=1`, taskID).Scan(&n)
	return n, wrapDB(err)
}

// ListNodesPage 分页列出扫描结果，只含活跃节点；Keyword 对路径做模糊匹配。
func (r *strmForgeNodeRepo) ListNodesPage(ctx context.Context, taskID string, query domain.StrmForgeNodeQuery) (*domain.StrmForgeNodePage, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = defaultNodePageSize
	}
	where := ` WHERE task_id=? AND is_active=1`
	args := []any{taskID}
	if keyword := strings.TrimSpace(query.Keyword); keyword != "" {
		where += ` AND rel_path LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(keyword)+"%")
	}

	var total int
	if err := r.db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM strmforge_nodes`+where, args...).Scan(&total); err != nil {
		return nil, wrapDB(err)
	}

	// 多取一条用来判断还有没有下一页，不用再查一次总数。
	pageArgs := append(append([]any{}, args...), limit+1, query.Offset)
	rows, err := r.db.read.QueryContext(ctx,
		selectStrmForgeNodeCols+where+` ORDER BY rel_path LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()

	page := &domain.StrmForgeNodePage{Total: total}
	for rows.Next() {
		node, err := scanStrmForgeNode(rows)
		if err != nil {
			return nil, wrapDB(err)
		}
		page.Nodes = append(page.Nodes, node)
	}
	if err := wrapDB(rows.Err()); err != nil {
		return nil, err
	}
	if len(page.Nodes) > limit {
		page.Nodes = page.Nodes[:limit]
		page.HasMore = true
	}
	return page, nil
}

// defaultNodePageSize 扫描结果列表的默认页大小。
const defaultNodePageSize = 100

// MarkSeenBatch 写入新增或已变化的节点；命中墓碑行时复活它（is_active=1、deleted_at=NULL）。
func (r *strmForgeNodeRepo) MarkSeenBatch(ctx context.Context, taskID string, nodes []*domain.StrmForgeNode) error {
	if len(nodes) == 0 {
		return nil
	}
	tx, err := r.db.write.BeginTx(ctx, nil)
	if err != nil {
		return wrapDB(err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO strmforge_nodes
		   (task_id, rel_path, parent_path, name, size, modified_time, is_active, last_seen_at, deleted_at)
		 VALUES (?,?,?,?,?,?,1,?,NULL)
		 ON CONFLICT(task_id, rel_path) DO UPDATE SET
		   parent_path=excluded.parent_path, name=excluded.name, size=excluded.size,
		   modified_time=excluded.modified_time, is_active=1, last_seen_at=excluded.last_seen_at,
		   deleted_at=NULL`)
	if err != nil {
		return wrapDB(err)
	}
	defer stmt.Close()
	for _, node := range nodes {
		if node.RelPath == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, taskID, node.RelPath, node.ParentPath, node.Name,
			node.Size, node.ModifiedTime.Unix(), node.LastSeenAt.Unix()); err != nil {
			return wrapDB(err)
		}
	}
	return wrapDB(tx.Commit())
}

// MarkDeletedBatch 打墓碑标记，不物理删除；网盘偶发读不全时留待用户确认。
func (r *strmForgeNodeRepo) MarkDeletedBatch(ctx context.Context, taskID string, relPaths []string, deletedAt time.Time) error {
	if len(relPaths) == 0 {
		return nil
	}
	tx, err := r.db.write.BeginTx(ctx, nil)
	if err != nil {
		return wrapDB(err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx,
		`UPDATE strmforge_nodes SET is_active=0, deleted_at=?
		 WHERE task_id=? AND rel_path=? AND is_active=1`)
	if err != nil {
		return wrapDB(err)
	}
	defer stmt.Close()
	for _, relPath := range relPaths {
		if _, err := stmt.ExecContext(ctx, deletedAt.Unix(), taskID, relPath); err != nil {
			return wrapDB(err)
		}
	}
	return wrapDB(tx.Commit())
}

func (r *strmForgeNodeRepo) PurgeNodes(ctx context.Context, taskID string, relPaths []string) (int64, error) {
	if len(relPaths) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(relPaths)+1)
	args = append(args, taskID)
	query := `DELETE FROM strmforge_nodes WHERE task_id=? AND rel_path IN (` +
		placeholders(len(relPaths)) + `)`
	for _, relPath := range relPaths {
		args = append(args, relPath)
	}
	res, err := r.db.write.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, wrapDB(err)
	}
	n, err := res.RowsAffected()
	return n, wrapDB(err)
}

// RestoreNodes 把墓碑行恢复成活跃：用户判定为误报时调用，relPaths 为空表示全部。
func (r *strmForgeNodeRepo) RestoreNodes(ctx context.Context, taskID string, relPaths []string) (int64, error) {
	query := `UPDATE strmforge_nodes SET is_active=1, deleted_at=NULL WHERE task_id=? AND is_active=0`
	args := []any{taskID}
	if len(relPaths) > 0 {
		query += ` AND rel_path IN (` + placeholders(len(relPaths)) + `)`
		for _, relPath := range relPaths {
			args = append(args, relPath)
		}
	}
	res, err := r.db.write.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, wrapDB(err)
	}
	n, err := res.RowsAffected()
	return n, wrapDB(err)
}

func scanStrmForgeNode(s rowScanner) (*domain.StrmForgeNode, error) {
	var (
		node         domain.StrmForgeNode
		modifiedTime int64
		lastSeenAt   int64
		deletedAt    sql.NullInt64
	)
	if err := s.Scan(&node.TaskID, &node.RelPath, &node.ParentPath, &node.Name, &node.Size,
		&modifiedTime, &node.IsActive, &lastSeenAt, &deletedAt); err != nil {
		return nil, wrapDB(err)
	}
	node.ModifiedTime = time.Unix(modifiedTime, 0)
	node.LastSeenAt = time.Unix(lastSeenAt, 0)
	if deletedAt.Valid {
		node.DeletedAt = time.Unix(deletedAt.Int64, 0)
	}
	return &node, nil
}
