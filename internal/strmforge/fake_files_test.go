package strmforge_test

import (
	"context"
	"time"

	"litepan/internal/domain"
)

// fakeFiles 用内存目录树替身 FileLister，按 parentID 精确返回条目。
type fakeFiles struct {
	tree    map[string][]domain.FileItem
	calls   []string
	failOn  map[string]error
	refresh []bool
	blocked string
}

func newFakeFiles() *fakeFiles {
	return &fakeFiles{tree: make(map[string][]domain.FileItem), failOn: make(map[string]error)}
}

// putDir 写入一个目录下的条目；items 为 nil 表示空目录。
func (f *fakeFiles) putDir(dirID string, items ...domain.FileItem) *fakeFiles {
	f.tree[dirID] = items
	return f
}

func (f *fakeFiles) fail(dirID string, err error) *fakeFiles {
	f.failOn[dirID] = err
	return f
}

func (f *fakeFiles) file(id, name string, size int64, modified time.Time) domain.FileItem {
	return domain.FileItem{ID: id, Name: name, Size: size, ModTime: modified}
}

func (f *fakeFiles) dir(id, name string) domain.FileItem {
	return domain.FileItem{ID: id, Name: name, IsDir: true}
}

func (f *fakeFiles) List(ctx context.Context, _ int64, parentID string, forceRefresh bool) ([]domain.FileItem, error) {
	f.calls = append(f.calls, parentID)
	f.refresh = append(f.refresh, forceRefresh)
	if parentID == f.blocked {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err, ok := f.failOn[parentID]; ok {
		return nil, err
	}
	return f.tree[parentID], nil
}

// blockDir 让指定目录的列举阻塞到 ctx 被取消，用于确定性地制造「扫描整体失败」。
func (f *fakeFiles) blockDir(dirID string) *fakeFiles {
	f.blocked = dirID
	return f
}
