// 白盒测试：domain-pure 规则不允许该目录下的文件 import internal/*，
// 所以同包测试，不导入自身包。
package domain

import (
	"encoding/json/v2"
	"errors"
	"reflect"
	"testing"
)

func sampleResult() *StrmForgeScanResult {
	return &StrmForgeScanResult{
		Mode:        "diff",
		Added:       3,
		Resurrected: 1,
		Changed:     2,
		Removed:     4,
		Pending:     4,
		Incomplete:  true,
		Failures: []StrmForgeScanFailure{
			{DirPath: "剧集/Silo", Message: "网盘限流"},
		},
		Error: errors.New("部分目录读取失败"),
	}
}

// jsonv2 直接序列化结构体即可，Error 应变成文本而不是 error 具体类型的字段。
func TestScanResultMarshalsErrorAsText(t *testing.T) {
	data, err := json.Marshal(sampleResult())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"mode":"diff","added":3,"resurrected":1,"changed":2,"removed":4,` +
		`"pending":4,"incomplete":true,"failures":[{"dir_path":"剧集/Silo","message":"网盘限流"}],` +
		`"error":"部分目录读取失败"}`
	if string(data) != want {
		t.Fatalf("序列化结果不符：\n got %s\nwant %s", data, want)
	}
}

func TestScanResultJSONRoundTrip(t *testing.T) {
	want := sampleResult()
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got StrmForgeScanResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Error == nil || got.Error.Error() != want.Error.Error() {
		t.Fatalf("Error 往返后不是 error：%v", got.Error)
	}
	// Error 是 interface，比较其余字段时先摘掉它。
	got.Error, want.Error = nil, nil
	if !reflect.DeepEqual(got, *want) {
		t.Fatalf("往返后不一致：\n got %+v\nwant %+v", got, *want)
	}
}

// Value 让结果能直接当 SQL 参数传。
func TestScanResultValueIsStorable(t *testing.T) {
	v, err := sampleResult().Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	text, ok := v.(string)
	if !ok {
		t.Fatalf("Value 返回 %T，应为 string", v)
	}
	if text == "" {
		t.Fatal("Value 不应返回空串")
	}
	// 零值写空串，避免在库里堆一串全零 JSON。
	zero := &StrmForgeScanResult{}
	v2, err := zero.Value()
	if err != nil {
		t.Fatalf("Value(零值): %v", err)
	}
	if v2 != "" {
		t.Fatalf("零值应写成空串，实际 %v", v2)
	}
}

func TestScanResultScanFromColumn(t *testing.T) {
	want := sampleResult()
	stored, err := want.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	text := stored.(string)

	for _, src := range []any{text, []byte(text)} {
		var got StrmForgeScanResult
		if err := got.Scan(src); err != nil {
			t.Fatalf("Scan(%T): %v", src, err)
		}
		if got.Error == nil || got.Error.Error() != want.Error.Error() {
			t.Fatalf("Scan(%T) 后 Error 丢失：%v", src, got.Error)
		}
		got.Error = nil
		want.Error = nil
		if !reflect.DeepEqual(got, *want) {
			t.Fatalf("Scan(%T) 结果不符：\n got %+v\nwant %+v", src, got, *want)
		}
		want.Error = errors.New("部分目录读取失败")
	}
}

// 空串与 NULL 都读成零值，用来表示「从未扫描」。
func TestScanResultScanEmptyIsZero(t *testing.T) {
	for _, src := range []any{nil, "", "   "} {
		var got StrmForgeScanResult
		if err := got.Scan(src); err != nil {
			t.Fatalf("Scan(%v): %v", src, err)
		}
		if !got.IsZero() {
			t.Fatalf("Scan(%v) 应得到零值，实际 %+v", src, got)
		}
		if got.Error != nil {
			t.Fatalf("空值不应产生 error，却得到 %v", got.Error)
		}
	}
}

func TestScanResultIsZero(t *testing.T) {
	var nilResult *StrmForgeScanResult
	if !nilResult.IsZero() {
		t.Fatal("nil 应视为零值")
	}
	if (&StrmForgeScanResult{}).IsZero() != true {
		t.Fatal("空结构体应为零值")
	}
	// 任何一个字段有值都不算零值。
	if (&StrmForgeScanResult{Mode: "diff"}).IsZero() {
		t.Fatal("有 mode 不该是零值")
	}
	if (&StrmForgeScanResult{Error: errors.New("x")}).IsZero() {
		t.Fatal("有 error 不该是零值")
	}
}
