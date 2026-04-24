export const PROTOCOL_VERSION = 1 as const;

export const HEARTBEAT_INTERVAL_MS = 30_000;
export const HEARTBEAT_TIMEOUT_MS = 10_000;
export const HEARTBEAT_MISS_LIMIT = 3;
export const HELLO_TIMEOUT_MS = 5_000;
export const MAX_MESSAGE_BYTES = 16 * 1024;

export const RECONNECT_BASE_MS = 1_000;
export const RECONNECT_MAX_MS = 30_000;
export const RECONNECT_JITTER_RATIO = 0.2;

export const TOKEN_PREFIX = 'agt_' as const;
export const TOKEN_RANDOM_BYTES = 32;
export const TOKEN_PREFIX_LOOKUP_LEN = 12;

export const DISCONNECT_REASONS = [
  'INCOMPATIBLE_VERSION',
  'INVALID_TOKEN',
  'REPLACED',
  'SERVER_SHUTDOWN',
  'HEARTBEAT_TIMEOUT',
  'PROTOCOL_ERROR',
] as const;
