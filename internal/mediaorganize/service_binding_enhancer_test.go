package mediaorganize

import (
	"context"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/mediaorganize/classification"
	"litepan/internal/mediaorganize/moplan"
	"litepan/internal/mediaorganize/planner"
	"litepan/internal/mediaorganize/recognition"
)

// bug1 回归：人工匹配后的局部重规划（Service.newPlanner）必须和 Build() 走同一套增强器注入。
// 之前 replanMatchedGroup 自己 new planner 且没注分类增强器，导致 classifyGroup 返回全零 Decision：
// 作品目录直接挂在 move 目标根（观感上就是「落到任务根目录」），metadata 里也没有任何 classification_* 字段。

type bindingStubFileService struct {
	dirs map[string][]domain.FileItem
}

func (s bindingStubFileService) List(_ context.Context, _ int64, parentID string, _ bool) ([]domain.FileItem, error) {
	return append([]domain.FileItem(nil), s.dirs[parentID]...), nil
}

type bindingStubClassification struct {
	available bool
	calls     int
	decision  classification.Decision
}

func (s *bindingStubClassification) Available() bool { return s.available }

func (s *bindingStubClassification) Classify(context.Context, classification.Request) (classification.Decision, error) {
	s.calls++
	return s.decision, nil
}

type bindingStubRecognition struct{ calls int }

func (s *bindingStubRecognition) Available() bool { return true }

func (s *bindingStubRecognition) Enhance(context.Context, recognition.BatchRequest) (recognition.BatchResult, error) {
	s.calls++
	return recognition.BatchResult{}, nil
}

func TestNewPlannerInjectsEnhancersForManualMatchReplan(t *testing.T) {
	classifier := &bindingStubClassification{
		available: true,
		decision: classification.Decision{
			Applied: true, Matched: true, Template: "media", Category: "电影",
			RelativeSegments: []string{"电影"},
		},
	}
	recognizer := &bindingStubRecognition{}
	svc := NewService(ServiceOptions{
		Files: bindingStubFileService{dirs: map[string][]domain.FileItem{
			"root":  {{ID: "movie", Name: "阿凡达 (2009)", IsDir: true}},
			"movie": {{ID: "f1", Name: "阿凡达.2009.mkv"}},
		}},
		Classification: classifier,
		Recognition:    recognizer,
	})

	cfg := map[string]any{
		"action_type":    "move",
		"target_dir_id":  "root",
		"target_root_id": "target",
		"use_tmdb":       true,
		"recursive":      true,
	}
	task := &domain.MediaOrganizeTask{ID: "task-1", AccountID: 1}

	p := svc.newPlanner(context.Background(), "task-1", task, cfg, nil, nil, nil, nil)
	if p == nil {
		t.Fatal("newPlanner 返回 nil")
	}
	plan, err := p.ReplanMatchedGroup(manualMatchGroupFixture(), tmdbDetailFixture())
	if err != nil {
		t.Fatalf("ReplanMatchedGroup: %v", err)
	}

	if classifier.calls == 0 {
		t.Fatal("手动匹配重规划没有调用分类增强器：增强器未注入 newPlanner 建的 planner")
	}

	// 作品目录必须落在 电影/ 下，而不是 move 目标根。
	category := findEnsureDirByName(t, plan.Actions, "电影")
	if category.TargetParentID != "target" {
		t.Fatalf("电影分类目录应挂在 move 目标根，实际 %q", category.TargetParentID)
	}
	workDir := findWorkDirAction(t, plan.Actions)
	if workDir.TargetParentID != "ref:"+category.ID {
		t.Fatalf("作品目录应挂在 电影/ 下，实际父目录 %q", workDir.TargetParentID)
	}
	if workDir.Metadata["classification_applied"] != true {
		t.Fatalf("作品目录缺少 classification_applied: %+v", workDir.Metadata)
	}
	if workDir.Metadata["classification_matched"] != true {
		t.Fatalf("作品目录缺少 classification_matched: %+v", workDir.Metadata)
	}
	if workDir.Metadata["classification_category"] != "电影" {
		t.Fatalf("作品目录缺少 classification_category: %+v", workDir.Metadata)
	}
}

// 增强器未配置时不能崩：应退化为无分类，但依然要把作品目录建在分类兜底层里
// （兜底逻辑见 classificationParentRef / ensureWorkDirAction）。
func TestNewPlannerWithoutEnhancersDoesNotPanic(t *testing.T) {
	svc := NewService(ServiceOptions{
		Files: bindingStubFileService{dirs: map[string][]domain.FileItem{
			"root":  {{ID: "movie", Name: "阿凡达 (2009)", IsDir: true}},
			"movie": {{ID: "f1", Name: "阿凡达.2009.mkv"}},
		}},
	})
	cfg := map[string]any{
		"action_type":    "move",
		"target_dir_id":  "root",
		"target_root_id": "target",
		"use_tmdb":       true,
		"recursive":      true,
	}
	p := svc.newPlanner(context.Background(), "task-1", &domain.MediaOrganizeTask{ID: "task-1", AccountID: 1}, cfg, nil, nil, nil, nil)
	plan, err := p.ReplanMatchedGroup(manualMatchGroupFixture(), tmdbDetailFixture())
	if err != nil {
		t.Fatalf("ReplanMatchedGroup: %v", err)
	}
	workDir := findWorkDirAction(t, plan.Actions)
	if workDir.ID == "" {
		t.Fatal("未配置增强器时也应生成作品目录")
	}
}

func manualMatchGroupFixture() planner.ManualMatchGroup {
	return planner.ManualMatchGroup{
		GroupUID:  "movie|movie|阿凡达 (2009)|阿凡达",
		MediaKind: "movie",
		DirID:     "movie",
		DirName:   "阿凡达 (2009)",
		Title:     "阿凡达",
	}
}

func tmdbDetailFixture() map[string]any {
	return map[string]any{"id": 19995, "title": "阿凡达", "release_date": "2009-12-18"}
}

func dumpActionChain(actions []moplan.PlanAction) string {
	out := ""
	for _, a := range actions {
		out += "\n  [" + a.ID + "] " + a.Kind + " " + a.TargetParentID + "/" + a.TargetName
	}
	return out
}

func findEnsureDirByName(t *testing.T, actions []moplan.PlanAction, name string) moplan.PlanAction {
	t.Helper()
	for _, a := range actions {
		if a.Kind == moplan.ActionKindEnsureDir && a.TargetName == name {
			return a
		}
	}
	t.Fatalf("计划中没有 ensure_dir %q: %s", name, dumpActionChain(actions))
	return moplan.PlanAction{}
}

func findWorkDirAction(t *testing.T, actions []moplan.PlanAction) moplan.PlanAction {
	t.Helper()
	for _, a := range actions {
		if a.Metadata["is_work_dir"] == true {
			return a
		}
	}
	t.Fatalf("计划中没有作品目录: %s", dumpActionChain(actions))
	return moplan.PlanAction{}
}
