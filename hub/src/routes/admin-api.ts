import type { FastifyInstance, FastifyRequest, FastifyReply } from 'fastify';
import { nanoid } from 'nanoid';
import { CreateAgentBody, type AgentView, type ListAgentsResponse } from '@preview/shared';
import type { AgentsRepo } from '../repos/agents.js';
import type { AgentRegistry } from '../services/agent-registry.js';
import { generateToken, extractLookupPrefix, hashToken } from '../services/tokens.js';

export interface AdminApiDeps {
  agentsRepo: AgentsRepo;
  bcryptCost: number;
  registry: AgentRegistry;
}

const LOCAL_IPS = new Set(['127.0.0.1', '::1', '::ffff:127.0.0.1']);

export function isLocalOnly(req: FastifyRequest, allowNonLocal = false): boolean {
  if (allowNonLocal) return true;
  const ip = req.ip;
  return LOCAL_IPS.has(ip);
}

export async function registerAdminApi(app: FastifyInstance, deps: AdminApiDeps): Promise<void> {
  const { agentsRepo, bcryptCost, registry } = deps;

  app.addHook('preHandler', async (req, reply) => {
    if (!req.url.startsWith('/admin')) return;
    const allowNonLocal =
      process.env.ADMIN_UNSAFE_ALLOW_NONLOCAL === '1' ||
      process.env.ADMIN_UNSAFE_ALLOW_NONLOCAL === 'true';
    if (!isLocalOnly(req, allowNonLocal)) {
      await reply.code(403).send({ error: 'forbidden: admin API is local-only' });
    }
  });

  app.post('/admin/agents', async (req, reply) => {
    const parsed = CreateAgentBody.safeParse(req.body);
    if (!parsed.success) {
      return reply.code(400).send({ error: 'invalid body', issues: parsed.error.issues });
    }
    const id = nanoid(21);
    const token = generateToken();
    const tokenPrefix = extractLookupPrefix(token);
    const tokenHash = await hashToken(token, bcryptCost);
    const now = Date.now();

    try {
      const row = await agentsRepo.insert({
        id,
        name: parsed.data.name,
        labels: parsed.data.labels,
        tokenPrefix,
        tokenHash,
        createdAt: now,
      });
      return reply.code(201).send({
        id: row.id,
        name: row.name,
        token,
        createdAt: row.createdAt,
      });
    } catch (err: unknown) {
      app.log.error({ err }, 'failed to create agent');
      return reply.code(500).send({ error: 'internal error' });
    }
  });

  app.get('/admin/agents', async (_req, reply: FastifyReply) => {
    const agents = await agentsRepo.list();
    const view: AgentView[] = agents.map((a) => ({
      id: a.id,
      name: a.name,
      labels: a.labels,
      status: a.status,
      lastSeenAt: a.lastSeenAt,
      createdAt: a.createdAt,
    }));
    const resp: ListAgentsResponse = { agents: view };
    return reply.send(resp);
  });

  app.delete('/admin/agents/:id', async (req, reply) => {
    const params = req.params as { id: string };
    const existed = await agentsRepo.deleteById(params.id);
    if (existed) {
      const active = registry.get(params.id);
      if (active) {
        try {
          active.ws.send(
            JSON.stringify({ type: 'DISCONNECT', reason: 'INVALID_TOKEN', sentAt: Date.now() }),
          );
        } catch {
          /* ignore send errors on closing socket */
        }
        try {
          active.ws.close(1008, 'deleted');
        } catch {
          /* ignore */
        }
      }
    }
    return reply.code(existed ? 204 : 404).send();
  });
}
