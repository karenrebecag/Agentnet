import { DurableObject } from "cloudflare:workers";
import {
  CONTACT_KEPT_MS,
  CONTACT_TTL_MS,
  MAX_CONTACTS,
  MAX_CONTACTS_PER_GUEST,
  dropGuest,
  newId,
  schema,
  sweep,
} from "./contacts";
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

/** An open guest waiting to talk to an agent for the first time. */
export interface Contact {
  id: string;
  guest: string;
  address: string;
  created: string;
  expires: string;
}


type Row = {
  name: string;
  kind: NodeKind;
  hash: string;
  targets: string;
  revoked: number;
  created: string;
  last_seen: string | null;
  agents: string;
  inbox: number;
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
    inbox: row.inbox === 1,
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
    // Added after the first deploy: existing databases get the column here.
    const cols = this.sql.exec<{ name: string }>("PRAGMA table_info(nodes)").toArray().map((c) => c.name);
    if (!cols.includes("inbox")) this.sql.exec("ALTER TABLE nodes ADD COLUMN inbox INTEGER NOT NULL DEFAULT 0");
    schema(this.sql);
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

  async create(name: string, kind: NodeKind, targets: string[], inbox = false): Promise<{ token: string } | { error: string }> {
    if (!NODE_RE.test(name) || RESERVED_NODES.has(name)) return { error: "invalid or reserved node name" };
    if (kind !== "machine" && kind !== "guest") return { error: "kind must be machine or guest" };
    if (kind === "machine" && targets.length > 0) return { error: "only guests have targets" };
    if (kind === "machine" && inbox) return { error: "only guests have an inbox" };
    for (const target of targets) {
      if (!splitAddress(target)) return { error: `invalid target ${target}` };
    }
    // A name is never reused, even after revoke: a new holder would inherit the
    // old node's mailbox and pending replies.
    if (this.row(name)) return { error: "name already used" };
    const token = newToken();
    this.sql.exec(
      "INSERT INTO nodes (name, kind, hash, targets, created, inbox) VALUES (?, ?, ?, ?, ?, ?)",
      name, kind, await sha256Hex(token), JSON.stringify(targets), now(), inbox ? 1 : 0,
    );
    return { token };
  }

  async rotate(name: string): Promise<{ token: string } | { error: string }> {
    const row = this.row(name);
    if (!row || row.revoked) return { error: "unknown or revoked node" };
    const token = newToken();
    this.sql.exec("UPDATE nodes SET hash = ? WHERE name = ?", await sha256Hex(token), name);
    // Rotating answers a leak, so sessions opened with the guest end too.
    dropGuest(this.sql, name);
    return { token };
  }

  async revoke(name: string): Promise<boolean> {
    if (!this.row(name)) return false;
    // The hash is replaced too, so the old token cannot match even by accident.
    this.sql.exec("UPDATE nodes SET revoked = 1, hash = ?, agents = '[]' WHERE name = ?", `revoked:${crypto.randomUUID()}`, name);
    dropGuest(this.sql, name);
    return true;
  }

  async setAgents(name: string, agents: AgentInfo[]): Promise<void> {
    this.sql.exec("UPDATE nodes SET agents = ?, last_seen = ? WHERE name = ?", JSON.stringify(agents), now(), name);
  }

  // ---- open guests: contacts ----

  /** Whether the person allowed this guest to talk to this agent session. */
  async contactAllowed(guest: string, address: string, session: string): Promise<boolean> {
    return this.sql.exec("SELECT 1 FROM contacts WHERE guest = ? AND address = ? AND session = ? AND status = 'approved' AND expires > ?",
      guest, address, session, Date.now()).toArray().length > 0;
  }

  /** Holds the first message until the person answers. One pending request per pair. */
  async requestContact(guest: string, address: string, session: string, envelope: string): Promise<{ id: string } | { error: string }> {
    const t = Date.now();
    const pending = this.sql.exec<{ id: string }>(
      "SELECT id FROM contacts WHERE guest = ? AND address = ? AND session = ? AND status = 'waiting' AND expires > ?",
      guest, address, session, t).toArray()[0];
    if (pending) return { error: `still waiting for approval (${pending.id}); try again after it is allowed` };
    const refused = this.sql.exec("SELECT 1 FROM contacts WHERE guest = ? AND address = ? AND session = ? AND status = 'denied' AND expires > ?",
      guest, address, session, t).toArray().length > 0;
    if (refused) return { error: "the person refused this contact recently; try again later" };
    const mine = this.sql.exec<{ n: number }>("SELECT COUNT(*) AS n FROM contacts WHERE guest = ? AND status = 'waiting' AND expires > ?", guest, t).toArray()[0].n;
    if (mine >= MAX_CONTACTS_PER_GUEST) return { error: "too many contacts waiting for approval" };
    const all = this.sql.exec<{ n: number }>("SELECT COUNT(*) AS n FROM contacts WHERE status = 'waiting'").toArray()[0].n;
    if (all >= MAX_CONTACTS) return { error: "too many contacts waiting for approval" };
    const id = newId();
    this.sql.exec("INSERT INTO contacts (id, guest, address, session, status, envelope, created, expires) VALUES (?, ?, ?, ?, 'waiting', ?, ?, ?)",
      id, guest, address, session, envelope, t, t + CONTACT_TTL_MS);
    await this.schedule();
    return { id };
  }

  async listContacts(): Promise<Contact[]> {
    return this.sql.exec<{ id: string; guest: string; address: string; created: number; expires: number }>(
      "SELECT id, guest, address, created, expires FROM contacts WHERE status = 'waiting' AND expires > ? ORDER BY created", Date.now())
      .toArray().map((r) => ({ id: r.id, guest: r.guest, address: r.address, created: new Date(r.created).toISOString(), expires: new Date(r.expires).toISOString() }));
  }

  /**
   * Answers a request and hands back the held message. An approval only holds
   * if the agent still runs the session it was asked for: a process that took
   * the name while the request waited gets nothing.
   */
  async decideContact(id: string, approve: boolean): Promise<{ envelope: string; address: string; deliver: boolean } | null> {
    const t = Date.now();
    const row = this.sql.exec<{ envelope: string; address: string; session: string }>(
      "SELECT envelope, address, session FROM contacts WHERE id = ? AND status = 'waiting' AND expires > ?", id, t).toArray()[0];
    if (!row) return null;
    const dest = splitAddress(row.address);
    const node = dest ? this.row(dest.node) : null;
    const current = node && !node.revoked ? (JSON.parse(node.agents) as AgentInfo[]).find((a) => a.name === dest!.agent)?.session : undefined;
    if (approve && current === row.session) {
      this.sql.exec("UPDATE contacts SET status = 'approved', envelope = NULL, expires = ? WHERE id = ?", t + CONTACT_KEPT_MS, id);
      return { envelope: row.envelope, address: row.address, deliver: true };
    }
    // A refusal is remembered for a while, so a leaked link cannot keep asking.
    this.sql.exec("UPDATE contacts SET status = 'denied', envelope = NULL, expires = ? WHERE id = ?", t + CONTACT_TTL_MS, id);
    return { envelope: row.envelope, address: row.address, deliver: false };
  }

  private async schedule(): Promise<void> {
    if ((await this.ctx.storage.getAlarm()) === null) await this.ctx.storage.setAlarm(Date.now() + 3600_000);
  }

  async alarm(): Promise<void> {
    if (sweep(this.sql) > 0) await this.ctx.storage.setAlarm(Date.now() + 3600_000);
  }
}
