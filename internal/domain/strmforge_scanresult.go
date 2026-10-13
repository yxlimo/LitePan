package domain

import (
	"bytes"
	"database/sql/driver"
	"encoding/json/v2"
	"fmt"
)

// scanResultWire 是 StrmForgeScanResult 的落库 / 对外形态。
// 单独一份是因为 Error 在代码里是 error 类型：jsonv2 会把 error 接口按具体类型的
// 导出字段抖出来（errors.New 得到 {}，指针类型得到 {"Msg":"x"}），都不是我们要的文本。
type scanResultWire struct {
	Mode        string                 `json:"mode"`
	Added       int                    `json:"added"`
	Resurrected int                    `json:"resurrected"`
	Changed     int                    `json:"changed"`
	Removed     int                    `json:"removed"`
	Pending     int                    `json:"pending"`
	Incomplete  bool                   `json:"incomplete,omitempty"`
	Failures    []StrmForgeScanFailure `json:"failures,omitempty"`
	Error       string                 `json:"error,omitempty"`
}

// MarshalJSON 让结构体自带序列化能力，调用方直接 json.Marshal 即可，
// 不必再在 store / api 各写一份转换。
func (r StrmForgeScanResult) MarshalJSON() ([]byte, error) {
	wire := scanResultWire{
		Mode:        r.Mode,
		Added:       r.Added,
		Resurrected: r.Resurrected,
		Changed:     r.Changed,
		Removed:     r.Removed,
		Pending:     r.Pending,
		Incomplete:  r.Incomplete,
		Failures:    r.Failures,
	}
	if r.Error != nil {
		wire.Error = r.Error.Error()
	}
	return json.Marshal(wire)
}

// UnmarshalJSON 与 MarshalJSON 对称：文本重新还原成 error。
func (r *StrmForgeScanResult) UnmarshalJSON(data []byte) error {
	if len(data) == 0 {
		*r = StrmForgeScanResult{}
		return nil
	}
	var wire scanResultWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("解析扫描结果失败: %w", err)
	}
	*r = StrmForgeScanResult{
		Mode:        wire.Mode,
		Added:       wire.Added,
		Resurrected: wire.Resurrected,
		Changed:     wire.Changed,
		Removed:     wire.Removed,
		Pending:     wire.Pending,
		Incomplete:  wire.Incomplete,
		Failures:    wire.Failures,
	}
	// 空文本对应真正的 nil 接口，不塞 typed-nil 进去。
	if wire.Error != "" {
		r.Error = &scanError{msg: wire.Error}
	}
	return nil
}

// scanError 是从落库文本还原出来的错误，只需要实现 error 接口。
type scanError struct{ msg string }

func (e *scanError) Error() string { return e.msg }

// Value 实现 driver.Valuer：结果可以直接作为 SQL 参数传入，不用预先编码。
// 零值写空串，避免在库里堆一串全零 JSON。
func (r StrmForgeScanResult) Value() (driver.Value, error) {
	if r.IsZero() {
		return "", nil
	}
	out, err := r.MarshalJSON()
	if err != nil {
		return nil, err
	}
	return string(out), nil
}

// Scan 实现 sql.Scanner：可以直接从 TEXT 列读进结构体。
// 空串与 NULL 都读成零值，由调用方用 IsZero 判断是不是「从未扫描」。
func (r *StrmForgeScanResult) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*r = StrmForgeScanResult{}
		return nil
	case string:
		return r.scanText([]byte(v))
	case []byte:
		return r.scanText(v)
	default:
		return fmt.Errorf("无法把 %T 读入扫描结果", src)
	}
}

func (r *StrmForgeScanResult) scanText(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		*r = StrmForgeScanResult{}
		return nil
	}
	return r.UnmarshalJSON(data)
}

// IsZero 判断结果里没有任何内容，用于区分「从未扫描」与「扫描了但全为零」。
func (r *StrmForgeScanResult) IsZero() bool {
	if r == nil {
		return true
	}
	return r.Mode == "" && r.Added == 0 && r.Resurrected == 0 && r.Changed == 0 &&
		r.Removed == 0 && r.Pending == 0 && !r.Incomplete &&
		len(r.Failures) == 0 && r.Error == nil
}
