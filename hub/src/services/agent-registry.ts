export interface WebSocketLike {
  send(data: string): void;
  close(code?: number, reason?: string): void;
}

export interface ActiveConnection {
  ws: WebSocketLike;
  connId: string;
  connectedAt: number;
}

export class AgentRegistry {
  private readonly byAgent = new Map<string, ActiveConnection>();

  register(agentId: string, conn: ActiveConnection): ActiveConnection | null {
    const prev = this.byAgent.get(agentId) ?? null;
    this.byAgent.set(agentId, conn);
    return prev;
  }

  get(agentId: string): ActiveConnection | undefined {
    return this.byAgent.get(agentId);
  }

  /**
   * Remove the connection only when the stored one matches connId.
   * Prevents a stale close handler from wiping a freshly-registered replacement.
   */
  removeIfCurrent(agentId: string, connId: string): boolean {
    const current = this.byAgent.get(agentId);
    if (current && current.connId === connId) {
      this.byAgent.delete(agentId);
      return true;
    }
    return false;
  }

  all(): ActiveConnection[] {
    return [...this.byAgent.values()];
  }

  size(): number {
    return this.byAgent.size;
  }
}
