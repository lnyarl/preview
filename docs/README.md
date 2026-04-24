# docs/

Preview 서비스의 설계 문서.

## 하위 디렉토리

- **`adr/`** — Architecture Decision Records. `NNNN-slug.md` 4자리 연속 번호 형식. MADR 스타일 5 섹션(Status, Context, Decision, Consequences, Alternatives).

## 현재 ADR

| #                                        | 제목                       | 상태     |
| ---------------------------------------- | -------------------------- | -------- |
| [0001](./adr/0001-monorepo-structure.md) | 모노레포 구조 및 기반 결정 | Accepted |

## 새 ADR 추가 방법

1. 기존 최대 번호 + 1로 파일명 결정 (예: `0002-job-state-machine.md`).
2. MADR 템플릿 5 섹션 유지.
3. 기존 ADR을 대체하는 경우 상단에 `Supersedes: ADR-NNNN` 기록, 대체된 ADR Status를 `Superseded by ADR-MMMM`으로 갱신.
4. README 표에 링크 추가.
