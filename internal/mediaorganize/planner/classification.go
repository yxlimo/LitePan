package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"litepan/internal/mediaorganize/classification"
	"litepan/internal/mediaorganize/rules"
)

// classifyGroup 调用分类增强器，决定作品目录相对 move 目标根的层级。
//
// Applied 的语义是「分类器确实参与并给出了决策」：增强器不可用/出错时返回
// Applied=false（分类器没参与），由调用方回退到源目录层级 + 媒体类型兜底；
// 分类器参与但归不了类时返回 Applied=true, Matched=false。
// 两种情况都不再把作品目录直接塞到移动根目录。
func (p *Planner) classifyGroup(mediaType, tmdbID string, raw map[string]any) classification.Decision {
	if p.actionType != "move" {
		return classification.Decision{}
	}
	if p.classification == nil || !p.classification.Available() {
		// 增强器拿不到时也要留下痕迹，不能静默返回全零 Decision。
		p.recordClassificationDegraded("classification_unavailable")
		p.log("[计划] 分类整理不可用，将回退源目录层级并按媒体类型兜底归类")
		return classification.Decision{
			MediaKind:      mediaType,
			DegradedReason: "classification_unavailable",
		}
	}
	decision, err := p.classification.Classify(p.ctx, classification.Request{
		MediaType: mediaType,
		TMDBID:    tmdbID,
		Raw:       raw,
		Loader:    p,
	})
	if decision.MediaKind == "" {
		decision.MediaKind = mediaType
	}
	if err != nil {
		reason := "classification_unavailable"
		if !errors.Is(err, classification.ErrUnavailable) {
			reason = "classification_error"
			p.log(fmt.Sprintf("[计划] 分类整理降级：%v", err))
		}
		p.recordClassificationDegraded(reason)
		return classification.Decision{MediaKind: mediaType, DegradedReason: reason}
	}
	if decision.Applied {
		if decision.Matched {
			p.log(fmt.Sprintf("[计划] 分类命中：%s -> %s", decision.Template, strings.Join(decision.RelativeSegments, "/")))
		} else {
			p.recordClassificationDegraded(decision.DegradedReason)
			p.log(fmt.Sprintf("[计划] 无法归类，按媒体类型兜底：%s", decision.DegradedReason))
		}
	}
	return decision
}

// Lookup 实现 classification.DetailLoader，统一复用 Planner 的 TMDB 请求间隔。
func (p *Planner) Lookup(ctx context.Context, tmdbID string, mediaType string) (json.RawMessage, error) {
	if p.tmdb == nil {
		return nil, fmt.Errorf("TMDB 客户端不可用")
	}
	payload, err := p.tmdb.Lookup(ctx, tmdbID, mediaType)
	p.sleepTMDB()
	return payload, err
}

func (p *Planner) recordClassificationDegraded(reason string) {
	if p.diagnostics == nil {
		return
	}
	count, _ := p.diagnostics["classification_degraded_count"].(int)
	p.diagnostics["classification_degraded_count"] = count + 1
	if strings.TrimSpace(reason) != "" {
		reasons, _ := p.diagnostics["classification_degraded_reasons"].(map[string]int)
		if reasons == nil {
			reasons = make(map[string]int)
		}
		reasons[reason]++
		p.diagnostics["classification_degraded_reasons"] = reasons
	}
}

// mediaKindFallbackSegment 分类不可用/未命中时的媒体类型兜底层。
// 返回空串表示这个媒体类型不适合兜底（此时才允许作品目录直接挂在移动根下）。
func mediaKindFallbackSegment(mediaKind string) string {
	switch strings.ToLower(strings.TrimSpace(mediaKind)) {
	case "movie", "电影", "影片":
		return "电影"
	case "tv", "电视剧", "剧集", "连续剧", "剧":
		return "电视剧"
	default:
		return ""
	}
}

// resolveWorkDirParent 决定作品目录的父目录。
//
// 核心修复：分类「跑了但没命中」不能等同于「应该放在移动根目录」——
// 移动根目录往往是用户配的网盘根，静默降级到那里就是用户看到的「落到任务根目录」。
//
//	分类命中        -> 用分类给出的相对层级
//	分类跑了没命中  -> 按媒体类型兜底（movie -> 电影，tv -> 电视剧）
//	分类没参与      -> 保留源目录已有的分类层级（用户自己的意图）；
//	                   仅当是散落文件且源目录也没有分类层级时才按媒体类型兜底
//
// 第二个返回值表示最终用的是媒体类型兜底层，用于登记 needs_classification 诊断。
func (p *Planner) resolveWorkDirParent(key groupKey, items []batchEntry, decision classification.Decision) (string, bool) {
	mediaKind := decision.MediaKind
	if mediaKind == "" {
		mediaKind = key.mediaKind
	}
	if decision.Applied {
		if decision.Matched {
			return p.classificationParentRef(decision), false
		}
		// 分类器明确表示「归不了类」：不镜像源目录，直接按媒体类型兜底。
		if seg := mediaKindFallbackSegment(mediaKind); seg != "" {
			return p.ensureDirAction(p.moveRootRef(), seg), true
		}
		return p.moveRootRef(), false
	}
	// 分类器没参与：源目录里已有的分类层级是用户自己的意图，先保留它。
	parentRef := p.buildTargetCategoryParentRef(p.categoryAncestors(key, items))
	// 散落文件（自己没有作品目录）且源目录也没有分类层级时，作品目录会直接挂在
	// 移动根上，观感上等于「扔到网盘根」，这里按媒体类型兜底补一层。
	if key.dirID == "" && parentRef == p.moveRootRef() {
		if seg := mediaKindFallbackSegment(mediaKind); seg != "" {
			return p.ensureDirAction(parentRef, seg), true
		}
	}
	return parentRef, false
}

func (p *Planner) moveRootRef() string {
	if p.targetRootID != "" {
		return p.targetRootID
	}
	return p.parentID
}

// resolvePromotedMoveTarget 解析「把电影从剧集目录里提升出来」这条路径的目标父目录。
// 与 resolveWorkDirParent 保持同一套语义：分类参与了但没命中时同样不能直接落到移动根目录。
// 第二个返回值表示是否用了媒体类型兜底层。
func (p *Planner) resolvePromotedMoveTarget(
	decision classification.Decision,
	mediaKind string,
	sampleAncestors []rules.Ancestor,
	dirID string,
) (string, bool) {
	if !decision.Applied {
		return p.resolvePromotedMovieTargetParent(sampleAncestors, dirID), false
	}
	if decision.Matched {
		return p.classificationParentRef(decision), false
	}
	if seg := mediaKindFallbackSegment(mediaKind); seg != "" {
		return p.ensureDirAction(p.moveRootRef(), seg), true
	}
	return p.moveRootRef(), false
}

func (p *Planner) classificationParentRef(decision classification.Decision) string {
	parentRef := p.moveRootRef()
	if !decision.Matched {
		return parentRef
	}
	for _, segment := range decision.RelativeSegments {
		name := strings.TrimSpace(segment)
		if name != "" {
			parentRef = p.ensureDirAction(parentRef, name)
		}
	}
	return parentRef
}

func classificationMetadata(decision classification.Decision) map[string]any {
	if !decision.Applied {
		return nil
	}
	return map[string]any{
		"classification_applied":         true,
		"classification_matched":         decision.Matched,
		"classification_template":        decision.Template,
		"classification_category":        decision.Category,
		"classification_relative_path":   strings.Join(decision.RelativeSegments, "/"),
		"classification_evidence":        decision.Evidence,
		"classification_degraded_reason": decision.DegradedReason,
	}
}

// recordNeedsClassification 登记「只进了兜底分类目录」的组。
// 分类降级现在会兜底建目录，不再静默落到移动根，但用户仍需要知道哪些条目
// 靠兜底归了类、值得回头补规则或手工整理。
func (p *Planner) recordNeedsClassification(key groupKey, items []batchEntry, decision classification.Decision, usedFallback bool) {
	if !usedFallback {
		return
	}
	mediaKind := decision.MediaKind
	if mediaKind == "" {
		mediaKind = key.mediaKind
	}
	entry := map[string]any{
		"group_uid":  groupUIDOf(key),
		"media_kind": mediaKind,
		"dir_id":     key.dirID,
		"dir_name":   key.dirName,
		"title":      key.title,
		"count":      len(items),
		"reason":     "未能按分类规则归类，已放入媒体类型兜底目录",
	}
	if decision.DegradedReason != "" {
		entry["degraded_reason"] = decision.DegradedReason
	}
	if key.hasYear {
		entry["year"] = key.year
	}
	entries, _ := p.diagnostics["needs_classification"].([]map[string]any)
	p.diagnostics["needs_classification"] = append(entries, entry)
}
