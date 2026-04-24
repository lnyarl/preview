import { describe, expect, it } from 'vitest';
import {
  ClientMessage,
  DisconnectMsg,
  HelloMsg,
  PingMsg,
  PongMsg,
  ServerMessage,
  WelcomeMsg,
} from '../messages.js';
import { PROTOCOL_VERSION } from '../constants.js';

describe('ClientMessage', () => {
  it('accepts a valid HELLO', () => {
    const msg = {
      type: 'HELLO' as const,
      version: PROTOCOL_VERSION,
      labels: { zone: 'home' },
      agentVersion: '0.0.0',
      sentAt: Date.now(),
    };
    const parsed = ClientMessage.parse(msg);
    expect(parsed.type).toBe('HELLO');
  });

  it('rejects HELLO with missing version', () => {
    const bad = { type: 'HELLO', labels: {}, agentVersion: '0', sentAt: 0 };
    expect(ClientMessage.safeParse(bad).success).toBe(false);
  });

  it('accepts PONG', () => {
    const msg = { type: 'PONG' as const, nonce: 'abc', sentAt: 0 };
    expect(ClientMessage.parse(msg)).toEqual(msg);
  });

  it('rejects label key > 64 chars', () => {
    const bad = {
      type: 'HELLO',
      version: 1,
      labels: { ['x'.repeat(65)]: 'v' },
      agentVersion: '0',
      sentAt: 0,
    };
    expect(HelloMsg.safeParse(bad).success).toBe(false);
  });
});

describe('ServerMessage', () => {
  it('accepts a valid WELCOME (nanoid 21 chars)', () => {
    const msg = {
      type: 'WELCOME' as const,
      agentId: 'V1StGXR8_Z5jdHi6B-myT',
      serverVersion: PROTOCOL_VERSION,
      heartbeatIntervalMs: 30_000,
      sentAt: Date.now(),
    };
    const parsed = ServerMessage.parse(msg);
    expect(parsed.type).toBe('WELCOME');
  });

  it('rejects WELCOME with bad agentId', () => {
    const bad = {
      type: 'WELCOME',
      agentId: 'short',
      serverVersion: 1,
      heartbeatIntervalMs: 30_000,
      sentAt: 0,
    };
    expect(WelcomeMsg.safeParse(bad).success).toBe(false);
  });

  it('accepts PING/PONG round-trip shape', () => {
    const ping: PingMsg = { type: 'PING', nonce: 'n', sentAt: 1 };
    const pong: PongMsg = { type: 'PONG', nonce: ping.nonce, sentAt: 2 };
    expect(PingMsg.parse(ping)).toEqual(ping);
    expect(PongMsg.parse(pong)).toEqual(pong);
  });

  it('accepts DISCONNECT with known reason', () => {
    const msg: DisconnectMsg = {
      type: 'DISCONNECT',
      reason: 'HEARTBEAT_TIMEOUT',
      sentAt: 0,
    };
    expect(DisconnectMsg.parse(msg)).toEqual(msg);
  });

  it('rejects DISCONNECT with unknown reason', () => {
    const bad = { type: 'DISCONNECT', reason: 'NOPE', sentAt: 0 };
    expect(DisconnectMsg.safeParse(bad).success).toBe(false);
  });
});
