// Package token handles Agent 인증 토큰의 생성·해싱·검증.
//
// 이 파일의 책임:
//   - Generate: "agt_" + base64url(32바이트 crypto/rand). 256비트 엔트로피.
//   - Hash: bcrypt.GenerateFromPassword. cost 기본 10.
//   - Verify: bcrypt.CompareHashAndPassword (상수 시간 근접).
//
// 보안 주의: 생성된 plain 토큰은 어떤 경로로도 로그에 기록되어서는 안 된다
// (NF-Security-2). 이 패키지의 API 사용자는 raw 값을 곧바로 hash 한 뒤
// HTTP 응답에 1회 노출하고 메모리에서 놓아 주어야 한다.
package token

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Prefix 는 토큰 문자열의 공통 prefix.
const Prefix = "agt_"

// DefaultCost 는 bcrypt 기본 cost(결정 3).
const DefaultCost = 10

// RandomBytes 는 토큰의 바이트 길이.
const RandomBytes = 32

// ErrInvalidToken 은 Verify 입력이 비정상적 형식일 때 반환된다.
var ErrInvalidToken = errors.New("token: invalid token format")

// Generator 는 토큰 생성·해싱 도구.
// cost 는 bcrypt GenerateFromPassword 에 사용된다.
type Generator struct {
	cost int
}

// NewGenerator 는 주어진 bcrypt cost 로 Generator 를 만든다.
// cost <= 0 이면 DefaultCost(10) 사용.
func NewGenerator(cost int) *Generator {
	if cost <= 0 {
		cost = DefaultCost
	}
	return &Generator{cost: cost}
}

// Generate 는 새 raw 토큰과 bcrypt 해시를 함께 반환한다.
// raw 는 호출자가 단 한 번만 응답에 싣고 잊어야 한다.
func (g *Generator) Generate() (raw string, hash string, err error) {
	b := make([]byte, RandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("token.Generate: rand: %w", err)
	}
	raw = Prefix + base64.RawURLEncoding.EncodeToString(b)
	hashBytes, err := bcrypt.GenerateFromPassword([]byte(raw), g.cost)
	if err != nil {
		return "", "", fmt.Errorf("token.Generate: bcrypt: %w", err)
	}
	return raw, string(hashBytes), nil
}

// Hash 는 주어진 raw 토큰에 대한 bcrypt 해시를 만든다.
// 테스트 용도. 프로덕션 코드는 Generate 를 사용하라.
func (g *Generator) Hash(raw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(raw), g.cost)
	if err != nil {
		return "", fmt.Errorf("token.Hash: %w", err)
	}
	return string(h), nil
}

// Verify 는 raw 토큰과 해시가 일치하는지 확인한다.
// 내부적으로 bcrypt.CompareHashAndPassword 를 사용하므로 상수 시간 근접 비교.
func Verify(raw, hash string) bool {
	if !strings.HasPrefix(raw, Prefix) {
		return false
	}
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(raw))
	return err == nil
}
