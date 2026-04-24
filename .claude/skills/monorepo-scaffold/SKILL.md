---
name: monorepo-scaffold
description: Preview 서비스의 pnpm workspace 모노레포 초기 구조를 생성한다. 모노레포 스캐폴딩, 패키지 구조 설정, TypeScript strict/ESLint/Prettier 공통 설정, docker-compose 초안이 필요할 때 이 스킬을 사용할 것. Phase 0에서 필수.
---

# Monorepo Scaffold — 모노레포 초기 구조 생성

Preview 서비스의 `/hub`, `/agent`, `/shared` 모노레포를 처음 세팅할 때 쓰는 스킬.

## 언제 쓰는가

- Phase 0에서 빈 디렉토리에 프로젝트 뼈대를 세울 때
- 새 패키지를 workspace에 추가할 때 (예: `/cli` 추가)

## 도구 선택 기준

| 항목 | 선택 | 이유 |
|---|---|---|
| 패키지 매니저 | **pnpm** | workspace 성능, 디스크 효율, Node.js 생태계 표준 |
| 언어 | TypeScript 5.x | strict mode 필수 |
| 테스트 | Vitest | TS 네이티브, Jest 호환 API, 빠름 |
| Lint | ESLint (flat config) + Prettier | 표준 |
| Node 버전 | 20 LTS | Fastify/dockerode 호환성 검증됨 |

## 최종 구조

```
/
├── package.json              (루트: 워크스페이스 정의, devDeps 공유)
├── pnpm-workspace.yaml
├── tsconfig.base.json        (공통 TS 설정: strict, ES2022, NodeNext)
├── .eslintrc.cjs             (또는 eslint.config.js — flat config)
├── .prettierrc
├── .editorconfig
├── .gitignore
├── .env.example
├── docker-compose.yml
├── README.md
├── CLAUDE.md                 (하네스 컨텍스트)
├── shared/
│   ├── package.json          (name: @preview/shared)
│   ├── tsconfig.json         (extends base, outDir dist)
│   └── src/index.ts          ("Hello Shared" export)
├── hub/
│   ├── package.json          (name: @preview/hub, "start" 스크립트)
│   ├── tsconfig.json
│   ├── src/index.ts          (Fastify "Hello Hub" 서버)
│   └── Dockerfile            (선택: Phase 0엔 optional)
├── agent/
│   ├── package.json          (name: @preview/agent, bin 필드)
│   ├── tsconfig.json
│   ├── src/index.ts
│   └── bin/agent.ts          ("Hello Agent" CLI)
├── docs/
│   └── adr/
│       └── 0001-monorepo-structure.md
└── _workspace/               (팀 중간 산출물 — gitignore)
```

## 핵심 설정 원칙

### pnpm-workspace.yaml

```yaml
packages:
  - 'hub'
  - 'agent'
  - 'shared'
```

### 루트 package.json

- `"private": true`로 배포 방지
- `scripts`: `build`, `typecheck`, `lint`, `format`, `dev:hub`, `dev:agent` — 모두 `pnpm -r --filter ...`로 위임
- `devDependencies`에 공통 도구(typescript, eslint, prettier, vitest)만

### tsconfig.base.json

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "module": "NodeNext",
    "moduleResolution": "NodeNext",
    "lib": ["ES2022"],
    "strict": true,
    "noUncheckedIndexedAccess": true,
    "exactOptionalPropertyTypes": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "forceConsistentCasingInFileNames": true,
    "declaration": true,
    "declarationMap": true,
    "sourceMap": true
  }
}
```

핵심: `strict: true`만으론 부족. `noUncheckedIndexedAccess`와 `exactOptionalPropertyTypes`도 켠다.

### shared 패키지 참조 방식

hub와 agent의 `package.json`에 `"@preview/shared": "workspace:*"`로 참조. tsconfig에선 `paths` 대신 pnpm의 workspace 심볼릭 링크를 신뢰.

### hub/src/index.ts (최소 예시)

```ts
import Fastify from 'fastify';

const app = Fastify({ logger: true });

app.get('/', async () => ({ hello: 'hub' }));

const port = Number(process.env.PORT ?? 3000);
app.listen({ port, host: '0.0.0.0' })
  .then(() => app.log.info(`Hub listening on ${port}`))
  .catch((err) => { app.log.error(err); process.exit(1); });
```

### agent/bin/agent.ts (최소 예시)

```ts
#!/usr/bin/env node
console.log('Hello Agent');
```

`package.json`의 `"bin": { "preview-agent": "./dist/bin/agent.js" }` 로 등록.

### docker-compose.yml (Phase 0용)

Postgres + Hub 두 서비스만. Hub는 Dockerfile 없으면 주석으로 placeholder. Postgres는 실제 기동되도록.

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: preview
      POSTGRES_PASSWORD: preview
      POSTGRES_DB: preview
    ports: ["5432:5432"]
    volumes: [pgdata:/var/lib/postgresql/data]
  # hub: (Phase 0 이후 활성화)
volumes:
  pgdata:
```

### .env.example

필수 항목:
- `DATABASE_URL=postgres://preview:preview@localhost:5432/preview`
- `HUB_PORT=3000`
- `HUB_PUBLIC_URL=http://localhost:3000`
- `GITHUB_WEBHOOK_SECRET=changeme` (Phase 1에서 사용 — 미리 예약)
- `AGENT_TOKEN=` (Phase 2에서 Agent 인증용 — 미리 예약)

### .gitignore

최소: `node_modules/`, `dist/`, `.env`, `_workspace/`, `*.log`, `pgdata/`

### README.md (Phase 0 버전)

섹션 구성:
1. 한 줄 소개 ("Self-hosted Vercel Preview for GitHub PRs")
2. 아키텍처 ASCII 다이어그램 (Hub / Agent / GitHub / Docker 관계)
3. 핵심 설계 결정 요약 (Pull dispatch, outbound WS, 토큰 인증, label 라우팅)
4. 기술 스택 표
5. 로컬 실행 방법 (`pnpm install`, `docker compose up -d postgres`, `pnpm dev:hub`, `pnpm dev:agent`)
6. Phase 로드맵 체크리스트

## 실행 순서 (권장)

1. 루트 파일들(`package.json`, `pnpm-workspace.yaml`, `tsconfig.base.json`, lint/prettier/editorconfig/gitignore, `.env.example`) 생성
2. `shared` → `hub` → `agent` 순으로 패키지 생성 (의존성 역순)
3. `docker-compose.yml` 작성
4. `README.md`, `CLAUDE.md`, `docs/adr/0001-monorepo-structure.md` 작성
5. `pnpm install` 실행하여 workspace 링크 검증
6. `pnpm -r typecheck && pnpm -r build` 실행하여 전 패키지 빌드 통과 확인
7. `pnpm --filter @preview/hub dev` 실행하여 Fastify가 뜨는지 확인
8. `pnpm --filter @preview/agent start` (또는 CLI bin 직접 실행)으로 "Hello Agent" 출력 확인

## 주의

- **의존성 최소화**: Phase 0엔 실제 웹훅/WebSocket/Docker 코드를 넣지 않는다. 진입점만 만든다.
- **과도한 스캐폴딩 금지**: eslint 플러그인, Husky, lint-staged 같은 건 요청 없이 추가 금지. Phase 0 목표는 "다음에 뭘 붙이면 되는지 명확한 상태".
- **Windows 호환성**: 사용자는 Windows이므로 쉘 스크립트(.sh) 금지, cross-env 같은 OS 호환 처리 필요 시 사용.
