package rules

import "testing"

// Bug 3-B：通用媒体目录名单必须覆盖常见的下载/临时落盘目录。
// 漏掉它们时，rename 模式下位于 /下载/ 的散落文件不会被判定为「需要建作品目录」，
// 只在原地改名，观感上就是「重命名完扔根目录」。
func TestIsGenericMediaDirCoversDownloadAndUnsortedDirs(t *testing.T) {
	for _, name := range []string{
		"下载", "download", "downloads", "temp", "tmp",
		"未分类", "其他", "未整理", "待整理", "unsorted", "incoming",
		// 原有条目不能被改坏
		"电影", "movie", "电视剧", "tv shows", "动漫", "视频",
		// 大小写与空白
		"  Movies  ", "MOVIE",
	} {
		if !IsGenericMediaDir(name) {
			t.Errorf("IsGenericMediaDir(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"阿凡达 (2009)", "Season 01", "某某的剧集", "aladdin"} {
		if IsGenericMediaDir(name) {
			t.Errorf("IsGenericMediaDir(%q) = true, want false（作品名不应被当成通用媒体目录）", name)
		}
	}
}

// Bug 3-B：collectionContainerHintRe 有裸的 "/" 分支，导致 IsCollectionContainerDir("/")
// 恒为 true。名字本身只是分隔符时并不是「装多部作品的容器」。
func TestIsCollectionContainerDirRejectsSeparatorOnlyNames(t *testing.T) {
	for _, name := range []string{"/", "//", "\\", "///", "/ ", "　", " / / "} {
		if IsCollectionContainerDir(name) {
			t.Errorf("IsCollectionContainerDir(%q) = true, want false（纯分隔符不是合集容器）", name)
		}
	}
	// 真正带分隔符的合集名仍应命中。
	if !IsCollectionContainerDir("流浪地球+超时空接触") {
		t.Errorf("含 + 的合集名应判定为合集容器")
	}
}
