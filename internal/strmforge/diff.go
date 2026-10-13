package strmforge

import (
	"sort"
	"time"

	"litepan/internal/domain"
)

// SeenNode 是本轮扫描实际读到的文件节点。
type SeenNode struct {
	RelPath      string
	ParentPath   string
	Name         string
	Size         int64
	ModifiedTime time.Time
}

// NodeDiff 是一次扫描的差异结果。
// Removed 只标记墓碑，等用户确认后才物理删除。
type NodeDiff struct {
	Added       []string
	Resurrected []string
	Changed     []string
	Removed     []string
	// Incomplete 为真时不做删除判定：本次读到的节点数不足库中一半，
	// 判定为拉取不完整，避免把没读到的节点误判为已删除。
	Incomplete bool
}

// DiffNodes 把本轮读到的节点与库中已有节点做集合差。
// 墓碑行不参与 added/removed 计算：它们已经在等用户确认，
// 本轮再次出现时算复活。
func DiffNodes(active map[string]*domain.StrmForgeNode, seen map[string]SeenNode) NodeDiff {
	var diff NodeDiff
	for relPath, node := range seen {
		if existing, ok := active[relPath]; ok {
			if existing.Size != node.Size || !existing.ModifiedTime.Equal(node.ModifiedTime) {
				diff.Changed = append(diff.Changed, relPath)
			}
			continue
		}
		diff.Added = append(diff.Added, relPath)
	}
	for relPath := range active {
		if _, ok := seen[relPath]; !ok {
			diff.Removed = append(diff.Removed, relPath)
		}
	}
	sort.Strings(diff.Added)
	sort.Strings(diff.Changed)
	sort.Strings(diff.Removed)
	return diff
}

// SplitResurrected 把本轮重新出现的路径从 Added 中摘出来：
// 库里存在同名墓碑行的算复活，不算新增。
func SplitResurrected(diff *NodeDiff, deleted map[string]struct{}) {
	if len(deleted) == 0 || len(diff.Added) == 0 {
		return
	}
	kept := diff.Added[:0]
	for _, relPath := range diff.Added {
		if _, ok := deleted[relPath]; ok {
			diff.Resurrected = append(diff.Resurrected, relPath)
			continue
		}
		kept = append(kept, relPath)
	}
	diff.Added = kept
	sort.Strings(diff.Resurrected)
}

// IncompleteScan 判断本次扫描是否拉取不完整。
// 沿用上游 pruneDirCache 的口径：本次覆盖不足库中一半就跳过删除判定。
func IncompleteScan(seenCount, activeCount int) bool {
	return seenCount > 0 && activeCount > 0 && seenCount*2 < activeCount
}
