import { describe, expect, it } from 'vitest';
import {
  extractLookupPrefix,
  generateToken,
  hashToken,
  isWellFormedToken,
  verifyToken,
} from '../services/tokens.js';
import { TOKEN_PREFIX, TOKEN_PREFIX_LOOKUP_LEN } from '@preview/shared';

describe('tokens', () => {
  it('generates a 47-char agt_ token', () => {
    const t = generateToken();
    expect(t).toMatch(/^agt_[A-Za-z0-9_-]{43}$/);
    expect(t.length).toBe(47);
    expect(isWellFormedToken(t)).toBe(true);
  });

  it('produces distinct tokens', () => {
    const a = generateToken();
    const b = generateToken();
    expect(a).not.toBe(b);
  });

  it('lookup prefix is 12 chars and starts with agt_', () => {
    const t = generateToken();
    const p = extractLookupPrefix(t);
    expect(p.length).toBe(TOKEN_PREFIX_LOOKUP_LEN);
    expect(p.startsWith(TOKEN_PREFIX)).toBe(true);
  });

  it('rejects malformed tokens', () => {
    expect(isWellFormedToken('agt_short')).toBe(false);
    expect(isWellFormedToken('bad_' + 'a'.repeat(43))).toBe(false);
    expect(isWellFormedToken('agt_' + '!'.repeat(43))).toBe(false);
  });

  it('hash + verify round-trip', async () => {
    const t = generateToken();
    const hash = await hashToken(t, 4); // low cost for test speed
    expect(hash).toMatch(/^\$2[aby]\$/);
    expect(await verifyToken(t, hash)).toBe(true);
    expect(await verifyToken('agt_' + 'x'.repeat(43), hash)).toBe(false);
  });
});
