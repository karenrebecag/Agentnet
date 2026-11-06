import { DurableObject } from "cloudflare:workers";
import {
  CODE_TTL_MS,
  FAILURE_WINDOW_MS,
  GRANT_MAX_MS,
  type Issued,
  MAX_PENDING_CLIENTS,
  MAX_REGISTRATIONS_PER_IP,
  MAX_REQUESTS,
  MAX_SIGNINS,
  CONTACT_TTL_MS,
  MAX_CONTACTS,
  MAX_CONTACTS_PER_GUEST,
  PAIR_FAILURE_LIMIT,
  PAIR_TTL_MS,
  REQUEST_TTL_MS,
  SIGNIN_TTL_MS,
  dropGrant,
  dropGuest,
  hostAllowed,
  issueTokens,
  newId,
  newPairCode,
  normalizePairCode,
  schema,
  sweep,
} from "./grants";
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
  base64url,
  newSecret,
  splitAddress,
} from "./shared";

export interface ClientRecord {
  id: string;
  kind: "dcr" | "cimd";
  name: string;
  redirects: string[];
}

export type AuthRequest = {
  client: string;
  redirect: string;
  challenge: string;
  state: string;
  scope: string;
};

type Failure = { error: string; description: string };

/** A sign-in that used a code with no host, waiting for the person to say yes. */
export interface Signin {
  id: string;
  guest: string;
  client: string;
  client_name: string;
  host: string;
  ip: string;
  created: string;
  expires: string;
}

/** An open guest waiting to talk to an agent for the first time. */
export interface Contact {
  id: string;
  guest: string;
  address: string;
  created: string;
  expires: string;
}

// An approval lasts as long as the agent's session; this only bounds the row.
const CONTACT_KEPT_MS = 30 * 86_400_000;

type SigninRow = AuthRequest & { id: string; guest: string; client_name: string; host: string; ip: string; status: string; created: number; expires: number };

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

function toSignin(r: SigninRow): Signin {
  return {
    id: r.id, guest: r.guest, client: r.client, client_name: r.client_name, host: r.host, ip: r.ip,
    created: new Date(r.created).toISOString(), expires: new Date(r.expires).toISOString(),
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
    const clientCols = this.sql.exec<{ name: string }>("PRAGMA table_info(clients)").toArray().map((c) => c.name);
    if (!clientCols.includes("ip")) this.sql.exec("ALTER TABLE clients ADD COLUMN ip TEXT NOT NULL DEFAULT ''");
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

  // ---- OAuth: pairing ----

  /**
   * Issues a one-use code that lets a client sign in as this guest. With a host
   * the code only works for that client; with "" the first client to use it
   * waits until the person approves the host it returns to.
   */
  async pair(name: string, host: string): Promise<{ code: string; expires: string } | { error: string }> {
    const row = this.row(name);
    if (!row || row.revoked || row.kind !== "guest") return { error: "unknown, revoked or not a guest" };
    const code = newPairCode();
    const expires = Date.now() + PAIR_TTL_MS;
    this.sql.exec("INSERT INTO pairings (hash, guest, host, expires) VALUES (?, ?, ?, ?)", await sha256Hex(normalizePairCode(code)), name, host, expires);
    await this.schedule();
    return { code, expires: new Date(expires).toISOString() };
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
    const id = newId().slice(0, 8);
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

  // ---- OAuth: clients ----

  async client(id: string): Promise<ClientRecord | null> {
    const row = this.sql.exec<{ id: string; kind: "dcr" | "cimd"; name: string; redirects: string; fetched: number }>(
      "SELECT id, kind, name, redirects, fetched FROM clients WHERE id = ?", id).toArray()[0];
    if (!row) return null;
    if (row.kind === "cimd" && Date.now() - row.fetched > 3600_000) return null;
    return { id: row.id, kind: row.kind, name: row.name, redirects: JSON.parse(row.redirects) };
  }

  async register(name: string, redirects: string[], ip: string): Promise<ClientRecord | { error: string }> {
    const recent = this.sql.exec<{ n: number }>("SELECT COUNT(*) AS n FROM clients WHERE ip = ? AND created > ?", ip, Date.now() - 3600_000).toArray()[0].n;
    if (recent >= MAX_REGISTRATIONS_PER_IP) return { error: "too many registrations from this address, try later" };
    // A full table drops the oldest unused registration instead of refusing
    // everyone: a real client signs in within minutes of registering.
    this.evict("clients", "used = 0", "created", MAX_PENDING_CLIENTS - 1);
    const id = "dcr_" + newId();
    this.sql.exec("INSERT INTO clients (id, kind, name, redirects, created, ip) VALUES (?, 'dcr', ?, ?, ?, ?)", id, name, JSON.stringify(redirects), Date.now(), ip);
    await this.schedule();
    return { id, kind: "dcr", name, redirects };
  }

  async cacheMetadataClient(c: ClientRecord): Promise<void> {
    this.evict("clients", "used = 0", "created", MAX_PENDING_CLIENTS - 1);
    this.sql.exec(
      "INSERT INTO clients (id, kind, name, redirects, created, fetched) VALUES (?, 'cimd', ?, ?, ?, ?) " +
        "ON CONFLICT(id) DO UPDATE SET name = excluded.name, redirects = excluded.redirects, fetched = excluded.fetched",
      c.id, c.name, JSON.stringify(c.redirects), Date.now(), Date.now());
    await this.schedule();
  }

  // ---- OAuth: authorization ----

  /** Stores a validated authorization request; its id doubles as the form's one-use CSRF token. */
  async startRequest(r: AuthRequest): Promise<string> {
    this.evict("requests", "1 = 1", "expires", MAX_REQUESTS - 1);
    const id = newSecret("req_");
    this.sql.exec(
      "INSERT INTO requests (hash, client, redirect, challenge, state, scope, expires) VALUES (?, ?, ?, ?, ?, ?, ?)",
      await sha256Hex(id), r.client, r.redirect, r.challenge, r.state, r.scope, Date.now() + REQUEST_TTL_MS);
    await this.schedule();
    return id;
  }

  async peekRequest(id: string): Promise<AuthRequest | null> {
    return this.sql.exec<AuthRequest>("SELECT client, redirect, challenge, state, scope FROM requests WHERE hash = ? AND expires > ?",
      await sha256Hex(id), Date.now()).toArray()[0] ?? null;
  }

  /**
   * The consent step. The request is consumed whatever happens, so a form can
   * be submitted once; a wrong code counts toward burning every open pairing.
   */
  async approve(requestId: string, typed: string, ip: string): Promise<{ redirect: string } | { waiting: string } | { retry: string; message: string } | Failure> {
    const reqHash = await sha256Hex(requestId);
    if (this.failures(ip) >= PAIR_FAILURE_LIMIT) {
      return { error: "temporarily_unavailable", description: "too many wrong codes from this address; wait 15 minutes" };
    }
    const req = this.sql.exec<AuthRequest>("SELECT client, redirect, challenge, state, scope FROM requests WHERE hash = ? AND expires > ?",
      reqHash, Date.now()).toArray()[0];
    this.sql.exec("DELETE FROM requests WHERE hash = ?", reqHash);
    if (!req) return { error: "invalid_request", description: "this sign-in page expired; start again from the client" };

    const pairing = this.sql.exec<{ hash: string; guest: string; host: string }>(
      "SELECT hash, guest, host FROM pairings WHERE hash = ? AND expires > ?", await sha256Hex(normalizePairCode(typed)), Date.now()).toArray()[0];
    let message = "";
    if (!pairing) message = "That code is not valid or has expired.";
    else if (pairing.host !== "" && !hostAllowed(req.redirect, pairing.host)) {
      message = `That code is for ${pairing.host}, but this client returns to ${new URL(req.redirect).host}.`;
    }
    if (message) {
      this.sql.exec("INSERT INTO failures (ip, at) VALUES (?, ?)", ip, Date.now());
      return { retry: await this.startRequest(req), message };
    }

    this.sql.exec("DELETE FROM pairings WHERE hash = ?", pairing!.hash);
    if (pairing!.host === "") return { waiting: await this.wait(req, pairing!.guest, ip) };
    return this.issueCode(pairing!.guest, req);
  }

  // The code is spent here, so it cannot be tried by a second client while
  // the first one waits.
  private async wait(req: AuthRequest, guest: string, ip: string): Promise<string> {
    // An approved row is one poll away from its code, so waiting rows go first.
    const over = this.sql.exec<{ n: number }>("SELECT COUNT(*) AS n FROM signins").toArray()[0].n - (MAX_SIGNINS - 1);
    if (over > 0) {
      this.sql.exec("DELETE FROM signins WHERE id IN (SELECT id FROM signins ORDER BY status = 'waiting' DESC, created LIMIT ?)", over);
    }
    const client = await this.client(req.client);
    const poll = newSecret("anw_");
    const t = Date.now();
    this.sql.exec(
      "INSERT INTO signins (id, poll, guest, client, client_name, redirect, challenge, state, scope, host, ip, created, expires) " +
        "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
      newId().slice(0, 8), await sha256Hex(poll), guest, req.client, client?.name ?? req.client, req.redirect, req.challenge,
      req.state, req.scope, new URL(req.redirect).host, ip, t, t + SIGNIN_TTL_MS);
    await this.schedule();
    return poll;
  }

  private async issueCode(guestName: string, req: AuthRequest): Promise<{ redirect: string } | Failure> {
    const guest = this.row(guestName);
    if (!guest || guest.revoked) return { error: "access_denied", description: "the guest was revoked" };
    const grantId = newId();
    this.sql.exec("INSERT INTO grants (id, guest, client, scope, created) VALUES (?, ?, ?, ?, ?)", grantId, guest.name, req.client, req.scope, Date.now());
    this.sql.exec("UPDATE clients SET used = 1 WHERE id = ?", req.client);
    const code = newSecret("anc_");
    this.sql.exec("INSERT INTO codes (hash, grant_id, client, redirect, challenge, expires) VALUES (?, ?, ?, ?, ?, ?)",
      await sha256Hex(code), grantId, req.client, req.redirect, req.challenge, Date.now() + CODE_TTL_MS);
    const back = new URL(req.redirect);
    back.searchParams.set("code", code);
    if (req.state) back.searchParams.set("state", req.state);
    return { redirect: back.toString() };
  }

  /** What the waiting browser sees each time it reloads. */
  async pollSignin(poll: string): Promise<{ waiting: Signin } | { redirect: string } | Failure> {
    const hash = await sha256Hex(poll);
    const row = this.sql.exec<SigninRow>("SELECT * FROM signins WHERE poll = ? AND expires > ?", hash, Date.now()).toArray()[0];
    if (!row) return { error: "invalid_request", description: "this sign-in expired or was already used; start again from the client" };
    if (row.status === "waiting") return { waiting: toSignin(row) };
    this.sql.exec("DELETE FROM signins WHERE id = ?", row.id);
    if (row.status === "approved") return this.issueCode(row.guest, row);
    const back = new URL(row.redirect);
    back.searchParams.set("error", "access_denied");
    if (row.state) back.searchParams.set("state", row.state);
    return { redirect: back.toString() };
  }

  async listSignins(): Promise<Signin[]> {
    return this.sql.exec<SigninRow>("SELECT * FROM signins WHERE status = 'waiting' AND expires > ? ORDER BY created", Date.now())
      .toArray().map(toSignin);
  }

  async decideSignin(id: string, approve: boolean): Promise<boolean> {
    const changed = this.sql.exec("UPDATE signins SET status = ? WHERE id = ? AND status = 'waiting' AND expires > ?",
      approve ? "approved" : "denied", id, Date.now()).rowsWritten;
    return changed > 0;
  }

  async deny(requestId: string): Promise<AuthRequest | null> {
    const hash = await sha256Hex(requestId);
    const req = this.sql.exec<AuthRequest>("SELECT client, redirect, challenge, state, scope FROM requests WHERE hash = ? AND expires > ?", hash, Date.now()).toArray()[0];
    this.sql.exec("DELETE FROM requests WHERE hash = ?", hash);
    return req ?? null;
  }

  // Wrong codes per address in the last 15 minutes. Counting per address
  // keeps one stranger from cancelling everyone's pairings; the 40-bit code
  // and its 10-minute life make guessing across many addresses hopeless.
  private failures(ip: string): number {
    return this.sql.exec<{ n: number }>("SELECT COUNT(*) AS n FROM failures WHERE ip = ? AND at > ?", ip, Date.now() - FAILURE_WINDOW_MS).toArray()[0].n;
  }

  // Keeps a table under a cap by deleting its oldest matching rows.
  private evict(table: "clients" | "requests", where: string, order: string, keep: number): void {
    const n = this.sql.exec<{ n: number }>(`SELECT COUNT(*) AS n FROM ${table} WHERE ${where}`).toArray()[0].n;
    if (n <= keep) return;
    const key = table === "clients" ? "id" : "hash";
    this.sql.exec(`DELETE FROM ${table} WHERE ${key} IN (SELECT ${key} FROM ${table} WHERE ${where} ORDER BY ${order} LIMIT ?)`, n - keep);
  }

  // ---- OAuth: tokens ----

  async exchange(code: string, client: string, redirect: string, verifier: string): Promise<Issued | Failure> {
    const hash = await sha256Hex(code);
    const row = this.sql.exec<{ grant_id: string; client: string; redirect: string; challenge: string; expires: number; used: number }>(
      "SELECT grant_id, client, redirect, challenge, expires, used FROM codes WHERE hash = ?", hash).toArray()[0];
    if (!row) return { error: "invalid_grant", description: "unknown code" };
    if (row.used) {
      // A code presented twice was intercepted somewhere: nothing it issued survives.
      dropGrant(this.sql, row.grant_id);
      return { error: "invalid_grant", description: "code already used" };
    }
    this.sql.exec("UPDATE codes SET used = 1 WHERE hash = ?", hash);
    if (row.expires < Date.now()) return { error: "invalid_grant", description: "code expired" };
    if (row.client !== client || row.redirect !== redirect) return { error: "invalid_grant", description: "client or redirect_uri mismatch" };
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier)));
    if (base64url(digest) !== row.challenge) return { error: "invalid_grant", description: "PKCE verification failed" };
    const grant = this.liveGrant(row.grant_id);
    if (!grant) return { error: "invalid_grant", description: "the guest was revoked" };
    return issueTokens(this.sql, row.grant_id, grant.scope, grant.created);
  }

  async refresh(token: string, client: string): Promise<Issued | Failure> {
    const hash = await sha256Hex(token);
    const row = this.sql.exec<{ grant_id: string; expires: number; used: number }>(
      "SELECT grant_id, expires, used FROM tokens WHERE hash = ? AND kind = 'refresh'", hash).toArray()[0];
    if (!row) return { error: "invalid_grant", description: "unknown refresh token" };
    if (row.used) {
      // Rotation means a refresh token works once. Seeing it again means a copy
      // exists somewhere else, so the whole chain ends.
      dropGrant(this.sql, row.grant_id);
      return { error: "invalid_grant", description: "refresh token reused; sign in again" };
    }
    if (row.expires < Date.now()) return { error: "invalid_grant", description: "refresh token expired" };
    const grant = this.liveGrant(row.grant_id);
    if (!grant || grant.client !== client) return { error: "invalid_grant", description: "grant revoked or client mismatch" };
    this.sql.exec("UPDATE tokens SET used = 1 WHERE hash = ?", hash);
    return issueTokens(this.sql, row.grant_id, grant.scope, grant.created);
  }

  async authenticateAccess(tokenHash: string): Promise<NodeRecord | null> {
    const row = this.sql.exec<{ grant_id: string }>(
      "SELECT grant_id FROM tokens WHERE hash = ? AND kind = 'access' AND expires > ?", tokenHash, Date.now()).toArray()[0];
    const grant = row ? this.liveGrant(row.grant_id) : null;
    if (!grant) return null;
    const guest = this.row(grant.guest);
    return guest && !guest.revoked && guest.kind === "guest" ? toRecord(guest) : null;
  }

  private liveGrant(id: string): { guest: string; client: string; scope: string; created: number } | null {
    const g = this.sql.exec<{ guest: string; client: string; scope: string; created: number }>(
      "SELECT guest, client, scope, created FROM grants WHERE id = ?", id).toArray()[0];
    if (!g || Date.now() - g.created > GRANT_MAX_MS) return null;
    const guest = this.row(g.guest);
    return guest && !guest.revoked ? g : null;
  }

  private async schedule(): Promise<void> {
    if ((await this.ctx.storage.getAlarm()) === null) await this.ctx.storage.setAlarm(Date.now() + 3600_000);
  }

  async alarm(): Promise<void> {
    if (sweep(this.sql) > 0) await this.ctx.storage.setAlarm(Date.now() + 3600_000);
  }
}
