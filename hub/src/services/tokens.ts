import { randomBytes } from 'node:crypto';
import bcrypt from 'bcryptjs';
import { TOKEN_PREFIX, TOKEN_PREFIX_LOOKUP_LEN, TOKEN_RANDOM_BYTES } from '@preview/shared';

const TOKEN_BODY_LEN = 43; // base64url(32 bytes) = 43 chars, no padding.
const TOKEN_TOTAL_LEN = TOKEN_PREFIX.length + TOKEN_BODY_LEN; // "agt_" + 43 = 47

function base64url(buf: Buffer): string {
  return buf.toString('base64').replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '');
}

export function generateToken(): string {
  return TOKEN_PREFIX + base64url(randomBytes(TOKEN_RANDOM_BYTES));
}

export function extractLookupPrefix(token: string): string {
  return token.slice(0, TOKEN_PREFIX_LOOKUP_LEN);
}

export function isWellFormedToken(token: string): boolean {
  return (
    token.length === TOKEN_TOTAL_LEN &&
    token.startsWith(TOKEN_PREFIX) &&
    /^[A-Za-z0-9_-]+$/.test(token.slice(TOKEN_PREFIX.length))
  );
}

export async function hashToken(token: string, cost: number): Promise<string> {
  return bcrypt.hash(token, cost);
}

export async function verifyToken(token: string, hash: string): Promise<boolean> {
  return bcrypt.compare(token, hash);
}
