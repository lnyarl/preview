# ADR-0001: 모노레포 구조 및 기반 기술 결정

- **Status**: Accepted
- **Date**: 2026-04-24
- **Authors**: architect (Phase 0)
- **Related**: `_workspace/phase-0-spec.md` §2, `.claude/skills/monorepo-scaffold/SKILL.md`

---

## Status

Accepted.

---

## Context

Preview 서비스는 두 런타임 컴포넌트(Hub 서버와 Agent CLI)로 구성된다. 양쪽은 **공통 메시지 프로토콜**(WebSocket 메시지, HTTP contract, 공유 상수)을 통해 통신하므로, 타입과 Zod 스키마를 동일한 소스에서 빌드하는 구조가 필요하다. 동시에 Phase 단위로 기능이 쌓여가는 장기 프로젝트이므로, 초기 결정이 전체 수명에 영향을 준다.

이 ADR은 Phase 0에서 확정한 11개의 기반 결정을 요약한다. 각 결정의 상세 대안과 트레이드오프는 `_workspace/phase-0-spec.md` §2에 기록되어 있으며, 본 ADR은 **결정 자체와 요약 근거**만을 담는다.

---

## Decision

다음 **11개의 결정**을 Phase 0 스캐폴딩에 적용한다. 번호는 phase-0-spec.md §2와 대응한다.

- **Decision 2.1 — 패키지 매니저 = pnpm 9.15.0 고정** (`packageManager: "pnpm@9.15.0"`).
  - 이유: content-addressable store, `workspace:*` 1급 지원, 모노레포 네이티브 `--filter`.
- **Decision 2.2 — Node 20 LTS** (`.nvmrc` = 20; `engines.node: ">=20.11"` — 상한은 실무 호환을 위해 제거, 구현 메모 E1).
  - 이유: Fastify/ws/dockerode 안정 호환 + Windows prebuilt 보급.
- **Decision 2.3 — TypeScript strict 3종 활성** (`strict`, `noUncheckedIndexedAccess`, `exactOptionalPropertyTypes`).
  - 이유: 경계면 옵셔널/인덱스 오류 컴파일 타임 차단.
  - 유의: Fastify 4와 `exactOptionalPropertyTypes` 미세 충돌 가능성 있음. Phase 1 킥오프 시 검증·대응(§2.5/ADR 별도).
- **Decision 2.4 — 모듈 시스템 = NodeNext / ESM** (`"type": "module"`).
  - 이유: Fastify/ws/dockerode 전부 ESM 지원. 상대 import에 `.js` 확장자 규칙.
- **Decision 2.5 — HTTP 프레임워크 = Fastify 4 고정** (`^4.28.0`).
  - 이유: 안정 LTS 라인. Phase 1 킥오프 시 Fastify 5 GA 확인하여 승격 여부 재평가.
- **Decision 2.6 — Vitest 선설치** (케이스 0, `--passWithNoTests`).
  - 이유: Phase 1부터 즉시 작성 가능. Jest/node:test 대비 설정 부담 낮음.
- **Decision 2.7 — ESLint 9 flat config + Prettier**.
  - 의존성: `@eslint/js`, `typescript-eslint`(메타), `globals`, `eslint-config-prettier`.
  - 이유: 플랫 config 표준, Node globals no-undef 오탐 회피, 포매팅 충돌 차단.
  - Typed-linting(`project: true`)은 Phase 0 비활성, Phase 1에서 재평가.
- **Decision 2.8 — `tsx` 개발 실행기 + 패키지별 devDep 재선언 정책**.
  - 이유: ESM+TS 직접 실행, esbuild 기반 속도. phantom-dep 회피를 위해 `tsx`, `typescript`, `vitest`, `@types/node`를 각 패키지 `devDependencies`에 재선언.
- **Decision 2.9 — Postgres 16-alpine**, named volume `pgdata`, 호스트 포트 `${PG_HOST_PORT:-5432}:5432` 파라미터화.
  - 이유: 16 LTS, alpine 이미지 최소화, 포트 충돌 회피.
- **Decision 2.10 — 패키지 네이밍 = `@preview/*`** (`@preview/hub`, `@preview/agent`, `@preview/shared`).
  - 이유: 스코프 명확, 외부 레지스트리와 충돌 회피.
- **Decision 2.11 — Workspace 참조 = `workspace:*`** (tsconfig `paths` 미사용).
  - 이유: pnpm 심볼릭 링크/정션으로 타입·런타임 모두 해결.

---

## Consequences

**긍정**

- 처음부터 strict 3종 + ESLint + Prettier가 일관되게 적용되어, Phase 1 이후 타입/스타일 규칙 변경 비용이 낮다.
- `shared` 패키지가 `workspace:*`로 즉시 참조되어 Hub↔Agent 프로토콜의 SSoT(Single Source of Truth)가 Phase 2부터 바로 작동 가능하다.
- 패키지별 devDep 재선언 정책은 CI/깨끗한 체크아웃에서의 재현성을 높인다.

**부정 / 비용**

- Node engines 상한 제거로 사용자가 Node 24 등 최신 버전에서 실행 시 LTS 권장 경고가 자동으로 뜨지 않는다. README "사전 요구사항"에 수동 안내.
- Fastify 4 + `exactOptionalPropertyTypes`는 Phase 1에서 라우트/훅 추가 시 타입 충돌이 관측될 수 있음. 대응 트리는 spec §2.3.
- 패키지별 devDep 재선언은 버전 드리프트 방지 책임이 개발자에게 있음(루트 한 곳에서 관리되지 않음). `pnpm up -r`으로 일괄 업데이트 가능하나 정책상 주기적 점검 필요.

---

## Alternatives

- **npm workspaces**: 느림, phantom-dep 다발. 거부.
- **yarn berry**: 학습 비용, `.pnp` 수용도 낮음. 거부.
- **Bun**: Windows/native 모듈(`dockerode`) 호환 리스크. Phase 0 배제.
- **TypeScript `strict`만 활성**: `undefined` 잠복 버그 위험. 거부.
- **Fastify 5**: Phase 0 시점 일부 플러그인 호환 이슈. Phase 1 재평가.
- **Biome (린트/포매팅 통합)**: 플러그인 생태계 미성숙. 후속 Phase 고려.
- **CommonJS 모듈**: 장기 표준이 아님. 거부.
- **tsconfig `paths` 기반 workspace 참조**: pnpm `workspace:*`와 중복. 거부.

상세 대안과 탈락 사유는 `_workspace/phase-0-spec.md` §2 각 항목 참조.

---

_End of ADR-0001._
