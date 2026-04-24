import Fastify, { type FastifyInstance } from 'fastify';
import formbody from '@fastify/formbody';
import view from '@fastify/view';
import handlebars from 'handlebars';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import type pg from 'pg';
import type { AppConfig } from './config.js';
import { createDb, createPool, type Db } from './db/client.js';
import { createAgentsRepo, type AgentsRepo } from './repos/agents.js';
import { AgentRegistry } from './services/agent-registry.js';
import { registerHealthRoute } from './routes/health.js';
import { registerAdminApi } from './routes/admin-api.js';
import { registerAdminUi } from './routes/admin-ui.js';

export interface AppDeps {
  config: AppConfig;
}

export interface AppBundle {
  app: FastifyInstance;
  pool: pg.Pool;
  db: Db;
  agentsRepo: AgentsRepo;
  registry: AgentRegistry;
}

function viewsDir(): string {
  const here = path.dirname(fileURLToPath(import.meta.url));
  return path.resolve(here, 'views');
}

export async function buildApp(deps: AppDeps): Promise<AppBundle> {
  const { config } = deps;

  const app = Fastify({
    logger: {
      level: config.LOG_LEVEL,
      ...(config.PRETTY_LOGS
        ? { transport: { target: 'pino-pretty', options: { translateTime: 'HH:MM:ss' } } }
        : {}),
    },
    disableRequestLogging: false,
    trustProxy: false,
    bodyLimit: 1024 * 1024,
  });

  await app.register(formbody);
  await app.register(view, {
    engine: { handlebars },
    root: viewsDir(),
    defaultContext: {},
    viewExt: 'hbs',
  });

  const pool = createPool(config.DATABASE_URL);
  const db = createDb(pool);
  const agentsRepo = createAgentsRepo(db);
  const registry = new AgentRegistry();

  if (config.ADMIN_UNSAFE_ALLOW_NONLOCAL) {
    app.log.warn(
      '!!! ADMIN API EXPOSED TO EXTERNAL !!! ADMIN_UNSAFE_ALLOW_NONLOCAL=1; Phase 3 auth 미구현 상태.',
    );
  }

  await registerHealthRoute(app);

  app.get('/', async () => ({ hello: 'hub' }));

  await registerAdminApi(app, { agentsRepo, bcryptCost: config.BCRYPT_COST, registry });
  await registerAdminUi(app, { agentsRepo, bcryptCost: config.BCRYPT_COST, registry });

  // Hub startup: reconcile potentially stale "online" rows from a prior crash (B3).
  const reset = await agentsRepo.bulkOffline();
  if (reset > 0) {
    app.log.warn({ reset }, 'reset stale online agents to offline on startup');
  }

  app.addHook('onClose', async () => {
    await db.destroy();
  });

  return { app, pool, db, agentsRepo, registry };
}
