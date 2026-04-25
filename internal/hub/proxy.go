// 이 파일의 책임:
//   - MatchHost: 호스트 헤더에서 pr 번호와 base domain 추출.
//   - ProxyMiddleware: 매칭된 호스트를 running preview 의 agent 로 프록시.
//   - 캐시: sync.Map (prNumber → *proxyEntry), UpdateStatus 후 Invalidate 호출로 evict.
//
// 참고: docs/specs/phase-2-webhook-dispatch-proxy.md §3 결정 2, F-S3-1~4-c.
package hub

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"sync"

	"github.com/lnyarl/preview/internal/store"
)

// hostRe 는 `pr-{n}.preview.{base}(:{port})?` 를 파싱한다.
// base 는 콜론을 포함하지 않는다([^:]+).
var hostRe = regexp.MustCompile(`^pr-(\d+)\.preview\.([^:]+)(?::\d+)?$`)

// MatchHost 는 호스트 헤더에서 PR 번호와 base domain 을 추출한다.
// ok=false 이면 호스트가 프록시 대상 아님.
func MatchHost(host string) (prNumber int, base string, ok bool) {
	m := hostRe.FindStringSubmatch(host)
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, "", false
	}
	return n, m[2], true
}

type proxyEntry struct {
	previewID string
	proxy     *httputil.ReverseProxy
}

// ProxyMiddleware 는 pr-N.preview.{base} 호스트 헤더를 running preview 의 agent 로 프록시.
type ProxyMiddleware struct {
	store      store.PreviewStore
	baseDomain string
	logger     *slog.Logger
	cache      sync.Map // int (prNumber) → *proxyEntry
}

// NewProxyMiddleware 는 ProxyMiddleware 를 만든다.
func NewProxyMiddleware(s store.PreviewStore, baseDomain string, logger *slog.Logger) *ProxyMiddleware {
	return &ProxyMiddleware{store: s, baseDomain: baseDomain, logger: logger}
}

// Invalidate 는 previewID 에 대응하는 캐시 항목을 제거한다.
// WebhookHandler handleClose + StatusUpdater 에서 호출.
func (m *ProxyMiddleware) Invalidate(previewID string) {
	m.cache.Range(func(k, v interface{}) bool {
		if v.(*proxyEntry).previewID == previewID {
			m.cache.Delete(k)
		}
		return true
	})
}

// Wrap 은 next 핸들러를 프록시 미들웨어로 감싼다.
// pr-N.preview.{baseDomain} 호스트 외에는 next 로 fallthrough.
func (m *ProxyMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prNumber, base, ok := MatchHost(r.Host)
		if !ok || base != m.baseDomain {
			next.ServeHTTP(w, r)
			return
		}

		// 캐시 조회.
		if v, hit := m.cache.Load(prNumber); hit {
			v.(*proxyEntry).proxy.ServeHTTP(w, r)
			return
		}

		// DB 조회.
		p, err := m.store.FindByHost(r.Context(), "", prNumber)
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "preview not found")
			return
		}
		if p.Status != "running" {
			writeError(w, http.StatusServiceUnavailable, "preview_not_running",
				fmt.Sprintf("preview status is %s", p.Status))
			return
		}
		if p.AgentHost == nil || p.AgentPort == nil {
			writeError(w, http.StatusServiceUnavailable, "preview_not_running", "agent address unknown")
			return
		}

		target := &url.URL{
			Scheme: "http",
			Host:   fmt.Sprintf("%s:%d", *p.AgentHost, *p.AgentPort),
		}
		rp := httputil.NewSingleHostReverseProxy(target)
		// F-S3-4-b: Director 가 req.Host 를 target.Host 로 덮어써야 함.
		rp.Director = func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			if _, ok := req.Header["User-Agent"]; !ok {
				req.Header.Set("User-Agent", "")
			}
		}
		entry := &proxyEntry{previewID: p.ID, proxy: rp}
		m.cache.Store(prNumber, entry)
		m.logger.Info("proxy_cache_miss", "preview_id", p.ID, "pr_number", prNumber, "target", target.Host)
		rp.ServeHTTP(w, r)
	})
}
