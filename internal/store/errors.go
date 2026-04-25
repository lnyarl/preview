// 이 파일의 책임:
//   - store 패키지 공용 sentinel 에러 선언
//
// 호출부는 errors.Is 로 에러 종류를 분기한다.
package store

import "errors"

// ErrNotFound 는 조회 대상이 없을 때 반환된다. HTTP 매핑은 404.
var ErrNotFound = errors.New("store: not found")

// ErrDuplicate 는 UNIQUE 제약 위반 시 반환된다. HTTP 매핑은 409.
var ErrDuplicate = errors.New("store: duplicate")

// ErrStaleState 는 UpdateStatus 의 fromStatus CAS 가드가 실패했을 때 반환된다.
// HTTP 매핑은 409 (현재 상태가 호출자의 기대와 다름).
var ErrStaleState = errors.New("store: stale state")

// ErrNotImplementedStep1 은 Phase 2 Step 1 에서 stub 으로 남긴 PreviewStore
// 메서드를 호출했을 때 반환된다. Step 2/3 에서 실제 구현으로 대체되며 단위
// 테스트에서 의도적으로 호출하지 않는다.
var ErrNotImplementedStep1 = errors.New("store: not implemented in step 1")
