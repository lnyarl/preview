import type { FastifyInstance, FastifyRequest } from 'fastify';
import type { WebSocket } from '@fastify/websocket';
import { MAX_MESSAGE_BYTES } from '@preview/shared';
import type { AgentsRepo } from '../repos/agents.js';
import type { AgentRegistry } from '../services/agent-registry.js';
import { extractLookupPrefix, isWellFormedToken, verifyToken } from '../services/tokens.js';
import { wire } from '../ws/handler.js';

export interface AgentWsDeps {
  agentsRepo: AgentsRepo;
  registry: AgentRegistry;
  heartbeatIntervalMs?: number;
  heartbeatMissLimit?: number;
}

declare module 'fastify' {
  interface FastifyRequest {
    agentId?: string;
  }
}

export async function registerAgentWs(app: FastifyInstance, deps: AgentWsDeps): Promise<void> {
  const { agentsRepo, registry } = deps;

  app.get(
    '/agent/ws',
    {
      websocket: true,
      preValidation: async (req, reply) => {
        const auth = req.headers['authorization'];
        if (typeof auth !== 'string' || !auth.startsWith('Bearer ')) {
          await reply.code(401).send({ error: 'unauthorized' });
          return;
        }
        const token = auth.slice('Bearer '.length).trim();
        if (!isWellFormedToken(token)) {
          await reply.code(401).send({ error: 'unauthorized' });
          return;
        }
        const prefix = extractLookupPrefix(token);
        const row = await agentsRepo.findByTokenPrefix(prefix);
        if (!row) {
          await reply.code(401).send({ error: 'unauthorized' });
          return;
        }
        const ok = await verifyToken(token, row.tokenHash);
        if (!ok) {
          await reply.code(401).send({ error: 'unauthorized' });
          return;
        }
        req.agentId = row.id;
      },
    },
    (socket: WebSocket, req: FastifyRequest) => {
      const agentId = req.agentId;
      if (!agentId) {
        try {
          socket.close(1008, 'unauthorized');
        } catch {
          /* ignore */
        }
        return;
      }

      const wired = wire(
        {
          agentsRepo,
          registry,
          log: req.log,
          heartbeatIntervalMs: deps.heartbeatIntervalMs,
          heartbeatMissLimit: deps.heartbeatMissLimit,
        },
        {
          send: (d) => socket.send(d),
          close: (c, r) => socket.close(c, r),
        },
        agentId,
      );

      socket.on('message', (raw: Buffer) => {
        if (raw.byteLength > MAX_MESSAGE_BYTES) {
          try {
            socket.close(1009, 'too-large');
          } catch {
            /* ignore */
          }
          return;
        }
        void wired.onRawMessage(raw);
      });
      socket.on('close', () => void wired.onClose());
      socket.on('error', (err: unknown) => req.log.warn({ err }, 'ws error'));
    },
  );
}
