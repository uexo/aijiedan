// Package aihttp 提供「可插拔外部 AI 服务(LLM / Embedding)」共用的 HTTP 客户端构造。
//
// 关键点：
//   - 默认读取环境代理 HTTP_PROXY / HTTPS_PROXY / NO_PROXY（http.ProxyFromEnvironment），
//     以适配内网 / 中国大陆访问 OpenAI、DeepSeek、本地 Ollama 等场景（如 Clash 127.0.0.1:7890）。
//   - 额外支持 LINKZAM_AI_PROXY 显式覆盖（优先级高于系统代理环境变量）。
//   - 不引入任何第三方依赖，纯标准库。
package aihttp

import (
	"net/http"
	"net/url"
	"os"
	"time"
)

// New 返回一个带代理感知 Transport 的 *http.Client。
// timeout 控制单次请求上限，避免外部 API 不可达时把请求线程挂死。
func New(timeout time.Duration) *http.Client {
	t := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if p := os.Getenv("LINKZAM_AI_PROXY"); p != "" {
		if u, err := url.Parse(p); err == nil {
			t.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Timeout: timeout, Transport: t}
}
