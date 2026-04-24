import Fastify from 'fastify';
import { SHARED_VERSION } from '@preview/shared';

const app = Fastify({ logger: true });

app.get('/', async () => ({ hello: 'hub', shared: SHARED_VERSION }));

const port = Number(process.env.HUB_PORT ?? 3000);

const shutdown = async (signal: string): Promise<void> => {
  app.log.info(`${signal} received, closing`);
  await app.close();
  process.exit(0);
};
process.once('SIGINT', () => void shutdown('SIGINT'));
process.once('SIGTERM', () => void shutdown('SIGTERM'));

app
  .listen({ port, host: '0.0.0.0' })
  .then(() => app.log.info(`Hub listening on ${port}`))
  .catch((err) => {
    app.log.error(err);
    process.exit(1);
  });
