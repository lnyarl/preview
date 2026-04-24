import { Migrator, type Migration, type MigrationProvider, type Kysely } from 'kysely';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import type { DatabaseSchema } from './schema.js';

function migrationsDir(): string {
  const here = path.dirname(fileURLToPath(import.meta.url));
  return path.resolve(here, '..', 'migrations');
}

// Windows-safe provider: dynamic import requires file:// URLs on Windows.
class WindowsSafeFsProvider implements MigrationProvider {
  constructor(private readonly folder: string) {}

  async getMigrations(): Promise<Record<string, Migration>> {
    const out: Record<string, Migration> = {};
    let files: string[];
    try {
      files = await fs.readdir(this.folder);
    } catch (err: unknown) {
      if ((err as NodeJS.ErrnoException).code === 'ENOENT') return out;
      throw err;
    }
    for (const file of files.sort()) {
      if (!/\.(js|mjs|ts|mts)$/i.test(file)) continue;
      if (/\.d\.(ts|mts)$/i.test(file)) continue;
      const abs = path.join(this.folder, file);
      const mod = (await import(pathToFileURL(abs).href)) as Migration;
      const name = file.replace(/\.(js|mjs|ts|mts)$/i, '');
      out[name] = mod;
    }
    return out;
  }
}

export function createMigrator(db: Kysely<DatabaseSchema>): Migrator {
  return new Migrator({ db, provider: new WindowsSafeFsProvider(migrationsDir()) });
}

export async function migrateToLatest(db: Kysely<DatabaseSchema>, log = console): Promise<void> {
  const migrator = createMigrator(db);
  const { error, results } = await migrator.migrateToLatest();

  for (const r of results ?? []) {
    if (r.status === 'Success') {
      log.info?.(`migration "${r.migrationName}" applied`);
    } else if (r.status === 'Error') {
      log.error?.(`migration "${r.migrationName}" FAILED`);
    }
  }
  if (error) {
    throw error instanceof Error ? error : new Error(String(error));
  }
}

export async function migrateDown(db: Kysely<DatabaseSchema>, log = console): Promise<void> {
  const migrator = createMigrator(db);
  const { error, results } = await migrator.migrateDown();
  for (const r of results ?? []) {
    log.info?.(`migration "${r.migrationName}" ${r.status}`);
  }
  if (error) {
    throw error instanceof Error ? error : new Error(String(error));
  }
}
