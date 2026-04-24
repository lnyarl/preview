import {
  ClientMessage,
  HELLO_TIMEOUT_MS,
  HEARTBEAT_INTERVAL_MS,
  MAX_MESSAGE_BYTES,
  PROTOCOL_VERSION,
  type DisconnectReason,
  type ServerMessage,
} from '@preview/shared';
import { nanoid } from 'nanoid';
import type { FastifyBaseLogger } from 'fastify';
import type { AgentsRepo } from '../repos/agents.js';
import type { AgentRegistry, WebSocketLike } from '../services/agent-registry.js';
import { startHeartbeat } from './heartbeat.js';

export interface HandlerDeps {
  agentsRepo: AgentsRepo;
  registry: AgentRegistry;
  log: FastifyBaseLogger;
  heartbeatIntervalMs?: number;
  heartbeatMissLimit?: number;
}

function send(ws: WebSocketLike, msg: ServerMessage): void {
  try {
    ws.send(JSON.stringify(msg));
  } catch {
    /* socket likely closing */
  }
}

function disconnect(ws: WebSocketLike, reason: DisconnectReason, code = 1008): void {
  send(ws, { type: 'DISCONNECT', reason, sentAt: Date.now() });
  try {
    ws.close(code, reason);
  } catch {
    /* ignore */
  }
}

export function wire(
  deps: HandlerDeps,
  ws: WebSocketLike,
  agentId: string,
): {
  onRawMessage(raw: Buffer | string): Promise<void>;
  onClose(): Promise<void>;
} {
  const { agentsRepo, registry, log } = deps;
  const connId = nanoid(10);
  const scopedLog = log.child({ agentId, connId });

  let helloReceived = false;
  let heartbeat: ReturnType<typeof startHeartbeat> | null = null;

  const helloTimer = setTimeout(() => {
    if (!helloReceived) {
      scopedLog.warn('HELLO timeout');
      disconnect(ws, 'PROTOCOL_ERROR', 1002);
    }
  }, HELLO_TIMEOUT_MS);

  async function onRawMessage(raw: Buffer | string): Promise<void> {
    let text: string;
    if (typeof raw === 'string') {
      text = raw;
    } else {
      if (raw.byteLength > MAX_MESSAGE_BYTES) {
        disconnect(ws, 'PROTOCOL_ERROR', 1009);
        return;
      }
      text = raw.toString('utf8');
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch {
      disconnect(ws, 'PROTOCOL_ERROR', 1002);
      return;
    }
    const result = ClientMessage.safeParse(parsed);
    if (!result.success) {
      scopedLog.warn({ issues: result.error.issues }, 'schema mismatch');
      disconnect(ws, 'PROTOCOL_ERROR', 1002);
      return;
    }
    const msg = result.data;

    if (msg.type === 'HELLO') {
      if (helloReceived) {
        disconnect(ws, 'PROTOCOL_ERROR', 1002);
        return;
      }
      if (msg.version !== PROTOCOL_VERSION) {
        disconnect(ws, 'INCOMPATIBLE_VERSION', 1002);
        return;
      }
      helloReceived = true;
      clearTimeout(helloTimer);

      const prev = registry.register(agentId, { ws, connId, connectedAt: Date.now() });
      if (prev && prev.connId !== connId) {
        send(prev.ws, { type: 'DISCONNECT', reason: 'REPLACED', sentAt: Date.now() });
        try {
          prev.ws.close(1008, 'REPLACED');
        } catch {
          /* ignore */
        }
      }

      try {
        await agentsRepo.markOnline(agentId, Date.now(), msg.labels);
      } catch (err) {
        scopedLog.error({ err }, 'failed to mark online');
        disconnect(ws, 'PROTOCOL_ERROR', 1011);
        return;
      }

      const heartbeatMs = deps.heartbeatIntervalMs ?? HEARTBEAT_INTERVAL_MS;
      send(ws, {
        type: 'WELCOME',
        agentId,
        serverVersion: PROTOCOL_VERSION,
        heartbeatIntervalMs: heartbeatMs,
        sentAt: Date.now(),
      });

      heartbeat = startHeartbeat({
        intervalMs: deps.heartbeatIntervalMs,
        missLimit: deps.heartbeatMissLimit,
        log: scopedLog,
        onTimeout: () => disconnect(ws, 'HEARTBEAT_TIMEOUT', 1011),
        send: (pingMsg) => send(ws, pingMsg),
      });
      scopedLog.info({ labels: msg.labels }, 'agent online');
      return;
    }

    if (msg.type === 'PONG') {
      heartbeat?.recordPong(msg.nonce);
      await agentsRepo.touchLastSeen(agentId, Date.now()).catch(() => {});
      return;
    }

    if (msg.type === 'READY') {
      return;
    }
  }

  async function onClose(): Promise<void> {
    clearTimeout(helloTimer);
    heartbeat?.stop();
    const removed = registry.removeIfCurrent(agentId, connId);
    if (removed) {
      try {
        await agentsRepo.setOfflineIfCurrent(agentId, true);
        scopedLog.info('agent offline');
      } catch (err) {
        scopedLog.warn({ err }, 'failed to mark offline on close');
      }
    }
  }

  return { onRawMessage, onClose };
}
