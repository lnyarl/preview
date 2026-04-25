// 이 파일의 책임:
//   - HTTP Basic Auth 미들웨어 (Phase 3 §5-6, 결정 3·4).
//   - ADMIN_PASSWORD env 가 비어있으면 미들웨어 wrap 자체를 건너뛰고 1회 WARN 로그.
//   - 비밀번호 비교는 subtle.ConstantTimeCompare 만 사용 (NF-Security-1).
//   - 인증 실패 시 401 + WWW-Authenticate: Basic realm="hub-admin".
//
// 본 미들웨어는 /admin/* 라우트만 wrap 한다 (NF-Security-3). webhook/agent ws/proxy 미적용.
//
// 참고: docs/specs/phase-3-admin-ui-and-mvp.md §5-6, 결정 3/4.
package hub

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
)

// BasicAuthMiddleware 는 next 핸들러를 HTTP Basic Auth 로 감싼다.
// password 가 빈 문자열이면 미들웨어를 건너뛰고 next 를 그대로 반환 + 1회 WARN.
// 사용자명은 "admin" 고정.
func BasicAuthMiddleware(next http.Handler, password string, logger *slog.Logger) http.Handler {
	if password == "" {
		logger.Warn("admin_unauthenticated",
			"msg", "ADMIN_PASSWORD not set — /admin/* routes are open (DEV ONLY)")
		return next
	}
	expected := []byte(password)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" ||
			subtle.ConstantTimeCompare([]byte(pass), expected) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="hub-admin"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
