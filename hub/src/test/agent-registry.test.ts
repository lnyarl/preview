import { describe, expect, it } from 'vitest';
import { AgentRegistry } from '../services/agent-registry.js';

function fakeWs(): { send: () => void; close: () => void } {
  return { send: () => {}, close: () => {} };
}

describe('AgentRegistry', () => {
  it('register returns previous connection', () => {
    const r = new AgentRegistry();
    const c1 = { ws: fakeWs(), connId: 'c1', connectedAt: 1 };
    const c2 = { ws: fakeWs(), connId: 'c2', connectedAt: 2 };

    expect(r.register('a1', c1)).toBeNull();
    expect(r.register('a1', c2)).toBe(c1);
    expect(r.get('a1')?.connId).toBe('c2');
  });

  it('removeIfCurrent only removes matching connId', () => {
    const r = new AgentRegistry();
    const c1 = { ws: fakeWs(), connId: 'c1', connectedAt: 1 };
    const c2 = { ws: fakeWs(), connId: 'c2', connectedAt: 2 };
    r.register('a1', c1);
    r.register('a1', c2);

    expect(r.removeIfCurrent('a1', 'c1')).toBe(false); // stale close
    expect(r.get('a1')?.connId).toBe('c2');
    expect(r.removeIfCurrent('a1', 'c2')).toBe(true);
    expect(r.get('a1')).toBeUndefined();
  });
});
