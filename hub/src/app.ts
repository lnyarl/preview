import Fastify, { type FastifyInstance } from 'fastify';
import formbody from '@fastify/formbody';
import view from '@fastify/view';
import websocket from '@fastify/websocket';
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
import { registerAgentWs } from './routes/agent-ws.js';

export interface AppDeps {
  config: AppConfig;
}

export interface AppBundle {
  app: FastifyInstance;
  pool: pg.Pool;
  db: Db;
  agentsRepo: AgentsRepo;
  registry: AgentRegistry;
  gracefulShutdown(signal: string): Promise<void>;
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
  await app.register(websocket, {
    options: { maxPayload: 16 * 1024 },
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
  await registerAgentWs(app, {
    agentsRepo,
    registry,
    heartbeatIntervalMs: config.HEARTBEAT_INTERVAL_MS,
    heartbeatMissLimit: config.HEARTBEAT_MISS_LIMIT,
  });

  // Startup reconciliation: anything marked online from a previous process is stale (B3).
  const reset = await agentsRepo.bulkOffline();
  if (reset > 0) {
    app.log.warn({ reset }, 'reset stale online agents to offline on startup');
  }

  app.addHook('onClose', async () => {
    await db.destroy();
  });

  let shuttingDown = false;
  async function gracefulShutdown(signal: string): Promise<void> {
    if (shuttingDown) return;
    shuttingDown = true;
    app.log.info({ signal }, 'graceful shutdown start');

    // (1) Disconnect active WS agents with SERVER_SHUTDOWN, then (2) bulk offline, (3) close Fastify.
    const sentAt = Date.now();
    for (const conn of registry.all()) {
      try {
        conn.ws.send(JSON.stringify({ type: 'DISCONNECT', reason: 'SERVER_SHUTDOWN', sentAt }));
      } catch {
        /* ignore */
      }
      try {
        conn.ws.close(1001, 'server-shutdown');
      } catch {
        /* ignore */
      }
    }

    try {
      await agentsRepo.bulkOffline();
    } catch (err) {
      app.log.warn({ err }, 'bulkOffline on shutdown failed');
    }

    try {
      await app.close();
    } catch (err) {
      app.log.error({ err }, 'fastify close failed');
    }
  }

  return { app, pool, db, agentsRepo, registry, gracefulShutdown };
}
