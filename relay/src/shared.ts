export interface Env {
  DIRECTORY: DurableObjectNamespace<import("./directory").Directory>;
  MAILBOX: DurableObjectNamespace<import("./mailbox").Mailbox>;
  AUTH_FAILURES?: RateLimit;
  OAUTH_LIMIT?: RateLimit;
  ADMIN_TOKEN: string;
}

export type NodeKind = "machine" | "guest";
export type Origin = "relay" | "external";

export interface AgentInfo {
  name: string;
  kind: string;
  accept_external: boolean;
  // Changes every time the agent joins again, so an approval given to one
  // session does not carry over to the next process under the same name.
  session?: string;
}

export interface Envelope {
  id: string;
  ts: string;
  from: string;
  to: string;
  text: string;
  origin: Origin;
  reply_to?: string;
}

export interface NodeRecord {
  name: string;
  kind: NodeKind;
  // A guest with inbox on can be written to by its targets without asking first.
  inbox: boolean;
  targets: string[];
  revoked: boolean;
  created: string;
  last_seen: string | null;
  agents: AgentInfo[];
}

export const NODE_RE = /^[a-z][a-z0-9-]{0,31}$/;
export const AGENT_RE = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/;
// `ext.<guest>` is how `expose` names local guest inboxes, so a node called
// `ext` would collide with them on every machine.
export const RESERVED_NODES = new Set(["ext", "local", "admin"]);
// Guests have one inbox, addressed `<guest>.mcp`, where replies to their asks land.
export const GUEST_AGENT = "mcp";

export const MAX_TEXT_MACHINE = 64 * 1024;
export const MAX_TEXT_GUEST = 16 * 1024;

export function splitAddress(address: string): { node: string; agent: string } | null {
  const dot = address.indexOf(".");
  if (dot <= 0) return null;
  const node = address.slice(0, dot);
  const agent = address.slice(dot + 1);
  if (!NODE_RE.test(node) || !AGENT_RE.test(agent)) return null;
  return { node, agent };
}

export function randomHex(bytes: number): string {
  const buf = new Uint8Array(bytes);
  crypto.getRandomValues(buf);
  return [...buf].map((b) => b.toString(16).padStart(2, "0")).join("");
}

export function base64url(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

// Every secret the relay issues is a prefix plus 32 random bytes. The prefix
// says what it is, so a refresh token can never be presented as a node token.
export function newSecret(prefix: string): string {
  const buf = new Uint8Array(32);
  crypto.getRandomValues(buf);
  return prefix + base64url(buf);
}

export function newToken(): string {
  return newSecret("an_");
}

export async function sha256Hex(text: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

// Hashing first makes both sides the same length, so not even the length of
// the secret leaks through an early exit.
export async function timingSafeEqual(a: string, b: string): Promise<boolean> {
  const [ha, hb] = await Promise.all([sha256Hex(a), sha256Hex(b)]);
  let diff = 0;
  for (let i = 0; i < ha.length; i++) diff |= ha.charCodeAt(i) ^ hb.charCodeAt(i);
  return diff === 0;
}

// Shape of every token newToken() issues: "an_" plus 32 bytes in base64url.
export const TOKEN_RE = /^an_[A-Za-z0-9_-]{43}$/;
// OAuth access and refresh tokens, issued to guests that signed in through a client.
export const ACCESS_RE = /^ana_[A-Za-z0-9_-]{43}$/;
export const REFRESH_RE = /^anr_[A-Za-z0-9_-]{43}$/;

export function bearer(request: Request): string | null {
  const header = request.headers.get("Authorization") ?? "";
  const match = /^Bearer\s+(\S+)$/i.exec(header);
  return match ? match[1] : null;
}

export function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

export function now(): string {
  return new Date().toISOString();
}
