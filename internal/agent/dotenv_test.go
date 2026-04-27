// 이 파일의 책임:
//   - WriteDotenv 의 단위 테스트 묶음 (Phase 10 F-22 ~ F-24, NF-Security-2).
//   - normal case: KEY 정렬 + KEY=VALUE\n 직렬화.
//   - nil / empty env: 파일 미생성 (기존 .env 보존, F-23).
//   - escape: value 안의 newline (0x0A) 이 literal `\n` 두 글자로 변환 (F-24).
//   - 파일 모드 0600 (NF-Security-2).
package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteDotenvNormal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	env := map[string]string{
		"DATABASE_URL": "postgres://app:pw@db:5432/app",
		"API_KEY":      "sk-xxxxxxxxxxxx",
		"FEATURE_FLAG": "on",
	}
	if err := WriteDotenv(path, env); err != nil {
		t.Fatalf("WriteDotenv: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// key 사전순 정렬: API_KEY, DATABASE_URL, FEATURE_FLAG.
	want := "API_KEY=sk-xxxxxxxxxxxx\nDATABASE_URL=postgres://app:pw@db:5432/app\nFEATURE_FLAG=on\n"
	if string(got) != want {
		t.Fatalf("content mismatch\nwant: %q\ngot:  %q", want, string(got))
	}
}

func TestWriteDotenvNilEnvSkips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	// 사전 작성된 .env 를 두고 — 호출 후 그대로 보존되어야 한다.
	pre := []byte("PRE_EXISTING=keep\n")
	if err := os.WriteFile(path, pre, 0o600); err != nil {
		t.Fatalf("pre-write: %v", err)
	}
	if err := WriteDotenv(path, nil); err != nil {
		t.Fatalf("WriteDotenv nil: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(pre) {
		t.Fatalf("file modified despite nil env: got=%q", string(got))
	}
}

func TestWriteDotenvEmptyMapSkips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	// 파일이 아예 없는 상태에서 빈 map 호출 → 파일 미생성.
	if err := WriteDotenv(path, map[string]string{}); err != nil {
		t.Fatalf("WriteDotenv empty: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file should not exist; stat err=%v", err)
	}
}

func TestWriteDotenvEscapeNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	env := map[string]string{
		"PEM": "line1\nline2\nline3",
	}
	if err := WriteDotenv(path, env); err != nil {
		t.Fatalf("WriteDotenv: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// 실제 0x0A 는 literal `\n` (2 byte: 0x5C 0x6E) 로 변환되어야 함.
	want := `PEM=line1\nline2\nline3` + "\n"
	if string(got) != want {
		t.Fatalf("escape mismatch\nwant: %q\ngot:  %q", want, string(got))
	}
}

func TestWriteDotenvFileMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := WriteDotenv(path, map[string]string{"K": "v"}); err != nil {
		t.Fatalf("WriteDotenv: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	mode := info.Mode().Perm()
	if runtime.GOOS == "windows" {
		// Windows 의 NTFS 권한 매핑은 Unix 0600 보장이 어렵다 — 모드 비검증, 파일 존재만 확인.
		// 본 케이스는 Linux/macOS 의 NF-Security-2 검증을 위한 것이며 Windows 는 OS-level ACL 의존.
		t.Skipf("skipping 0600 perm check on windows (mode=%o)", mode)
	}
	if mode != 0o600 {
		t.Fatalf("file mode = %o want 0o600", mode)
	}
}

func TestEscapeDotenvValueNoNewline(t *testing.T) {
	in := "value#tag=base64==/?q=1&r=2"
	if got := escapeDotenvValue(in); got != in {
		t.Fatalf("no-op escape changed content\nin:  %q\nout: %q", in, got)
	}
}

func TestEscapeDotenvValueWithNewline(t *testing.T) {
	in := "a\nb"
	want := `a\nb`
	if got := escapeDotenvValue(in); got != want {
		t.Fatalf("escape\nwant: %q\ngot:  %q", want, got)
	}
}
