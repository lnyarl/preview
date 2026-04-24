import type { Db } from '../db/client.js';

export interface AgentRow {
  id: string;
  name: string;
  labels: Record<string, string>;
  tokenPrefix: string;
  tokenHash: string;
  status: 'online' | 'offline';
  lastSeenAt: number | null;
  createdAt: number;
}

export interface AgentPublic {
  id: string;
  name: string;
  labels: Record<string, string>;
  status: 'online' | 'offline';
  lastSeenAt: number | null;
  createdAt: number;
}

function toPublic(row: AgentRow): AgentPublic {
  return {
    id: row.id,
    name: row.name,
    labels: row.labels,
    status: row.status,
    lastSeenAt: row.lastSeenAt,
    createdAt: row.createdAt,
  };
}

export function createAgentsRepo(db: Db) {
  return {
    async insert(input: {
      id: string;
      name: string;
      labels: Record<string, string>;
      tokenPrefix: string;
      tokenHash: string;
      createdAt: number;
    }): Promise<AgentPublic> {
      const row = await db
        .insertInto('agents')
        .values({
          id: input.id,
          name: input.name,
          labels: JSON.stringify(input.labels),
          token_prefix: input.tokenPrefix,
          token_hash: input.tokenHash,
          status: 'offline',
          last_seen_at: null,
          created_at: input.createdAt,
        })
        .returningAll()
        .executeTakeFirstOrThrow();
      return toPublic(row as unknown as AgentRow);
    },

    async list(): Promise<AgentPublic[]> {
      const rows = await db
        .selectFrom('agents')
        .selectAll()
        .orderBy('created_at', 'desc')
        .execute();
      return (rows as unknown as AgentRow[]).map(toPublic);
    },

    async findByTokenPrefix(prefix: string): Promise<AgentRow | null> {
      const row = await db
        .selectFrom('agents')
        .selectAll()
        .where('token_prefix', '=', prefix)
        .executeTakeFirst();
      return (row as unknown as AgentRow | undefined) ?? null;
    },

    async deleteById(id: string): Promise<boolean> {
      const result = await db.deleteFrom('agents').where('id', '=', id).executeTakeFirst();
      return Number(result.numDeletedRows ?? 0) > 0;
    },

    async markOnline(
      id: string,
      lastSeenAt: number,
      labels: Record<string, string>,
    ): Promise<void> {
      await db
        .updateTable('agents')
        .set({
          status: 'online',
          last_seen_at: lastSeenAt,
          labels: JSON.stringify(labels),
        })
        .where('id', '=', id)
        .execute();
    },

    async touchLastSeen(id: string, lastSeenAt: number): Promise<void> {
      await db
        .updateTable('agents')
        .set({ last_seen_at: lastSeenAt })
        .where('id', '=', id)
        .execute();
    },

    async setOfflineIfCurrent(id: string, onlyIfOnline = true): Promise<void> {
      let q = db.updateTable('agents').set({ status: 'offline' }).where('id', '=', id);
      if (onlyIfOnline) q = q.where('status', '=', 'online');
      await q.execute();
    },

    async bulkOffline(): Promise<number> {
      const result = await db
        .updateTable('agents')
        .set({ status: 'offline' })
        .where('status', '=', 'online')
        .executeTakeFirst();
      return Number(result.numUpdatedRows ?? 0);
    },
  };
}

export type AgentsRepo = ReturnType<typeof createAgentsRepo>;
