import type { ColumnType, Generated } from 'kysely';

export interface AgentsTable {
  id: string;
  name: string;
  labels: ColumnType<Record<string, string>, string, string>;
  token_prefix: string;
  token_hash: string;
  status: 'online' | 'offline';
  last_seen_at: number | null;
  created_at: Generated<number>;
}

export interface DatabaseSchema {
  agents: AgentsTable;
}
