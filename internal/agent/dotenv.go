// 이 파일의 책임:
//   - WriteDotenv: worktree 루트에 KEY=VALUE\n 묶음을 0600 권한 dotenv 파일로 작성한다 (Phase 10).
//   - escapeDotenvValue: VALUE 안의 실제 newline (0x0A) 을 literal `\n` 두 글자로 escape (결정 10).
//   - env 가 nil 또는 비어있으면 파일 작성 자체를 skip — 기존 worktree 의 .env 를 보존한다 (결정 5).
//   - key 정렬을 보장해 동일 입력 → 동일 출력의 deterministic file content (테스트/diff 용이성).
//
// 의존성: 표준 라이브러리만 사용. 본 파일은 internal/store / internal/db/* 를 import 하지 않는다 (NF-Layer-1).
//
// 참고: docs/specs/phase-10-repo-build-secrets.md §3 결정 10, §5-5, F-22~F-24, NF-Security-2.
package agent

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// WriteDotenv 는 env 의 (KEY,VALUE) 쌍들을 KEY=VALUE\n 형식으로 path 에 작성한다.
//
// 동작:
//   - env 가 nil 또는 len==0 → 파일을 만들지 않고 nil 반환 (기존 .env 보존, 결정 5).
//   - VALUE 안의 newline (0x0A) 은 literal `\n` 두 글자로 escape (결정 10).
//   - key 는 사전순으로 정렬 후 작성 — deterministic output.
//   - 파일 모드 0600 (NF-Security-2: group/other 읽기 차단).
//
// 본 함수는 path 의 부모 디렉토리가 이미 존재한다고 가정한다 (worktree 가 그 부모).
func WriteDotenv(path string, env map[string]string) error {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(escapeDotenvValue(env[k]))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("WriteDotenv: %w", err)
	}
	return nil
}

// escapeDotenvValue 는 docker compose 의 single-line dotenv 호환을 위해
// VALUE 안의 실제 newline 을 literal `\n` 두 글자로 변환한다 (결정 10).
//
// 운영자가 textarea 에 직접 입력한 두 글자 `\` + `n` 은 변환 대상이 아니다 — 그쪽은
// VALUE 안에 0x5C 0x6E 두 바이트로 들어와 있을 뿐 0x0A 가 아니므로 본 함수가 손대지 않는다.
//
// 외부에서 paste 등으로 0x0A 가 들어온 경우만 본 escape 가 작동한다.
func escapeDotenvValue(v string) string {
	if !strings.ContainsRune(v, '\n') {
		return v
	}
	return strings.ReplaceAll(v, "\n", `\n`)
}
