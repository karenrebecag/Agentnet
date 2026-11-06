import { DurableObject } from "cloudflare:workers";
import {
  type AgentInfo,
  type Env,
  type NodeKind,
  type NodeRecord,
  NODE_RE,
  RESERVED_NODES,
  newToken,
  now,
  sha256Hex,
  splitAddress,
} from "./shared";

type Row = {
  name: string;
  kind: NodeKind;
  hash: string;
  targets: string;
  revoked: number;
  created: string;
  last_seen: string | null;
  agents: string;
};

function toRecord(row: Row): NodeRecord {
  return {
    name: row.name,
    kind: row.kind,
    targets: JSON.parse(row.targets),
    revoked: row.revoked === 1,
    created: row.created,
    last_seen: row.last_seen,
    agents: JSON.parse(row.agents),
  };
}

/**
 * The single source of truth for nodes and tokens. One instance, so a revoke is
 * visible to the very next request: there is no cache to wait out.
 */
export class Directory extends DurableObject<Env> {
  private sql: SqlStorage;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.sql = ctx.storage.sql;
    this.sql.exec(`CREATE TABLE IF NOT EXISTS nodes (
      name TEXT PRIMARY KEY, kind TEXT NOT NULL, hash TEXT NOT NULL UNIQUE,
      targets TEXT NOT NULL DEFAULT '[]', revoked INTEGER NOT NULL DEFAULT 0,
      created TEXT NOT NULL, last_seen TEXT, agents TEXT NOT NULL DEFAULT '[]')`);
  }

  private row(name: string): Row | null {
    const rows = this.sql.exec<Row>("SELECT * FROM nodes WHERE name = ?", name).toArray();
    return rows[0] ?? null;
  }

  async authenticate(tokenHash: string): Promise<NodeRecord | null> {
    const rows = this.sql.exec<Row>("SELECT * FROM nodes WHERE hash = ? AND revoked = 0", tokenHash).toArray();
    if (rows.length === 0) return null;
    // At most one write a minute per node: every MCP call authenticates, and
    // this object is shared by all of them.
    const seen = rows[0].last_seen ? Date.parse(rows[0].last_seen) : 0;
    if (Date.now() - seen > 60_000) this.sql.exec("UPDATE nodes SET last_seen = ? WHERE name = ?", now(), rows[0].name);
    return toRecord(rows[0]);
  }

  async get(name: string): Promise<NodeRecord | null> {
    const row = this.row(name);
    return row ? toRecord(row) : null;
  }

  async list(): Promise<NodeRecord[]> {
    return this.sql.exec<Row>("SELECT * FROM nodes ORDER BY name").toArray().map(toRecord);
  }

  async create(name: string, kind: NodeKind, targets: string[]): Promise<{ token: string } | { error: string }> {
    if (!NODE_RE.test(name) || RESERVED_NODES.has(name)) return { error: "invalid or reserved node name" };
    if (kind !== "machine" && kind !== "guest") return { error: "kind must be machine or guest" };
    if (kind === "guest" && targets.length === 0) return { error: "a guest needs at least one target" };
    if (kind === "machine" && targets.length > 0) return { error: "only guests have targets" };
    for (const target of targets) {
      if (!splitAddress(target)) return { error: `invalid target ${target}` };
    }
    // A name is never reused, even after revoke: a new holder would inherit the
    // old node's mailbox and pending replies.
    if (this.row(name)) return { error: "name already used" };
    const token = newToken();
    this.sql.exec(
      "INSERT INTO nodes (name, kind, hash, targets, created) VALUES (?, ?, ?, ?, ?)",
      name, kind, await sha256Hex(token), JSON.stringify(targets), now(),
    );
    return { token };
  }

  async rotate(name: string): Promise<{ token: string } | { error: string }> {
    const row = this.row(name);
    if (!row || row.revoked) return { error: "unknown or revoked node" };
    const token = newToken();
    this.sql.exec("UPDATE nodes SET hash = ? WHERE name = ?", await sha256Hex(token), name);
    return { token };
  }

  async revoke(name: string): Promise<boolean> {
    if (!this.row(name)) return false;
    // The hash is replaced too, so the old token cannot match even by accident.
    this.sql.exec("UPDATE nodes SET revoked = 1, hash = ?, agents = '[]' WHERE name = ?", `revoked:${crypto.randomUUID()}`, name);
    return true;
  }

  async setAgents(name: string, agents: AgentInfo[]): Promise<void> {
    this.sql.exec("UPDATE nodes SET agents = ?, last_seen = ? WHERE name = ?", JSON.stringify(agents), now(), name);
  }
}
