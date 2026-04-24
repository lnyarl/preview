import { loadConfig } from '../config.js';
import { createDb, createPool } from '../db/client.js';
import { migrateDown } from '../db/migrator.js';

async function main(): Promise<void> {
  const config = loadConfig();
  const pool = createPool(config.DATABASE_URL);
  const db = createDb(pool);
  try {
    await migrateDown(db);
    console.log('migration reverted');
  } finally {
    await db.destroy();
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
