import { z } from 'zod';

const EnvSchema = z.object({
  HUB_PORT: z.coerce.number().int().positive().default(3000),
  DATABASE_URL: z.string().min(1).default('postgres://preview:preview@localhost:5432/preview'),
  LOG_LEVEL: z.enum(['fatal', 'error', 'warn', 'info', 'debug', 'trace']).default('info'),
  PRETTY_LOGS: z
    .string()
    .optional()
    .transform((v) => v === '1' || v === 'true'),
  ADMIN_UNSAFE_ALLOW_NONLOCAL: z
    .string()
    .optional()
    .transform((v) => v === '1' || v === 'true'),
  BCRYPT_COST: z.coerce.number().int().min(4).max(14).default(10),
  HEARTBEAT_INTERVAL_MS: z.coerce.number().int().positive().optional(),
  HEARTBEAT_MISS_LIMIT: z.coerce.number().int().positive().optional(),
});

export type AppConfig = z.infer<typeof EnvSchema>;

export function loadConfig(env: NodeJS.ProcessEnv = process.env): AppConfig {
  const parsed = EnvSchema.safeParse(env);
  if (!parsed.success) {
    const issues = parsed.error.issues
      .map((i) => `  - ${i.path.join('.')}: ${i.message}`)
      .join('\n');
    throw new Error(`Invalid Hub env:\n${issues}`);
  }
  return parsed.data;
}
