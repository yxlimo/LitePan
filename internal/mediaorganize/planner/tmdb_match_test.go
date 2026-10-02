package planner

import (
	"context"
	"encoding/json"
	"testing"
)

type fallbackTMDBStub int

func (*fallbackTMDBStub) ValidateConnection(context.Context) bool { return true }

func (s *fallbackTMDBStub) Search(context.Context, string, *int, string) ([]json.RawMessage, error) {
	*s++
	return []json.RawMessage{json.RawMessage(`{"id":1001,"title":"测试电影","release_date":"2010-01-01"}`)}, nil
}

func (*fallbackTMDBStub) Lookup(context.Context, string, string) (json.RawMessage, error) {
	return nil, nil
}
func (*fallbackTMDBStub) FetchTVSeasons(context.Context, string) ([]json.RawMessage, error) {
	return nil, nil
}

type trailingNumberTMDBStub struct {
	queries []string
}

type aliasTMDBStub struct{}

func (*aliasTMDBStub) ValidateConnection(context.Context) bool { return true }

func (*aliasTMDBStub) Search(_ context.Context, query string, _ *int, mediaType string) ([]json.RawMessage, error) {
	if mediaType == "tv" && query == "海贼王" {
		return []json.RawMessage{json.RawMessage(`{"id":37854,"name":"航海王","original_name":"ワンピース","first_air_date":"1999-10-20"}`)}, nil
	}
	return nil, nil
}

func (*aliasTMDBStub) Lookup(context.Context, string, string) (json.RawMessage, error) {
	return nil, nil
}

func (*aliasTMDBStub) FetchTVSeasons(context.Context, string) ([]json.RawMessage, error) {
	return nil, nil
}

func (*trailingNumberTMDBStub) ValidateConnection(context.Context) bool { return true }

func (s *trailingNumberTMDBStub) Search(_ context.Context, query string, _ *int, mediaType string) ([]json.RawMessage, error) {
	s.queries = append(s.queries, mediaType+":"+query)
	switch mediaType + ":" + query {
	case "movie:测试电影":
		return []json.RawMessage{json.RawMessage(`{"id":2001,"title":"测试电影","release_date":"2020-01-01"}`)}, nil
	case "tv:测试剧":
		return []json.RawMessage{json.RawMessage(`{"id":3001,"name":"测试剧","first_air_date":"2020-01-01"}`)}, nil
	default:
		return nil, nil
	}
}

func (*trailingNumberTMDBStub) Lookup(context.Context, string, string) (json.RawMessage, error) {
	return nil, nil
}

func (*trailingNumberTMDBStub) FetchTVSeasons(context.Context, string) ([]json.RawMessage, error) {
	return nil, nil
}

// 年份是加权项不是红线：声明 2020、TMDB 只有 2010 版时仍应采用匹配，
// 并用 TMDB 真实年份纠正目录名，避免「匹配失败 -> 名字被改成错年份 -> 重复失败」的死循环。
func TestMatchTMDBForGroupAcceptsYearMismatchAndUsesTMDBYear(t *testing.T) {
	year := 2020
	var tmdb fallbackTMDBStub
	p := &Planner{ctx: context.Background(), tmdb: &tmdb, log: func(string) {}}

	result, err := p.matchTMDBForGroup(groupKey{mediaKind: "movie", title: "测试电影", year: year, hasYear: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.tmdbID != "1001" {
		t.Fatalf("唯一强标题匹配不应因年份不符被丢弃，实际 tmdb id=%q", result.tmdbID)
	}
	if result.yearMismatch != true {
		t.Fatalf("应标记 yearMismatch，实际 %+v", result)
	}
	if result.year == nil || *result.year != 2010 {
		t.Fatalf("目录名应使用 TMDB 真实年份 2010，实际 %v", result.year)
	}
	// 置信度取「年份一致(0.9)」与「年份不符(0.65)」之间的中间档。
	if result.confidence != 0.75 {
		t.Fatalf("年份不符时置信度应取中间档 0.75，实际 %v", result.confidence)
	}
	if tmdb != 1 {
		t.Fatalf("第一次查询即可命中放宽匹配，不应重复查询，实际 %d 次", tmdb)
	}
}

// 年份一致时保持原有置信度与查询次数不变。
func TestMatchTMDBForGroupKeepsExactYearConfidence(t *testing.T) {
	year := 2010
	var tmdb fallbackTMDBStub
	p := &Planner{ctx: context.Background(), tmdb: &tmdb, log: func(string) {}}

	result, err := p.matchTMDBForGroup(groupKey{mediaKind: "movie", title: "测试电影", year: year, hasYear: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.tmdbID != "1001" {
		t.Fatalf("年份一致时应命中，实际 tmdb id=%q", result.tmdbID)
	}
	if result.yearMismatch {
		t.Fatalf("年份一致时不应标记 yearMismatch: %+v", result)
	}
	if result.confidence != 0.9 {
		t.Fatalf("年份一致时置信度应为 0.9，实际 %v", result.confidence)
	}
	if result.year == nil || *result.year != 2010 {
		t.Fatalf("年份应保持 2010，实际 %v", result.year)
	}
}

func TestMatchTMDBForGroupAcceptsTMDBAliasWithExactYear(t *testing.T) {
	year := 1999
	p := &Planner{ctx: context.Background(), tmdb: &aliasTMDBStub{}, log: func(string) {}}
	result, err := p.matchTMDBForGroup(groupKey{mediaKind: "tv", title: "海贼王", year: year, hasYear: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.tmdbID != "37854" || result.tmdbTitle != "航海王" {
		t.Fatalf("海贼王 (1999) 应通过 TMDB 别名搜索命中航海王，实际=%+v", result)
	}
}

func TestTrailingNumberFallbackOnlyAppliesToTV(t *testing.T) {
	movieTMDB := &trailingNumberTMDBStub{}
	moviePlanner := &Planner{ctx: context.Background(), tmdb: movieTMDB, log: func(string) {}}
	movieResult, err := moviePlanner.matchTMDBForGroup(groupKey{mediaKind: "movie", title: "测试电影2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if movieResult.tmdbID != "" {
		t.Fatalf("电影尾数字不应被剔除后命中前作，实际 tmdb id=%q", movieResult.tmdbID)
	}
	for _, query := range movieTMDB.queries {
		if query == "movie:测试电影" {
			t.Fatalf("电影不应搜索剔除尾数字后的标题，查询=%v", movieTMDB.queries)
		}
	}

	tvTMDB := &trailingNumberTMDBStub{}
	tvPlanner := &Planner{ctx: context.Background(), tmdb: tvTMDB, log: func(string) {}}
	tvResult, err := tvPlanner.matchTMDBForGroup(groupKey{mediaKind: "tv", title: "测试剧2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tvResult.tmdbID != "3001" {
		t.Fatalf("电视剧应保留尾数字季号回退，实际 tmdb id=%q", tvResult.tmdbID)
	}
	if season, ok := tvResult.inferredSeason.(int); !ok || season != 2 {
		t.Fatalf("电视剧尾数字应推断为第 2 季，实际=%v", tvResult.inferredSeason)
	}
}
