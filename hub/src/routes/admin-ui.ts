import type { FastifyInstance } from 'fastify';
import { nanoid } from 'nanoid';
import type { AgentsRepo } from '../repos/agents.js';
import type { AgentRegistry } from '../services/agent-registry.js';
import { extractLookupPrefix, generateToken, hashToken } from '../services/tokens.js';

export interface AdminUiDeps {
  agentsRepo: AgentsRepo;
  bcryptCost: number;
  registry: AgentRegistry;
}

export async function registerAdminUi(app: FastifyInstance, deps: AdminUiDeps): Promise<void> {
  const { agentsRepo, bcryptCost } = deps;

  app.get('/admin', async (_req, reply) => {
    const agents = await agentsRepo.list();
    const rows = agents.map((a) => ({
      id: a.id,
      name: a.name,
      labelsJson: JSON.stringify(a.labels),
      status: a.status,
      lastSeenAt: a.lastSeenAt ? new Date(a.lastSeenAt).toISOString() : '-',
      createdAt: new Date(a.createdAt).toISOString(),
    }));
    const unsafeBanner =
      process.env.ADMIN_UNSAFE_ALLOW_NONLOCAL === '1' ||
      process.env.ADMIN_UNSAFE_ALLOW_NONLOCAL === 'true';
    return reply.view('list.hbs', { agents: rows, unsafeBanner });
  });

  app.post('/admin/ui/agents', async (req, reply) => {
    const body = req.body as { name?: string; labels?: string } | undefined;
    if (!body || !body.name) {
      return reply.code(400).view('error.hbs', { message: 'name is required' });
    }
    const labels: Record<string, string> = {};
    if (body.labels && body.labels.trim().length > 0) {
      try {
        const parsed = JSON.parse(body.labels) as unknown;
        if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
          for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
            if (typeof v !== 'string') {
              return reply
                .code(400)
                .view('error.hbs', { message: `label value for "${k}" must be a string` });
            }
            labels[k] = v;
          }
        } else {
          return reply.code(400).view('error.hbs', { message: 'labels must be a JSON object' });
        }
      } catch {
        return reply.code(400).view('error.hbs', { message: 'labels must be valid JSON' });
      }
    }

    const id = nanoid(21);
    const token = generateToken();
    const tokenPrefix = extractLookupPrefix(token);
    const tokenHash = await hashToken(token, bcryptCost);
    const now = Date.now();

    await agentsRepo.insert({
      id,
      name: body.name.slice(0, 64),
      labels,
      tokenPrefix,
      tokenHash,
      createdAt: now,
    });

    return reply.view('token.hbs', { id, name: body.name, token });
  });
}
