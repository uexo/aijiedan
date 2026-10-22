package httpapi

import (
	"io"
	"net/http"
)

// maxDrainBytes 单次最多丢弃多少请求体字节。
// 4KB 足够覆盖本服务的全部请求(发单最长 2K 字), 又不至于让一个畸形的大包
// 把连接线程占住。超过的不再勉强, 按"读不完"处理——那种请求本来也不该出现。
const maxDrainBytes = 64 << 10

// WithDrainBody 在写出响应头之前把请求体读干净。
//
// 为什么非做不可: 有些写端点的请求体是空的或者干脆没被读(认领 POST 只带 `{}`,
// 鉴权失败的请求在读 body 之前就被拒), 而客户端(urllib、部分 SDK)默认
// `Connection: close`。服务端回完响应立刻关连接, 此时接收缓冲区里还剩未读字节,
// 操作系统发的不是 FIN 而是 RST——客户端看到的是 "Connection reset by peer",
// 可是业务其实已经成功了。
//
// 症状极具误导性: 同一条链路 201 和 409 都会随机变成"连接被重置",
// 单人顺序请求都能复现(实测 25 轮里有 9~12 轮), 于是很容易被当成
// "客户端 socket 抖动"或"服务端并发问题", 查很久都查不到。
//
// 放在最外层: 鉴权、CSRF 的提前拒绝也要覆盖, 那才是最容易漏读 body 的路径。
func WithDrainBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&drainWriter{ResponseWriter: w, req: r}, r)
	})
}

// drainWriter 把"读完请求体"这件事挂在第一次写响应头之前。
// 业务已经读过 body 时这里是空操作(读到 EOF 立刻返回), 不会多耗一次往返。
type drainWriter struct {
	http.ResponseWriter
	req  *http.Request
	done bool
}

func (d *drainWriter) WriteHeader(code int) {
	d.drain()
	d.ResponseWriter.WriteHeader(code)
}

func (d *drainWriter) Write(p []byte) (int, error) {
	d.drain() // 有的路径直接 Write 而不显式 WriteHeader
	return d.ResponseWriter.Write(p)
}

func (d *drainWriter) drain() {
	if d.done {
		return
	}
	d.done = true
	// 不设上限地读完会被畸形请求拖住; 带上限后最坏只是漏读, 与不做的效果相同。
	_, _ = io.Copy(io.Discard, io.LimitReader(d.req.Body, maxDrainBytes))
	_ = d.req.Body.Close()
}

// Unwrap 让 http.ResponseController 能拿到原始 ResponseWriter。
// 没有这个, 将来接 SSE / 大文件下载时会因为看不到 Flusher 而卡死。
func (d *drainWriter) Unwrap() http.ResponseWriter { return d.ResponseWriter }
