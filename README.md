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

## 릴리즈

```bash
make tag v=1.0.0
```

태그를 푸시하면 GitHub Actions가 자동으로 전 플랫폼 바이너리를 빌드해 Release에 업로드합니다.

## 저장소 요구사항

빌드 대상 저장소 루트에 `Dockerfile`이 있어야 하고, 컨테이너는 포트 80을 노출해야 합니다.

## 기술 스택

Go 1.22 · SQLite (CGO-free) · html/template · coder/websocket · Docker SDK
