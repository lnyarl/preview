import { buildApp } from './app.js';
import { loadConfig } from './config.js';

async function main(): Promise<void> {
  const config = loadConfig();
  const bundle = await buildApp({ config });
  const { app, gracefulShutdown } = bundle;

  const handle = (signal: string): void => {
    void gracefulShutdown(signal).then(() => process.exit(0));
  };
  process.once('SIGINT', () => handle('SIGINT'));
  process.once('SIGTERM', () => handle('SIGTERM'));

  try {
    await app.listen({ port: config.HUB_PORT, host: '0.0.0.0' });
    app.log.info(`Hub listening on ${config.HUB_PORT}`);
  } catch (err) {
    app.log.error({ err }, 'failed to start');
    process.exit(1);
  }
}

void main();
