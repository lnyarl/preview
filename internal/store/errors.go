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
