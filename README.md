# Preview

GitHub PR이 열리면 자동으로 미리보기 환경을 띄우고, PR이 닫히면 정리하는 셀프호스팅 서비스.

## 구조

```
Hub (서버)                Agent (빌드 머신)
─────────────────         ─────────────────────────────
GitHub 웹훅 수신    ←──   outbound WebSocket 연결
Job 큐 관리         ──→   git fetch → docker build → docker run
리버스 프록시              PR 닫히면 컨테이너 정리
관리자 대시보드
```

Agent가 Hub에 outbound로 연결하기 때문에 빌드 머신에 포트를 열 필요가 없습니다.

## 빠른 시작

```bash
# 1. 설정
cp .env.example .env
# .env 편집: GITHUB_WEBHOOK_SECRET, AGENT_REPO_URL 채우기

# 2. 실행
make dev

# 3. 대시보드에서 Agent 등록
# http://localhost:3000/admin → Agents → Add Agent
# → 토큰 페이지에서 OS별 실행 명령 확인
```

## 환경변수 (.env)

| 변수 | 필수 | 설명 |
|---|---|---|
| `GITHUB_WEBHOOK_SECRET` | ✅ | GitHub Webhook 서명 키 |
| `AGENT_REPO_URL` | ✅ | 빌드할 저장소 URL |
| `ADMIN_PASSWORD` | | 대시보드 비밀번호 (미설정 시 인증 없음) |
| `AGENT_DOWNLOAD_URL` | | Agent 바이너리 다운로드 URL (토큰 페이지에 표시) |
| `PREVIEW_BASE_DOMAIN` | | 프록시 도메인 (기본: `localhost`) |

## 주요 명령어

```bash
make dev          # Docker로 Hub + Agent 시작
make up           # 시작 (빌드 생략)
make down         # 중지
make logs         # 로그
make tag v=1.0.0  # 릴리즈 태그 생성 + 푸시
make test         # 테스트
```

## Build secrets (Phase 10)

저장소(`owner/repo`) 단위로 빌드 시 환경변수 묶음을 등록할 수 있습니다.

- 등록: 대시보드 → **Repos** → `Edit secrets` → textarea 에 `KEY=VALUE` 한 줄씩 입력 → **Save**.
- 적용: 다음 PR webhook 의 JOB_ASSIGN 메시지에 동봉되어 Agent 가 worktree 루트에 `.env` 파일로 작성.
- compose 모드: `docker compose up` 이 동일 디렉토리의 `.env` 를 자동 인식 → `${VAR}` 보간 + `env_file: .env` 양쪽이 동작.
- Dockerfile 모드: `docker build` 는 `.env` 를 자동 ARG/ENV 로 주입하지 **않습니다**. `Dockerfile` 안에서 `COPY .env /app/.env` 후 런타임 dotenv 로딩, 또는 build args 로 명시 전달이 필요합니다.

예:

```
DATABASE_URL=postgres://app:pw@db:5432/app
API_KEY=sk-xxxxxxxxxxxx
FEATURE_FLAG=on
```

**보안 주의 (Phase 10 한정):**
- 값은 **plaintext** 로 Hub DB(`hub.db`) 에 저장됩니다. 암호화는 후속 Phase 예정.
- JOB_ASSIGN 페이로드의 `build_env` 필드도 평문으로 WS frame 위에 흐릅니다 — Hub 를 reverse proxy(TLS 종단) 또는 자체 https 뒤에 두어야 합니다 (`wss://`).
- Repo full name 은 case-insensitive (저장 시 lowercase 정규화).
- 같은 repo 의 worktree 에 git-tracked 된 기존 `.env` 가 있으면 매 빌드마다 덮어써집니다.

## 프로덕션 설치

OS별 상세 가이드:

- [Linux](docs/install/linux.md) — Hub 서버 + Agent (systemd, Caddy TLS)
- [macOS](docs/install/macos.md) — Agent (launchd)
- [Windows](docs/install/windows.md) — Agent (작업 스케줄러 / NSSM)

## 릴리즈

```bash
make tag v=1.0.0
```

태그를 푸시하면 GitHub Actions가 자동으로 전 플랫폼 바이너리를 빌드해 Release에 업로드합니다.

## 기술 스택

Go 1.22 · SQLite (CGO-free) · html/template · coder/websocket · Docker SDK
