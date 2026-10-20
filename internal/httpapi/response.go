// Package httpapi v2 的 HTTP 接入层。
//
// 与 v1 的区别只有三点, 但都是为了少写判断:
//   - 成功失败看 ok 布尔, 不靠状态码猜; 状态码依然正确, 只是前端不必维护映射表
//   - error 是对象, 带 code / message / hint / field。field 有值时直接标红对应输入框
//   - 每个响应带 trace_id, 和日志、audit_log 对得上, 用户报问题时发这一串就能查到上下文
//
// v1 的 /api/v1 保持原样, 由 internal/handler 继续提供, 两条链路并行一段时间。
package httpapi

import (
	"encoding/json"
	"net/http"
)

// Envelope 统一响应信封。Data 在失败时为 null, Error 在成功时为 null。
type Envelope struct {
	OK      bool        `json:"ok"`
	Data    interface{} `json:"data"`
	Error   *ErrPayload `json:"error"`
	TraceID string      `json:"trace_id"`
}

// ErrPayload 错误体。Hint 是给人看的下一步建议, Field 指向具体入参字段。
type ErrPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Field   string `json:"field,omitempty"`
}

// WriteOK 写成功响应。data 为 nil 时输出 null 而不是省略, 前端解析不必判断键存在。
func WriteOK(w http.ResponseWriter, r *http.Request, data interface{}) {
	writeJSON(w, r, http.StatusOK, Envelope{
		OK:      true,
		Data:    data,
		TraceID: TraceID(r),
	})
}

// WriteCreated 201。
func WriteCreated(w http.ResponseWriter, r *http.Request, data interface{}) {
	writeJSON(w, r, http.StatusCreated, Envelope{
		OK:      true,
		Data:    data,
		TraceID: TraceID(r),
	})
}

// WriteErr 写失败响应。
func WriteErr(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	writeJSON(w, r, status, Envelope{
		OK:      false,
		Error:   &ErrPayload{Code: code, Message: msg},
		TraceID: TraceID(r),
	})
}

// WriteErrField 带字段定位的校验错误。前端据此把红字标在该字段下面。
func WriteErrField(w http.ResponseWriter, r *http.Request, status int, code, msg, field, hint string) {
	writeJSON(w, r, status, Envelope{
		OK:      false,
		Error:   &ErrPayload{Code: code, Message: msg, Field: field, Hint: hint},
		TraceID: TraceID(r),
	})
}

// DecodeJSON 解析请求体。限制 1MB, 防止超大 body 打满内存。
// 不开 DisallowUnknownFields: 前后端并行开发阶段, 前端多传一个展示字段就 400 太脆。
// 真正的契约校验靠各字段的业务规则, 不靠"有没有多余键"。
func DecodeJSON(r *http.Request, dst interface{}) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	return dec.Decode(dst) == nil
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, env Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}
