// 이 파일의 책임:
//   - LoadPreviewConfig / FirstService 의 유닛 테스트.
//   - 실제 임시 디렉토리(t.TempDir())에 .preview.yml 을 써서 파싱 흐름을 검증한다.
package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig 는 worktree 에 fileName(예: ".preview.yml") 으로 content 를 쓴다.
func writeConfig(t *testing.T, worktree, fileName, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(worktree, fileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", fileName, err)
	}
}

// reasonContains 는 invalid 에러의 메시지에 reason 토큰이 포함되는지 검사.
func reasonContains(t *testing.T, err error, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", reason)
	}
	if !strings.Contains(err.Error(), reason) {
		t.Fatalf("expected error containing %q, got: %v", reason, err)
	}
}

// F-1: 정상 YAML → services 맵 파싱.
func TestPreviewConfig_F1_ValidYAML(t *testing.T) {
	wt := t.TempDir()
	writeConfig(t, wt, ".preview.yml", `
services:
  frontend:
    port: 3000
    path: /front
    strip: true
  admin:
    port: 8080
    path: /admin
`)
	cfg, err := LoadPreviewConfig(wt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Services) != 2 {
		t.Fatalf("services count = %d, want 2", len(cfg.Services))
	}
	front, ok := cfg.Services["frontend"]
	if !ok {
		t.Fatalf("frontend missing")
	}
	if front.Port != 3000 || front.Path != "/front" || !front.Strip {
		t.Fatalf("frontend mismatch: %+v", front)
	}
	admin, ok := cfg.Services["admin"]
	if !ok {
		t.Fatalf("admin missing")
	}
	if admin.Port != 8080 || admin.Path != "/admin" {
		t.Fatalf("admin mismatch: %+v", admin)
	}
}

// F-2: services 비었거나 nil → services_empty.
func TestPreviewConfig_F2_ServicesEmpty(t *testing.T) {
	cases := map[string]string{
		"nil":   `compose_file: ./foo.yml`,
		"empty": "services: {}\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			wt := t.TempDir()
			// compose_file 이 명시된 경우 nil 케이스에서 build_file_not_found 가 먼저 터지지 않도록
			// nil 케이스는 compose_file 빼고 새로 작성한다.
			content := body
			if name == "nil" {
				content = "# no services key\n"
			}
			writeConfig(t, wt, ".preview.yml", content)
			_, err := LoadPreviewConfig(wt)
			reasonContains(t, err, reasonServicesEmpty)
		})
	}
}

// F-3: service name 대문자/공백/특수문자 → service_name_invalid (3케이스).
func TestPreviewConfig_F3_ServiceNameInvalid(t *testing.T) {
	cases := map[string]string{
		"uppercase": "Frontend",
		"space":     "front end",
		"special":   "front$",
	}
	for name, svcName := range cases {
		t.Run(name, func(t *testing.T) {
			wt := t.TempDir()
			// space/special 은 yaml flow scalar 로 quote 처리.
			body := "services:\n  \"" + svcName + "\":\n    port: 3000\n    path: /x\n"
			writeConfig(t, wt, ".preview.yml", body)
			_, err := LoadPreviewConfig(wt)
			reasonContains(t, err, reasonServiceNameInvalid)
		})
	}
}

// F-4: port 0, 음수, 65536 → port_invalid (경계값).
func TestPreviewConfig_F4_PortInvalid(t *testing.T) {
	cases := map[string]int{
		"zero":     0,
		"negative": -1,
		"over":     65536,
	}
	for name, port := range cases {
		t.Run(name, func(t *testing.T) {
			wt := t.TempDir()
			body := "services:\n  app:\n    port: " + itoa(port) + "\n    path: /x\n"
			writeConfig(t, wt, ".preview.yml", body)
			_, err := LoadPreviewConfig(wt)
			reasonContains(t, err, reasonPortInvalid)
		})
	}
}

// F-5: path "/" 미시작 → path_invalid.
func TestPreviewConfig_F5_PathInvalid(t *testing.T) {
	wt := t.TempDir()
	writeConfig(t, wt, ".preview.yml", `
services:
  app:
    port: 3000
    path: front
`)
	_, err := LoadPreviewConfig(wt)
	reasonContains(t, err, reasonPathInvalid)
}

// F-6: 두 service path 동일 → duplicate_path.
func TestPreviewConfig_F6_DuplicatePath(t *testing.T) {
	wt := t.TempDir()
	writeConfig(t, wt, ".preview.yml", `
services:
  a:
    port: 3000
    path: /same
  b:
    port: 4000
    path: /same
`)
	_, err := LoadPreviewConfig(wt)
	reasonContains(t, err, reasonDuplicatePath)
}

// F-7: strip 생략 시 default true.
func TestPreviewConfig_F7_StripDefaultTrue(t *testing.T) {
	wt := t.TempDir()
	writeConfig(t, wt, ".preview.yml", `
services:
  app:
    port: 3000
    path: /app
`)
	cfg, err := LoadPreviewConfig(wt)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !cfg.Services["app"].Strip {
		t.Fatalf("strip default = true 기대, got false")
	}
}

// F-8: strip:false 명시 시 false 보존.
func TestPreviewConfig_F8_StripFalseExplicit(t *testing.T) {
	wt := t.TempDir()
	writeConfig(t, wt, ".preview.yml", `
services:
  app:
    port: 3000
    path: /app
    strip: false
`)
	cfg, err := LoadPreviewConfig(wt)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.Services["app"].Strip {
		t.Fatalf("strip:false 명시 시 false 기대, got true")
	}
}

// F-9: 잘린 YAML → yaml_parse 포함 에러.
func TestPreviewConfig_F9_YAMLParseError(t *testing.T) {
	wt := t.TempDir()
	// 잘못된 들여쓰기 / 닫히지 않은 구조로 yaml.v3 가 reject 하도록.
	writeConfig(t, wt, ".preview.yml", "services:\n  app:\n    port: 3000\n    path: /x\n   strip: [unclosed\n")
	_, err := LoadPreviewConfig(wt)
	reasonContains(t, err, reasonYAMLParsePrefix)
}

// F-10: 파일 부재 → ErrPreviewConfigMissing.
func TestPreviewConfig_F10_Missing(t *testing.T) {
	wt := t.TempDir()
	_, err := LoadPreviewConfig(wt)
	if !errors.Is(err, ErrPreviewConfigMissing) {
		t.Fatalf("expected ErrPreviewConfigMissing, got %v", err)
	}
}

// F-11: FirstService() 알파벳 오름차순 첫 키 반환 (3 service 섞기).
func TestPreviewConfig_F11_FirstServiceAlphabetical(t *testing.T) {
	wt := t.TempDir()
	writeConfig(t, wt, ".preview.yml", `
services:
  zebra:
    port: 3000
    path: /z
  apple:
    port: 4000
    path: /a
  mango:
    port: 5000
    path: /m
`)
	cfg, err := LoadPreviewConfig(wt)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	name, svc, ok := cfg.FirstService()
	if !ok {
		t.Fatalf("FirstService ok=false")
	}
	if name != "apple" {
		t.Fatalf("first service = %q, want apple", name)
	}
	if svc.Port != 4000 || svc.Path != "/a" {
		t.Fatalf("apple svc mismatch: %+v", svc)
	}

	// 빈 cfg 에서는 ok=false.
	empty := PreviewConfig{}
	if _, _, ok := empty.FirstService(); ok {
		t.Fatalf("empty cfg ok 기대 false")
	}
}

// F-11b: compose_file 명시 시 cfg.ComposeFile 에 그대로 반영.
func TestPreviewConfig_F11b_ComposeFileSet(t *testing.T) {
	wt := t.TempDir()
	// 실제 파일 만들어 build_file_not_found 회피.
	must(t, os.MkdirAll(filepath.Join(wt, "docker"), 0o755))
	writeConfig(t, wt, "docker/docker-compose.yml", "services: {}\n")
	writeConfig(t, wt, ".preview.yml", `
compose_file: ./docker/docker-compose.yml
services:
  app:
    port: 3000
    path: /app
`)
	cfg, err := LoadPreviewConfig(wt)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.ComposeFile != "./docker/docker-compose.yml" {
		t.Fatalf("ComposeFile = %q, want ./docker/docker-compose.yml", cfg.ComposeFile)
	}
}

// F-11c: dockerfile 명시 시 cfg.Dockerfile 에 그대로 반영.
func TestPreviewConfig_F11c_DockerfileSet(t *testing.T) {
	wt := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(wt, "backend"), 0o755))
	writeConfig(t, wt, "backend/Dockerfile", "FROM scratch\n")
	writeConfig(t, wt, ".preview.yml", `
dockerfile: ./backend/Dockerfile
services:
  app:
    port: 3000
    path: /app
`)
	cfg, err := LoadPreviewConfig(wt)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.Dockerfile != "./backend/Dockerfile" {
		t.Fatalf("Dockerfile = %q, want ./backend/Dockerfile", cfg.Dockerfile)
	}
}

// F-11d: compose_file 명시 + 파일 미존재 → build_file_not_found.
func TestPreviewConfig_F11d_ComposeFileMissing(t *testing.T) {
	wt := t.TempDir()
	writeConfig(t, wt, ".preview.yml", `
compose_file: ./does/not/exist.yml
services:
  app:
    port: 3000
    path: /app
`)
	_, err := LoadPreviewConfig(wt)
	reasonContains(t, err, reasonBuildFileNotFound)
}

// F-11e: compose_file + dockerfile 둘 다 명시 시 둘 다 파싱됨
// (우선순위 결정은 Runner 단계 — 파서는 둘 다 저장).
func TestPreviewConfig_F11e_BothBuildFiles(t *testing.T) {
	wt := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(wt, "docker"), 0o755))
	must(t, os.MkdirAll(filepath.Join(wt, "backend"), 0o755))
	writeConfig(t, wt, "docker/docker-compose.yml", "services: {}\n")
	writeConfig(t, wt, "backend/Dockerfile", "FROM scratch\n")
	writeConfig(t, wt, ".preview.yml", `
compose_file: ./docker/docker-compose.yml
dockerfile: ./backend/Dockerfile
services:
  app:
    port: 3000
    path: /app
`)
	cfg, err := LoadPreviewConfig(wt)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.ComposeFile == "" || cfg.Dockerfile == "" {
		t.Fatalf("둘 다 채워져야 함: compose=%q dockerfile=%q", cfg.ComposeFile, cfg.Dockerfile)
	}
}

// 보조: 파일명 탐색 우선순위 검증 — 기획서 명시는 없지만 안전망 역할.
// .preview.yaml 만 있을 때도 정상 파싱되어야 한다.
func TestPreviewConfig_PreviewYamlFallback(t *testing.T) {
	wt := t.TempDir()
	writeConfig(t, wt, ".preview.yaml", `
services:
  app:
    port: 3000
    path: /app
`)
	cfg, err := LoadPreviewConfig(wt)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if _, ok := cfg.Services["app"]; !ok {
		t.Fatalf("app service missing")
	}
}

// --- helpers (테스트 전용) ---

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
}

// strconv 의존을 피해 작은 정수만 처리하는 단순 itoa.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
