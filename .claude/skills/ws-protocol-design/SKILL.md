---
name: ws-protocol-design
description: Preview 서비스의 Hub↔Agent WebSocket 메시지 프로토콜을 설계한다. 메시지 타입 추가, Zod 스키마 작성, shared 패키지 갱신, 프로토콜 변경의 하위호환성 검토가 필요할 때 이 스킬을 반드시 사용. Phase 2 이후 WebSocket 단계에서 중심 스킬.
---

# WebSocket Protocol Design — Hub↔Agent 메시지 설계

Hub와 Agent 사이의 WebSocket 메시지는 `/shared` 패키지의 Zod 스키마가 유일한 진실 공급원. 이 스킬은 프로토콜을 추가·변경할 때 쓰는 표준 패턴.

## 언제 쓰는가

- 새 메시지 타입 추가 (예: `JOB_ASSIGNED`, `BUILD_LOG`)
- 기존 메시지의 필드 변경
- 프로토콜 버전 협상 규칙 변경
- Hub/Agent 한쪽이 "이런 메시지가 필요하다"고 요청할 때

## 설계 원칙

### 1. Discriminated Union + Zod

모든 메시지는 `type` 필드로 구분되는 discriminated union. Zod로 선언하고 `z.infer<>`로 타입 추출.

```ts
// shared/src/messages.ts
import { z } from 'zod';

export const AgentReady = z.object({
  type: z.literal('AGENT_READY'),
  agentId: z.string(),
  labels: z.record(z.string(), z.string()),
  capacity: z.number().int().positive(),
});

export const JobAssigned = z.object({
  type: z.literal('JOB_ASSIGNED'),
  jobId: z.string(),
  prNumber: z.number().int().positive(),
  repoUrl: z.string().url(),
  commitSha: z.string().length(40),
  labels: z.record(z.string(), z.string()),
});

export const ServerMessage = z.discriminatedUnion('type', [
  JobAssigned,
  // ...
]);

export const ClientMessage = z.discriminatedUnion('type', [
  AgentReady,
  // ...
]);

export type ServerMessage = z.infer<typeof ServerMessage>;
export type ClientMessage = z.infer<typeof ClientMessage>;
```

### 2. 방향별로 분리

- **ClientMessage**: Agent → Hub (READY, STATUS, LOG, ERROR)
- **ServerMessage**: Hub → Agent (ASSIGNED, TEARDOWN, PING, DISCONNECT)

한 쪽 방향으로만 가는 메시지는 반대쪽 union에 넣지 않는다.

### 3. ID / 시간 표기 통일

- **ID**: nanoid (21자). 한 번 정했으면 끝까지 유지.
- **시간**: epoch ms (number). ISO 문자열보다 비교·정렬이 싸다.
- **Enum**: `z.enum([...])`로 Zod에서 직접 정의. 문자열 리터럴 사용.

### 4. 작은 Surface Area

한 메시지는 한 가지 일만. 복합 메시지(여러 이벤트를 묶어서 보내는 것) 금지. 스트리밍(로그)은 별도 메시지 타입으로.

### 5. 버저닝

- 현재는 단일 버전. `PROTOCOL_VERSION` 상수를 `shared/src/constants.ts`에 export.
- Agent는 연결 직후 `AGENT_HELLO { version }` 보내고, Hub는 `SERVER_HELLO { version, accepted }` 회신.
- 버전 불일치: Hub가 `DISCONNECT { reason: 'INCOMPATIBLE' }` 보내고 종료.
- 하위호환 가능한 변경 (필드 추가): 버전 유지.
- 파괴적 변경: 버전 올리고 architect에게 ADR 요청.

## 표준 메시지 세트 (초기 제안)

### Client → Server (Agent → Hub)

| type | 목적 |
|---|---|
| `AGENT_HELLO` | 연결 직후 인증 토큰 + 프로토콜 버전 |
| `AGENT_READY` | "작업 받을 준비됨" (pull 요청) |
| `JOB_STATUS` | 상태 전이 보고 (`building`, `running`, `teardown`, `done`, `failed`) |
| `JOB_LOG` | 로그 샘플 (레벨, 메시지, jobId) |
| `JOB_URL` | 프리뷰 URL 보고 (jobId, port, url) |
| `HEARTBEAT` | 주기적 생존 신호 |

### Server → Client (Hub → Agent)

| type | 목적 |
|---|---|
| `SERVER_HELLO` | Hello 응답, 프로토콜 수락/거부 |
| `JOB_ASSIGNED` | 작업 할당 (repo, commit, labels) |
| `JOB_CANCEL` | 진행 중 작업 취소 지시 |
| `TEARDOWN` | PR 닫혔을 때 해당 컨테이너 정리 지시 |
| `DISCONNECT` | 연결 종료 (reason 포함) |

## 작성 플로우

1. **요청 분석**: 누가 어떤 시나리오에서 이 메시지를 쓰는가? 방향은? 빈도는?
2. **기존 메시지로 커버 가능한지 확인**: 유사 메시지가 있으면 필드 추가로 해결 시도.
3. **ADR 필요 여부 판단**: 파괴적 변경이면 architect에게 ADR 요청. 아니면 스킵.
4. **Zod 스키마 작성**: `shared/src/messages.ts`에 추가. discriminated union에 등록.
5. **타입 재노출 확인**: `shared/src/index.ts`에서 re-export되는지.
6. **변경 요약 작성**: `_workspace/protocol-change-{feature}.md`에 변경 내역, 영향 패키지, migration 필요 여부 기록.
7. **양쪽 개발자에게 알림**: SendMessage로 hub-dev와 agent-dev에게 "shared에 X 메시지 추가됨" 통지.

## 양쪽 구현 패턴

### Hub 측 핸들러

```ts
// hub/src/ws/handler.ts
import { ClientMessage } from '@preview/shared';

ws.on('message', (raw) => {
  const parsed = ClientMessage.safeParse(JSON.parse(raw.toString()));
  if (!parsed.success) { /* reject + log */ return; }
  const msg = parsed.data;
  switch (msg.type) {
    case 'AGENT_READY': /* ... */ break;
    case 'JOB_STATUS':  /* ... */ break;
    // exhaustive: msg를 never에 할당해서 컴파일 체크
    default: { const _exhaust: never = msg; void _exhaust; }
  }
});
```

### Agent 측 송신

```ts
// agent/src/ws/send.ts
import { ClientMessage } from '@preview/shared';

export function send(ws: WebSocket, msg: ClientMessage) {
  ws.send(JSON.stringify(msg));
}
```

## 검증 체크리스트 (qa-reviewer가 확인)

- [ ] discriminated union에 등록되었는가?
- [ ] Hub·Agent 양쪽 handler 중 한 곳이라도 누락된 type이 있는가? (exhaustive switch 사용 확인)
- [ ] 필드 이름이 기존 관례(`camelCase`)와 일치하는가?
- [ ] ID/시간 표기가 공통 규칙(nanoid, epoch ms)을 따르는가?
- [ ] `shared/src/index.ts`에서 re-export되는가?
- [ ] 테스트: 잘못된 payload를 safeParse할 때 실패가 예상되는가?

## 안티패턴

- **JSON.parse + as 캐스팅**: Zod safeParse 거치지 않고 타입 단언 금지.
- **any/unknown 흘려보내기**: 경계 한 번만 검증, 이후엔 타입 안전.
- **거대 메시지**: 500KB 넘는 payload는 스트리밍으로 분해.
- **양방향 동일 이름**: `STATUS`가 Hub→Agent, Agent→Hub 둘 다에 있으면 혼란. 방향 접두어(`AGENT_`, `SERVER_`) 권장.
