//go:build bugrepro

package planner_test

import (
	"context"
	"fmt"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/mediaorganize/classification"
	"litepan/internal/mediaorganize/moplan"
	"litepan/internal/mediaorganize/planner"
)

// 目标：找出「根目录散落 movie.mkv 最终 relocate 到哪里」的完整行为矩阵。
func resolveTargets(plan *moplan.Plan) map[string]string {
	byID := map[string]*moplan.PlanAction{}
	for i := range plan.Actions {
		byID[plan.Actions[i].ID] = &plan.Actions[i]
	}
	var resolve func(ref string, depth int) string
	resolve = func(ref string, depth int) string {
		if depth > 8 {
			return "?"
		}
		if !strings_HasPrefix(ref, "ref:") {
			return ref
		}
		a := byID[ref[4:]]
		if a == nil {
			return "<unresolved:" + ref + ">"
		}
		parent := resolve(a.TargetParentID, depth+1)
		if parent == "" {
			parent = "<ROOT>"
		}
		return parent + "/" + a.TargetName
	}
	out := map[string]string{}
	for i := range plan.Actions {
		a := plan.Actions[i]
		if a.Kind != moplan.ActionKindRelocate || a.SourceID == "" {
			continue
		}
		out[a.SourceName] = resolve(a.TargetParentID, 0) + "/" + a.TargetName
	}
	return out
}

func TestBug3_Matrix(t *testing.T) {
	names := []string{"阿凡达 (2009).mkv", "阿凡达.mkv"}
	structures := []string{"bare", "in_dir"}
	actions := []string{"move", "rename"}
	roots := []string{"target", ""}
	clas := []string{"off", "unmatched", "media", "region"}
	tmdbModes := []string{"hit", "miss"}

	for _, fname := range names {
		for _, structure := range structures {
			for _, at := range actions {
				for _, root := range roots {
					for _, cl := range clas {
						for _, tm := range tmdbModes {
							dirs := map[string][]domain.FileItem{}
							if structure == "bare" {
								dirs["root"] = []domain.FileItem{{ID: "f1", Name: fname}}
							} else {
								dirs["root"] = []domain.FileItem{{ID: "d1", Name: "阿凡达 (2009)", IsDir: true}}
								dirs["d1"] = []domain.FileItem{{ID: "f1", Name: fname}}
							}
							fs := &mockFS{dirs: dirs}
							tmdb := &mockTMDB{searchFn: func(_ string, y *int) []map[string]any {
								if tm == "miss" {
									return nil
								}
								return []map[string]any{{"id": 19995, "title": "阿凡达", "release_date": "2009-12-18"}}
							}}
							cfg := planner.TaskConfig{
								TargetDirectoryID: "root", TargetRootID: root, ActionType: at,
								UseTMDB: true, Recursive: true,
							}
							p := planner.New(context.Background(), fs, 1, cfg,
								planner.Settings{"mo_tmdb_api_key": "k"}, "task", tmdb, func(string) {}, nil, func() error { return nil })
							switch cl {
							case "off":
							case "unmatched":
								p.SetClassificationEnhancer(classificationStub{available: true, decision: classification.Decision{Applied: true, Matched: false, DegradedReason: "tmdb_detail_unavailable"}})
							case "media":
								p.SetClassificationEnhancer(classifyByType(true))
							case "region":
								p.SetClassificationEnhancer(classificationFnStub{available: true, fn: func(req classification.Request) classification.Decision {
									if req.TMDBID == "" {
										return classification.Decision{Applied: true, DegradedReason: "tmdb_detail_unavailable"}
									}
									return classification.Decision{Applied: true, Matched: true, Category: "国产", RelativeSegments: []string{"电影", "国产"}}
								}})
							}
							plan, err := p.Build()
							if err != nil {
								continue
							}
							for _, dst := range resolveTargets(plan) {
								label := fmt.Sprintf("file=%-20s struct=%-6s action=%-6s root=%-6q class=%-9s tmdb=%-4s", fname, structure, at, root, cl, tm)
								flag := "  "
								if strings_HasPrefix(dst, "/") || strings_HasPrefix(dst, "<") {
									flag = "!!"
								}
								t.Logf("%s %s => %s", flag, label, dst)
							}
						}
					}
				}
			}
		}
	}
}

func strings_Trim(s string) string { return strings_TrimSpace(s) }
func strings_HasPrefix(s, p string) bool { return strings_HasPrefixImpl(s, p) }
func strings_LastIndexSlash(s string) int { return strings_LastIndex(s, "/") }
