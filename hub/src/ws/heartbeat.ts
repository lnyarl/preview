import {
  HEARTBEAT_INTERVAL_MS,
  HEARTBEAT_MISS_LIMIT,
  HEARTBEAT_TIMEOUT_MS,
  type PingMsg,
} from '@preview/shared';
import { nanoid } from 'nanoid';
import type { FastifyBaseLogger } from 'fastify';

export interface HeartbeatControl {
  stop(): void;
  recordPong(nonce: string): void;
}

export interface HeartbeatOptions {
  intervalMs?: number;
  timeoutMs?: number;
  missLimit?: number;
  log: FastifyBaseLogger;
  onTimeout: () => void;
  send(msg: PingMsg): void;
}

export function startHeartbeat(opts: HeartbeatOptions): HeartbeatControl {
  const interval = opts.intervalMs ?? HEARTBEAT_INTERVAL_MS;
  const timeout = opts.timeoutMs ?? HEARTBEAT_TIMEOUT_MS;
  const missLimit = opts.missLimit ?? HEARTBEAT_MISS_LIMIT;

  let missedInARow = 0;
  let pendingNonce: string | null = null;
  let pendingTimer: NodeJS.Timeout | null = null;
  let stopped = false;

  const clearPending = (): void => {
    if (pendingTimer) {
      clearTimeout(pendingTimer);
      pendingTimer = null;
    }
    pendingNonce = null;
  };

  const tick = (): void => {
    if (stopped) return;
    const nonce = nanoid(16);
    pendingNonce = nonce;
    try {
      opts.send({ type: 'PING', nonce, sentAt: Date.now() });
    } catch (err) {
      opts.log.warn({ err }, 'failed to send PING');
    }
    pendingTimer = setTimeout(() => {
      missedInARow += 1;
      pendingNonce = null;
      opts.log.warn({ missedInARow, missLimit }, 'PONG timeout');
      if (missedInARow >= missLimit) {
        opts.onTimeout();
      }
    }, timeout);
  };

  const intervalTimer = setInterval(tick, interval);
  // Optional: first PING after one interval (not immediately) to avoid immediate cycle.

  return {
    stop(): void {
      stopped = true;
      clearInterval(intervalTimer);
      clearPending();
    },
    recordPong(nonce: string): void {
      if (pendingNonce && pendingNonce === nonce) {
        missedInARow = 0;
        clearPending();
      }
    },
  };
}
