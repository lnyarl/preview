import { z } from 'zod';

const LabelsSchema = z.record(z.string().min(1).max(64), z.string().max(256));

export const CreateAgentBody = z.object({
  name: z.string().min(1).max(64),
  labels: LabelsSchema.refine((v) => Object.keys(v).length <= 16, {
    message: 'labels must have at most 16 entries',
  }),
});
export type CreateAgentBody = z.infer<typeof CreateAgentBody>;

export const AgentView = z.object({
  id: z.string(),
  name: z.string(),
  labels: LabelsSchema,
  status: z.enum(['online', 'offline']),
  lastSeenAt: z.number().int().nullable(),
  createdAt: z.number().int(),
});
export type AgentView = z.infer<typeof AgentView>;

export const ListAgentsResponse = z.object({
  agents: z.array(AgentView),
});
export type ListAgentsResponse = z.infer<typeof ListAgentsResponse>;

export const CreateAgentResponse = z.object({
  id: z.string(),
  name: z.string(),
  token: z.string(),
  createdAt: z.number().int(),
});
export type CreateAgentResponse = z.infer<typeof CreateAgentResponse>;
