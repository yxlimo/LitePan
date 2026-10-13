package store

import "litepan/internal/domain"

// StrmForgeRepos 聚合 STRM 映射所需的仓储。
// 单独定义而不挂在上游 Store 上，避免持续改动那个文件。
type StrmForgeRepos struct {
	Tasks domain.StrmForgeTaskRepository
	Nodes domain.StrmForgeNodeRepository
}

// NewStrmForgeRepos 基于已打开的 DB 构造 STRM 映射的仓储集合。
func NewStrmForgeRepos(db *DB) *StrmForgeRepos {
	return &StrmForgeRepos{
		Tasks: &strmForgeTaskRepo{db: db},
		Nodes: &strmForgeNodeRepo{db: db},
	}
}
