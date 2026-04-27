// 이 파일의 책임:
//   - Phase 10 textarea raw text 파싱 / 직렬화 헬퍼.
//   - parseSecretsRaw: 결정 3 / 3a / 3b / 3c 의 정확한 라인 파서.
//   - secretsToRawText: store rows → "KEY=VALUE\n" 묶음 (인덱스 페이지 textarea 표시용).
//   - keyRE: dotenv 호환 KEY 정규식.
//
// admin_ui_repo_secrets.go 와 분리한 이유: NF-File-2 (300 줄 한계) 준수 + 파서가 핸들러와
// 독립적으로 단위 테스트 가능 (TestParseSecretsRawAllRules).
//
// 참고: docs/specs/phase-10-repo-build-secrets.md §3 결정 3a/3b/3c.
package hub

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/lnyarl/preview/internal/store"
)

// keyRE 는 dotenv KEY 호환 정규식 (결정 3). 첫 글자는 알파벳 또는 _, 이후 영숫자/_.
var keyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// secretsToRawText 는 store row 리스트를 textarea 표시용 "KEY=VALUE\n" 묶음으로 변환한다.
// rows 는 store 가 key ASC 로 정렬해 준 결과를 가정 — 추가 정렬 안 함.
func secretsToRawText(rows []store.RepoSecret) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.Key)
		b.WriteByte('=')
		b.WriteString(r.Value)
		b.WriteByte('\n')
	}
	return b.String()
}

// parseSecretsRaw 는 textarea raw text 를 KEY→VALUE 맵으로 파싱한다.
// 결정 3a/3b/3c 의 정확한 정책:
//   - CRLF 호환: 각 라인 끝의 `\r` `\n` 제거.
//   - 라인 전체 trim 후 빈 라인은 무시.
//   - trim 후 첫 글자가 `#` → 코멘트, 무시.
//   - `=` 미포함 라인 → ValidationError "line N: missing '=' separator".
//   - 첫 `=` 위치로 split (VALUE 안의 추가 `=` 보존).
//   - KEY 만 양쪽 trim, KEY 정규식 검증 (결정 3).
//   - VALUE 는 trim 하지 않는다 (선·후행 공백 보존, 결정 3a 단계 8).
//   - `KEY=` 는 합법, value="" 저장.
//
// 같은 KEY 가 중복 등장하면 마지막 줄이 우선 (운영자 의도 추정).
func parseSecretsRaw(raw string) (map[string]string, error) {
	out := map[string]string{}
	// raw 의 \r\n / \n 모두 호환되도록 통일적으로 \n 으로 split.
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	for i, line := range lines {
		lineNum := i + 1
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" {
			continue
		}
		if strings.HasPrefix(trimmedLine, "#") {
			continue
		}
		if !strings.Contains(line, "=") {
			return nil, fmt.Errorf("line %d: missing '=' separator", lineNum)
		}
		parts := strings.SplitN(line, "=", 2)
		// parts 는 항상 길이 2 (위에서 Contains '=' 확인).
		key := strings.TrimSpace(parts[0])
		value := parts[1] // VALUE 는 trim 안 함 (결정 3a 단계 8).
		if key == "" {
			return nil, fmt.Errorf("line %d: empty KEY before '='", lineNum)
		}
		if !keyRE.MatchString(key) {
			return nil, fmt.Errorf("line %d: KEY %q does not match ^[A-Za-z_][A-Za-z0-9_]*$", lineNum, key)
		}
		out[key] = value
	}
	return out, nil
}
