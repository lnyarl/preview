import { CamelCasePlugin, Kysely, PostgresDialect } from 'kysely';
import pg from 'pg';
import type { DatabaseSchema } from './schema.js';

export type Db = Kysely<DatabaseSchema>;

// Parse BIGINT (OID 20) as JS number. Phase 1 columns (created_at, last_seen_at)
// are epoch ms and safely fit in Number.MAX_SAFE_INTEGER (until year 287396).
pg.types.setTypeParser(pg.types.builtins.INT8, (v) => (v === null ? null : Number(v)));

export function createPool(databaseUrl: string): pg.Pool {
  return new pg.Pool({
    connectionString: databaseUrl,
    max: 10,
    idleTimeoutMillis: 10_000,
    connectionTimeoutMillis: 5_000,
  });
}

export function createDb(pool: pg.Pool): Db {
  return new Kysely<DatabaseSchema>({
    dialect: new PostgresDialect({ pool }),
    plugins: [new CamelCasePlugin()],
  });
}
