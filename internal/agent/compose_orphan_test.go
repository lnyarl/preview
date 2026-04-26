// 이 파일의 책임:
//   - PruneComposeOrphans 의 단위 테스트 (F-7 ~ F-17).
//   - fakeOrphanCmd 는 docker compose ls 응답 / down 호출을 격리해 시뮬레이트한다.
//     기존 runner_test.go 의 fakeCmd 와 이름 충돌을 피하기 위해 별도 타입.
//   - parseComposeLs 의 직접 테스트(F-16) 도 함께 포함.
//
// 참고: docs/specs/phase-8-orphan-cleanup.md §4-2, §6-2.
package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

// fakeOrphanCmd 는 Output 응답과 Run 호출 기록을 가진다.
//   - lsOut/lsErr 는 Output 호출에 대한 응답 (compose ls).
//   - downCmds 는 Run 호출 시 [name, args...] 를 그대로 보존.
//   - downErrs 는 project name → error 매핑 (지정된 프로젝트의 down 만 실패).
type fakeOrphanCmd struct {
	lsOut    string
	lsErr    error
	downCmds [][]string
	downErrs map[string]error
}

func (f *fakeOrphanCmd) Run(_ context.Context, name string, args ...string) error {
	f.downCmds = append(f.downCmds, append([]string{name}, args...))
	// args 패턴: ["compose","--project-name", projectName, "down"]
	if len(args) >= 3 {
		if e, ok := f.downErrs[args[2]]; ok {
			return e
		}
	}
	return nil
}

func (f *fakeOrphanCmd) Output(_ context.Context, _ string, _ ...string) (string, error) {
	return f.lsOut, f.lsErr
}

func newOrphanDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newOrphanRunner 는 RunningPreviewIDs() 만 사용되는 가벼운 Runner 를 만든다.
// docker/cache/hub 등 다른 의존은 주입하지 않는다 (PruneComposeOrphans 비참조).
func newOrphanRunner(t *testing.T, activeIDs ...string) *Runner {
	t.Helper()
	r := NewRunner(nil, nil, nil, nil, "127.0.0.1", newOrphanDiscardLogger())
	for _, pid := range activeIDs {
		r.RegisterRestoredJob(pid, "", "127.0.0.1", 0)
	}
	return r
}

// F-7: compose ls 가 [] 반환 → (0, nil), Run 호출 0.
func TestPruneComposeOrphansEmptyList(t *testing.T) {
	cmd := &fakeOrphanCmd{lsOut: "[]"}
	runner := newOrphanRunner(t)

	n, err := PruneComposeOrphans(context.Background(), cmd, runner, newOrphanDiscardLogger())
	if err != nil {
		t.Fatalf("err=%v want nil", err)
	}
	if n != 0 {
		t.Fatalf("pruned=%d want 0", n)
	}
	if got := len(cmd.downCmds); got != 0 {
		t.Fatalf("down call count=%d want 0", got)
	}
}

// F-8: orphan 1 + active 1 → 1 down + 5 토큰 인자.
func TestPruneComposeOrphansOneOrphanOneActive(t *testing.T) {
	cmd := &fakeOrphanCmd{lsOut: `[{"Name":"preview-p1"},{"Name":"preview-p2"}]`}
	runner := newOrphanRunner(t, "p1")

	n, err := PruneComposeOrphans(context.Background(), cmd, runner, newOrphanDiscardLogger())
	if err != nil {
		t.Fatalf("err=%v want nil", err)
	}
	if n != 1 {
		t.Fatalf("pruned=%d want 1", n)
	}
	if got := len(cmd.downCmds); got != 1 {
		t.Fatalf("down call count=%d want 1", got)
	}
	want := []string{"docker", "compose", "--project-name", "preview-p2", "down"}
	got := cmd.downCmds[0]
	if len(got) != len(want) {
		t.Fatalf("down args len=%d want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("down args[%d]=%q want %q (full=%v)", i, got[i], want[i], got)
		}
	}
}

// F-9: compose ls 자체가 에러 → (0, err), Run 호출 0.
func TestPruneComposeOrphansLsError(t *testing.T) {
	lsErr := errors.New("docker compose ls boom")
	cmd := &fakeOrphanCmd{lsErr: lsErr}
	runner := newOrphanRunner(t)

	n, err := PruneComposeOrphans(context.Background(), cmd, runner, newOrphanDiscardLogger())
	if err == nil {
		t.Fatalf("err=nil want non-nil")
	}
	if !errors.Is(err, lsErr) {
		t.Fatalf("err=%v want wraps %v", err, lsErr)
	}
	if n != 0 {
		t.Fatalf("pruned=%d want 0", n)
	}
	if got := len(cmd.downCmds); got != 0 {
		t.Fatalf("down call count=%d want 0", got)
	}
}

// F-10: compose ls 출력이 잘못된 JSON → (0, err), Run 호출 0.
func TestPruneComposeOrphansInvalidJSON(t *testing.T) {
	cmd := &fakeOrphanCmd{lsOut: "not-json"}
	runner := newOrphanRunner(t)

	n, err := PruneComposeOrphans(context.Background(), cmd, runner, newOrphanDiscardLogger())
	if err == nil {
		t.Fatalf("err=nil want non-nil")
	}
	if n != 0 {
		t.Fatalf("pruned=%d want 0", n)
	}
	if got := len(cmd.downCmds); got != 0 {
		t.Fatalf("down call count=%d want 0", got)
	}
}

// F-11: down 1 건 실패해도 나머지 계속 — (1, firstErr) + 두 down 호출 모두 발생.
func TestPruneComposeOrphansDownPartialFailure(t *testing.T) {
	downErr := errors.New("compose down boom")
	cmd := &fakeOrphanCmd{
		lsOut:    `[{"Name":"preview-p1"},{"Name":"preview-p2"}]`,
		downErrs: map[string]error{"preview-p1": downErr},
	}
	runner := newOrphanRunner(t)

	n, err := PruneComposeOrphans(context.Background(), cmd, runner, newOrphanDiscardLogger())
	if err == nil {
		t.Fatalf("err=nil want non-nil")
	}
	if !errors.Is(err, downErr) {
		t.Fatalf("err=%v want = %v (firstErr 보관)", err, downErr)
	}
	if n != 1 {
		t.Fatalf("pruned=%d want 1 (성공한 1 건만 카운트)", n)
	}
	if got := len(cmd.downCmds); got != 2 {
		t.Fatalf("down call count=%d want 2 (실패해도 다음 항목 계속)", got)
	}
}

// F-12: 접두사 미일치(other-app) 무시.
func TestPruneComposeOrphansIgnoresNonPreviewPrefix(t *testing.T) {
	cmd := &fakeOrphanCmd{lsOut: `[{"Name":"preview-p1"},{"Name":"other-app"}]`}
	runner := newOrphanRunner(t)

	n, err := PruneComposeOrphans(context.Background(), cmd, runner, newOrphanDiscardLogger())
	if err != nil {
		t.Fatalf("err=%v want nil", err)
	}
	if n != 1 {
		t.Fatalf("pruned=%d want 1", n)
	}
	if got := len(cmd.downCmds); got != 1 {
		t.Fatalf("down call count=%d want 1 (other-app 미포함)", got)
	}
	if cmd.downCmds[0][3] != "preview-p1" {
		t.Fatalf("down project=%q want preview-p1", cmd.downCmds[0][3])
	}
}

// F-13: cmd == nil 또는 runner == nil → (0, nil), 패닉 없음.
func TestPruneComposeOrphansNilGuard(t *testing.T) {
	t.Run("nil cmd", func(t *testing.T) {
		runner := newOrphanRunner(t)
		n, err := PruneComposeOrphans(context.Background(), nil, runner, newOrphanDiscardLogger())
		if err != nil || n != 0 {
			t.Fatalf("nil cmd → (n=%d, err=%v) want (0, nil)", n, err)
		}
	})
	t.Run("nil runner", func(t *testing.T) {
		cmd := &fakeOrphanCmd{lsOut: "[]"}
		n, err := PruneComposeOrphans(context.Background(), cmd, nil, newOrphanDiscardLogger())
		if err != nil || n != 0 {
			t.Fatalf("nil runner → (n=%d, err=%v) want (0, nil)", n, err)
		}
		if got := len(cmd.downCmds); got != 0 {
			t.Fatalf("nil runner → down call count=%d want 0", got)
		}
	})
}

// F-14: compose ls 출력이 빈 문자열 → (0, nil), Run 호출 0.
func TestPruneComposeOrphansEmptyOutput(t *testing.T) {
	cmd := &fakeOrphanCmd{lsOut: ""}
	runner := newOrphanRunner(t)

	n, err := PruneComposeOrphans(context.Background(), cmd, runner, newOrphanDiscardLogger())
	if err != nil {
		t.Fatalf("err=%v want nil", err)
	}
	if n != 0 {
		t.Fatalf("pruned=%d want 0", n)
	}
	if got := len(cmd.downCmds); got != 0 {
		t.Fatalf("down call count=%d want 0", got)
	}
}

// F-15: 알 수 없는 추가 필드 포함 JSON → 정상 파싱 (forward-compat).
func TestPruneComposeOrphansForwardCompatExtraFields(t *testing.T) {
	cmd := &fakeOrphanCmd{
		lsOut: `[{"Name":"preview-p1","Status":"running(2)","ConfigFiles":"/x.yml","FutureField":42}]`,
	}
	runner := newOrphanRunner(t)

	n, err := PruneComposeOrphans(context.Background(), cmd, runner, newOrphanDiscardLogger())
	if err != nil {
		t.Fatalf("err=%v want nil (extra field 는 encoding/json 이 무시)", err)
	}
	if n != 1 {
		t.Fatalf("pruned=%d want 1", n)
	}
}

// F-16: parseComposeLs 직접 테스트 — "[]\n" trailing newline → (nil, nil).
func TestParseComposeLsTrailingNewline(t *testing.T) {
	ps, err := parseComposeLs("[]\n")
	if err != nil {
		t.Fatalf("err=%v want nil", err)
	}
	if ps != nil {
		t.Fatalf("ps=%v want nil", ps)
	}
}

// F-17: active 매핑이 "preview-"+id 형태 — runner 에 abc 등록, ls 응답에 preview-abc → down 호출 0.
func TestPruneComposeOrphansActiveMappingPrefix(t *testing.T) {
	cmd := &fakeOrphanCmd{lsOut: `[{"Name":"preview-abc"}]`}
	runner := newOrphanRunner(t, "abc")

	n, err := PruneComposeOrphans(context.Background(), cmd, runner, newOrphanDiscardLogger())
	if err != nil {
		t.Fatalf("err=%v want nil", err)
	}
	if n != 0 {
		t.Fatalf("pruned=%d want 0 (abc 는 active)", n)
	}
	if got := len(cmd.downCmds); got != 0 {
		t.Fatalf("down call count=%d want 0", got)
	}
}
