import Fastify, { type FastifyInstance } from 'fastify';
import type { AppConfig } from './config.js';
import { registerHealthRoute } from './routes/health.js';

export interface AppDeps {
  config: AppConfig;
}

export async function buildApp(deps: AppDeps): Promise<FastifyInstance> {
  const { config } = deps;

  const app = Fastify({
    logger: {
      level: config.LOG_LEVEL,
      ...(config.PRETTY_LOGS
        ? { transport: { target: 'pino-pretty', options: { translateTime: 'HH:MM:ss' } } }
        : {}),
    },
    disableRequestLogging: false,
  });

  await registerHealthRoute(app);

  app.get('/', async () => ({ hello: 'hub' }));

  return app;
}
