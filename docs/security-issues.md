# Preview — 보안 이슈 목록

Date: 2026-04-26

---

## 🔴 High

### H-1. `ADMIN_PASSWORD` 미설정 시 `/admin/*` 무방비
- **위치**: `internal/hub/auth.go:22-25`
- **내용**: `BasicAuthMiddleware`는 존재하며 `subtle.ConstantTimeCompare`로 올바르게 구현됨. 단, `ADMIN_PASSWORD` 환경변수가 빈 값이면 인증을 건너뛰고 WARN 로그만 남긴 채 `/admin/*` 전체를 열어준다. 프로덕션 배포 시 환경변수 누락으로 무방비 상태가 될 수 있음.
- **상태**: 해결 (`3cd01ff` — `Validate()`에 `ErrAdminPasswordMissing` 추가, 데몬 기동 차단)

### H-2. 임의 Dockerfile/docker-compose 실행
- **위치**: `internal/agent/runner.go` (`handleCompose`, `handleDockerfile`)
- **내용**: Agent가 PR 리포지토리를 clone해 `docker build` / `docker compose up`을 실행한다. 악의적인 PR의 `Dockerfile` 또는 `docker-compose.yml`이 Agent 호스트에서 임의 코드를 실행할 수 있다.
- **상태**: 수용 (won't fix — 신뢰된 리포지토리만 연결하는 운영 정책으로 관리)

### H-3. Docker daemon 접근 = root 동급
- **위치**: `cmd/agent/main.go`, `internal/agent/docker.go`
- **내용**: Agent가 Docker socket을 직접 사용한다. 컨테이너 탈출 취약점이 발생하면 호스트 루트 접근 가능. `docker build` 중 악의적인 `RUN` 명령도 동일 위협.
- **상태**: 수용 (won't fix — H-2와 동일 위협 모델, rootless Docker는 운영 환경 선택 사항)

---

## 🟡 Medium

### M-1. WebSocket Origin 검증 비활성
- **위치**: `internal/hub/ws_handler.go:101`
- **내용**: `InsecureSkipVerify: true`로 Cross-Origin WebSocket 연결을 허용한다. 코드 주석 "Origin 검증은 Phase 후속" 으로 의도적 미구현 상태.
- **상태**: 미해결

### M-2. Traefik API 무인증 노출
- **위치**: `internal/agent/traefik.go` (`EnsureTraefik`)
- **내용**: `APIHostPort > 0`이면 `--api.insecure=true`로 Traefik 대시보드/REST API가 인증 없이 호스트 포트에 바인딩된다. 라우팅 설정 전체가 외부에 노출.
- **상태**: 해결 (Phase 7 — `cmd/agent/docker_sdk.go:96-99`의 SDK 어댑터가 ContainerPort≠80인 포트를 `127.0.0.1`에만 바인딩. Traefik API는 에이전트 호스트 로컬에서만 접근 가능)

### M-3. Webhook body 크기 제한 없음
- **위치**: `internal/hub/webhook_handler.go:153`
- **내용**: `io.ReadAll(r.Body)` 에 `http.MaxBytesReader` 래핑이 없다. 거대한 페이로드로 Hub 메모리를 소진할 수 있다 (DoS).
- **상태**: 해결 (`webhookMaxBodyBytes = 10 MiB` 상수 + `http.MaxBytesReader` 래핑)

### M-4. TLS 미적용 시 토큰 평문 전송
- **위치**: `cmd/agent/main.go`, `cmd/hub/main.go`
- **내용**: Hub-Agent WebSocket 연결이 HTTP면 Bearer 토큰이 평문으로 전송된다. TLS 강제 설정이나 가이드가 없음.
- **상태**: 미해결

---

## 🟢 이미 잘 된 것

- **GitHub webhook HMAC**: `crypto/hmac.Equal` (timing-safe) 올바르게 구현 (`webhook_handler.go:173`)
- **Agent 토큰 인증**: Bearer 토큰 + DB lookup 정상 구현 (`ws_handler.go:79-98`)
- **SQL injection**: sqlc 생성 코드 + prepared statement 사용
- **Shell injection 없음**: `exec.Command` 기반 CmdRunner — 셸을 거치지 않아 인자 주입 불가
