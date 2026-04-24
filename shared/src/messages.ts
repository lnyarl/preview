import { z } from 'zod';
import { DISCONNECT_REASONS } from './constants.js';

const LabelsSchema = z.record(z.string().min(1).max(64), z.string().max(256));

export const HelloMsg = z.object({
  type: z.literal('HELLO'),
  version: z.number().int().positive(),
  labels: LabelsSchema,
  agentVersion: z.string().max(32),
  sentAt: z.number().int().nonnegative(),
});
export type HelloMsg = z.infer<typeof HelloMsg>;

export const PongMsg = z.object({
  type: z.literal('PONG'),
  nonce: z.string().min(1).max(64),
  sentAt: z.number().int().nonnegative(),
});
export type PongMsg = z.infer<typeof PongMsg>;

export const ReadyMsg = z.object({
  type: z.literal('READY'),
  capacity: z.number().int().positive().max(64),
  sentAt: z.number().int().nonnegative(),
});
export type ReadyMsg = z.infer<typeof ReadyMsg>;

export const ClientMessage = z.discriminatedUnion('type', [HelloMsg, PongMsg, ReadyMsg]);
export type ClientMessage = z.infer<typeof ClientMessage>;

export const WelcomeMsg = z.object({
  type: z.literal('WELCOME'),
  agentId: z.string().regex(/^[A-Za-z0-9_-]{21}$/),
  serverVersion: z.number().int().positive(),
  heartbeatIntervalMs: z.number().int().positive(),
  sentAt: z.number().int().nonnegative(),
});
export type WelcomeMsg = z.infer<typeof WelcomeMsg>;

export const PingMsg = z.object({
  type: z.literal('PING'),
  nonce: z.string().min(1).max(64),
  sentAt: z.number().int().nonnegative(),
});
export type PingMsg = z.infer<typeof PingMsg>;

export const DisconnectMsg = z.object({
  type: z.literal('DISCONNECT'),
  reason: z.enum(DISCONNECT_REASONS),
  sentAt: z.number().int().nonnegative(),
});
export type DisconnectMsg = z.infer<typeof DisconnectMsg>;

export const ServerMessage = z.discriminatedUnion('type', [WelcomeMsg, PingMsg, DisconnectMsg]);
export type ServerMessage = z.infer<typeof ServerMessage>;

export type DisconnectReason = (typeof DISCONNECT_REASONS)[number];
