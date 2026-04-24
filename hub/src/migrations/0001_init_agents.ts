import { sql, type Kysely } from 'kysely';

export async function up(db: Kysely<unknown>): Promise<void> {
  await db.schema
    .createTable('agents')
    .addColumn('id', 'text', (col) => col.primaryKey())
    .addColumn('name', 'text', (col) => col.notNull())
    .addColumn('labels', 'jsonb', (col) => col.notNull().defaultTo(sql`'{}'::jsonb`))
    .addColumn('token_prefix', 'text', (col) => col.notNull())
    .addColumn('token_hash', 'text', (col) => col.notNull())
    .addColumn('status', 'text', (col) =>
      col
        .notNull()
        .defaultTo('offline')
        .check(sql`status IN ('online','offline')`),
    )
    .addColumn('last_seen_at', 'bigint')
    .addColumn('created_at', 'bigint', (col) => col.notNull())
    .addCheckConstraint('agents_name_len', sql`length(name) BETWEEN 1 AND 64`)
    .addCheckConstraint('agents_token_prefix_len', sql`length(token_prefix) = 12`)
    .execute();

  await db.schema
    .createIndex('ix_agents_token_prefix')
    .on('agents')
    .column('token_prefix')
    .execute();
}

export async function down(db: Kysely<unknown>): Promise<void> {
  await db.schema.dropTable('agents').ifExists().execute();
}
