import { DurableObject } from "cloudflare:workers";
import { type SendResult, route } from "./route";
import { type AgentInfo, type Env, type Envelope, AGENT_RE, Quota } from "./shared";

const MAX_QUEUE = 1000;
const RETENTION_MS = 7 * 86_400_000;
const MAX_FRAME_BYTES = 80 * 1024;
const MAX_AGENTS = 100;
const POLL_MS = 250;
const MAX_INFLIGHT_GUEST = 4;

type QueueRow = {
  seq: number;
  env: string;
};

type ClientFrame =
  | { type: "hello" | "agents"; agents: AgentInfo[] }
  | { type: "send"; ref: string; from: string; to: string; text: string; reply_to?: string }
  | { type: "ack"; seq: number };

/**
 * One instance per node, addressed by node name. It owns that node's queue and
 * its WebSocket, so per-recipient ordering needs no coordination.
 */
export class Mailbox extends DurableObject<Env> {
  private sql: SqlStorage;
  private sends = new Quota(60, 2000);
  private asks = new Quota(10, 100);
  private inflight = 0;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.sql = ctx.storage.sql;
    this.sql.exec(`CREATE TABLE IF NOT EXISTS queue (
      seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, env TEXT NOT NULL,
      reply_to TEXT, sender TEXT NOT NULL, created INTEGER NOT NULL)`);
    this.sql.exec("CREATE INDEX IF NOT EXISTS queue_reply ON queue (reply_to)");
    this.sql.exec(`CREATE TABLE IF NOT EXISTS asks (
      id TEXT PRIMARY KEY, target TEXT NOT NULL, created INTEGER NOT NULL)`);
    this.sql.exec("CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)");
  }

  private meta(key: string): string | null {
    return this.sql.exec<{ v: string }>("SELECT v FROM meta WHERE k = ?", key).toArray()[0]?.v ?? null;
  }

  private setMeta(key: string, value: string): void {
    this.sql.exec("INSERT INTO meta (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v", key, value);
  }

  private acked(): number {
    return Number(this.meta("acked") ?? "0");
  }

  private agents(): AgentInfo[] {
    return JSON.parse(this.meta("agents") ?? "[]");
  }

  // ---- machine WebSocket ----

  async fetch(request: Request): Promise<Response> {
    const node = request.headers.get("X-Agentnet-Node");
    if (request.headers.get("Upgrade") !== "websocket" || !node) {
      return new Response("expected websocket", { status: 426 });
    }
    for (const old of this.ctx.getWebSockets()) old.close(4000, "replaced by a newer connection");
    const [client, server] = Object.values(new WebSocketPair());
    this.ctx.acceptWebSocket(server);
    this.setMeta("node", node);
    server.send(JSON.stringify({ type: "welcome", node }));
    this.flush(server);
    await this.scheduleCleanup();
    return new Response(null, { status: 101, webSocket: client });
  }

  private flush(ws: WebSocket): void {
    const rows = this.sql.exec<QueueRow>("SELECT seq, env FROM queue WHERE seq > ? ORDER BY seq LIMIT 500", this.acked()).toArray();
    for (const row of rows) ws.send(`{"type":"deliver","seq":${row.seq},"envelope":${row.env}}`);
  }

  private fail(ws: WebSocket, code: string, message: string, fatal = false): void {
    ws.send(JSON.stringify({ type: "error", code, message }));
    if (fatal) ws.close(1008, code);
  }

  async webSocketMessage(ws: WebSocket, raw: string | ArrayBuffer): Promise<void> {
    if (typeof raw !== "string" || raw.length > MAX_FRAME_BYTES) return this.fail(ws, "frame", "text frames up to 80 KiB only", true);
    let frame: ClientFrame;
    try {
      frame = JSON.parse(raw);
    } catch {
      return this.fail(ws, "frame", "invalid JSON", true);
    }
    const node = this.meta("node");
    if (!node) return this.fail(ws, "state", "unknown node", true);

    switch (frame.type) {
      case "hello":
      case "agents": {
        const agents = sanitizeAgents(frame.agents);
        if (!agents) return this.fail(ws, "agents", "invalid agent list");
        this.setMeta("agents", JSON.stringify(agents));
        await this.env.DIRECTORY.getByName("directory").setAgents(node, agents);
        if (frame.type === "hello") this.flush(ws);
        return;
      }
      case "send": {
        const result = await this.send(node, frame);
        ws.send(JSON.stringify({ type: "sent", ref: String(frame.ref ?? ""), ...result }));
        return;
      }
      case "ack": {
        const seq = Number(frame.seq);
        const top = this.sql.exec<{ m: number | null }>("SELECT MAX(seq) AS m FROM queue").toArray()[0]?.m ?? 0;
        // Never move past what was actually handed out.
        if (!Number.isInteger(seq) || seq <= this.acked() || seq > top) return;
        this.setMeta("acked", String(seq));
        this.sql.exec("DELETE FROM queue WHERE seq <= ?", seq);
        return;
      }
      default:
        return this.fail(ws, "frame", "unknown frame type");
    }
  }

  private async send(node: string, frame: Extract<ClientFrame, { type: "send" }>): Promise<SendResult> {
    if (!this.sends.take()) return { status: "rejected", error: "rate limit" };
    if (typeof frame.from !== "string" || !this.agents().some((a) => a.name === frame.from)) {
      return { status: "rejected", error: "from is not an announced agent of this node" };
    }
    if (typeof frame.to !== "string" || typeof frame.text !== "string") return { status: "rejected", error: "to and text are required" };
    const sender = await this.env.DIRECTORY.getByName("directory").get(node);
    if (!sender || sender.revoked || sender.kind !== "machine") return { status: "rejected", error: "node revoked" };
    return route(this.env, {
      sender,
      fromAgent: frame.from,
      to: frame.to,
      text: frame.text,
      replyTo: typeof frame.reply_to === "string" ? frame.reply_to : undefined,
    });
  }

  async webSocketClose(): Promise<void> {}

  // ---- RPC used by route() and the MCP handler ----

  async enqueue(envelope: Envelope): Promise<SendResult> {
    const count = this.sql.exec<{ n: number }>("SELECT COUNT(*) AS n FROM queue").toArray()[0]?.n ?? 0;
    if (count >= MAX_QUEUE) return { status: "rejected", error: "recipient queue is full" };
    const env = JSON.stringify(envelope);
    this.sql.exec(
      "INSERT INTO queue (id, env, reply_to, sender, created) VALUES (?, ?, ?, ?, ?)",
      envelope.id, env, envelope.reply_to ?? null, envelope.from, Date.now(),
    );
    const seq = this.sql.exec<{ seq: number }>("SELECT seq FROM queue WHERE id = ?", envelope.id).toArray()[0].seq;
    const sockets = this.ctx.getWebSockets();
    for (const ws of sockets) ws.send(`{"type":"deliver","seq":${seq},"envelope":${env}}`);
    await this.scheduleCleanup();
    return { status: sockets.length > 0 ? "delivered" : "queued", id: envelope.id };
  }

  async closeAll(): Promise<void> {
    for (const ws of this.ctx.getWebSockets()) ws.close(4001, "token rotated or revoked");
  }

  async isOnline(): Promise<boolean> {
    return this.ctx.getWebSockets().length > 0;
  }

  /** Guest side: reserves an ask slot, or says why not. */
  async admitAsk(): Promise<string | null> {
    if (this.inflight >= MAX_INFLIGHT_GUEST) return "too many requests in flight";
    if (!this.asks.take()) return "rate limit";
    return null;
  }

  async enter(): Promise<boolean> {
    if (this.inflight >= MAX_INFLIGHT_GUEST) return false;
    this.inflight++;
    return true;
  }

  async leave(): Promise<void> {
    this.inflight = Math.max(0, this.inflight - 1);
  }

  async recordAsk(id: string, target: string): Promise<void> {
    this.sql.exec("INSERT OR IGNORE INTO asks (id, target, created) VALUES (?, ?, ?)", id, target, Date.now());
    await this.scheduleCleanup();
  }

  async askedOf(id: string, from: string): Promise<boolean> {
    return this.sql.exec("SELECT 1 FROM asks WHERE id = ? AND target = ?", id, from).toArray().length > 0;
  }

  async hasAsk(id: string): Promise<boolean> {
    return this.sql.exec("SELECT 1 FROM asks WHERE id = ?", id).toArray().length > 0;
  }

  /** Waits for the reply to an ask, polling local storage, which is cheap. */
  async waitReply(id: string, waitMs: number): Promise<Envelope | null> {
    const deadline = Date.now() + waitMs;
    for (;;) {
      const row = this.sql.exec<{ env: string }>("SELECT env FROM queue WHERE reply_to = ? ORDER BY seq LIMIT 1", id).toArray()[0];
      if (row) return JSON.parse(row.env);
      if (Date.now() >= deadline) return null;
      await new Promise((resolve) => setTimeout(resolve, POLL_MS));
    }
  }

  // ---- retention ----

  private async scheduleCleanup(): Promise<void> {
    if ((await this.ctx.storage.getAlarm()) === null) await this.ctx.storage.setAlarm(Date.now() + 3_600_000);
  }

  async alarm(): Promise<void> {
    const cutoff = Date.now() - RETENTION_MS;
    this.sql.exec("DELETE FROM queue WHERE created < ?", cutoff);
    this.sql.exec("DELETE FROM asks WHERE created < ?", cutoff);
    const left = this.sql.exec<{ n: number }>("SELECT (SELECT COUNT(*) FROM queue) + (SELECT COUNT(*) FROM asks) AS n").toArray()[0].n;
    if (left > 0) await this.ctx.storage.setAlarm(Date.now() + 3_600_000);
  }
}

function sanitizeAgents(input: unknown): AgentInfo[] | null {
  if (!Array.isArray(input) || input.length > MAX_AGENTS) return null;
  const out: AgentInfo[] = [];
  for (const item of input) {
    if (typeof item !== "object" || item === null) return null;
    const { name, kind, accept_external } = item as Record<string, unknown>;
    if (typeof name !== "string" || !AGENT_RE.test(name)) return null;
    out.push({ name, kind: typeof kind === "string" ? kind.slice(0, 16) : "agent", accept_external: accept_external === true });
  }
  return out;
}
