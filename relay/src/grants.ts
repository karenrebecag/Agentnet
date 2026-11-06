import { newSecret, randomHex, sha256Hex } from "./shared";

// OAuth state for guests that sign in through a client such as claude.ai. It
// lives in the Directory's database, next to the nodes, so revoking a guest and
// killing its grants happen in one place with no cache in between.

export const PAIR_TTL_MS = 10 * 60_000;
export const PAIR_FAILURE_LIMIT = 5;
export const REQUEST_TTL_MS = 10 * 60_000;
export const CODE_TTL_MS = 60_000;
export const ACCESS_TTL_MS = 3600_000;
export const REFRESH_TTL_MS = 30 * 86_400_000;
export const UNUSED_CLIENT_TTL_MS = 3600_000;
export const CIMD_CACHE_MS = 3600_000;
export const MAX_PENDING_CLIENTS = 200;
export const MAX_REGISTRATIONS_PER_IP = 10;
export const MAX_REQUESTS = 1000;
// A sign-in lasts at most this long, however often its refresh token rotates.
export const GRANT_MAX_MS = 90 * 86_400_000;
// Wrong pairing codes are counted per address, in a window that forgets.
export const FAILURE_WINDOW_MS = 15 * 60_000;
// A code with no host waits for a yes on the person's machine this long.
export const SIGNIN_TTL_MS = 10 * 60_000;
export const MAX_SIGNINS = 50;
// Used refresh tokens are kept only to catch a replay; a few per grant is enough.
export const USED_REFRESH_KEPT = 5;

// Hosts where anyone can get a subdomain. A pairing bound to one of these would
// accept a redirect to every tenant's site, so they cannot be used with --for.
const SHARED_SUFFIXES = [
  "workers.dev", "pages.dev", "trycloudflare.com", "vercel.app", "netlify.app", "github.io", "githubusercontent.com",
  "herokuapp.com", "fly.dev", "onrender.com", "web.app", "firebaseapp.com", "appspot.com", "azurewebsites.net",
  "cloudfront.net", "amazonaws.com", "ngrok.app", "ngrok-free.app", "ngrok.io", "glitch.me", "replit.app", "repl.co",
];

export function sharedSuffix(host: string): boolean {
  return SHARED_SUFFIXES.some((s) => host === s || host.endsWith("." + s));
}

// Crockford base32 without I, L, O, U: nothing a person can misread.
const PAIR_ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";

export function schema(sql: SqlStorage): void {
  sql.exec(`CREATE TABLE IF NOT EXISTS pairings (
    hash TEXT PRIMARY KEY, guest TEXT NOT NULL, host TEXT NOT NULL, expires INTEGER NOT NULL)`);
  sql.exec(`CREATE TABLE IF NOT EXISTS clients (
    id TEXT PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL, redirects TEXT NOT NULL,
    created INTEGER NOT NULL, fetched INTEGER NOT NULL DEFAULT 0, used INTEGER NOT NULL DEFAULT 0,
    ip TEXT NOT NULL DEFAULT '')`);
  sql.exec(`CREATE TABLE IF NOT EXISTS requests (
    hash TEXT PRIMARY KEY, client TEXT NOT NULL, redirect TEXT NOT NULL, challenge TEXT NOT NULL,
    state TEXT NOT NULL, scope TEXT NOT NULL, expires INTEGER NOT NULL)`);
  sql.exec("CREATE TABLE IF NOT EXISTS failures (ip TEXT NOT NULL, at INTEGER NOT NULL)");
  sql.exec("CREATE INDEX IF NOT EXISTS failures_ip ON failures (ip, at)");
  sql.exec(`CREATE TABLE IF NOT EXISTS codes (
    hash TEXT PRIMARY KEY, grant_id TEXT NOT NULL, client TEXT NOT NULL, redirect TEXT NOT NULL,
    challenge TEXT NOT NULL, expires INTEGER NOT NULL, used INTEGER NOT NULL DEFAULT 0)`);
  sql.exec(`CREATE TABLE IF NOT EXISTS grants (
    id TEXT PRIMARY KEY, guest TEXT NOT NULL, client TEXT NOT NULL, scope TEXT NOT NULL, created INTEGER NOT NULL)`);
  sql.exec(`CREATE TABLE IF NOT EXISTS tokens (
    hash TEXT PRIMARY KEY, grant_id TEXT NOT NULL, kind TEXT NOT NULL, expires INTEGER NOT NULL,
    used INTEGER NOT NULL DEFAULT 0)`);
  sql.exec("CREATE INDEX IF NOT EXISTS tokens_grant ON tokens (grant_id)");
  sql.exec(`CREATE TABLE IF NOT EXISTS signins (
    id TEXT PRIMARY KEY, poll TEXT NOT NULL UNIQUE, guest TEXT NOT NULL, client TEXT NOT NULL, client_name TEXT NOT NULL,
    redirect TEXT NOT NULL, challenge TEXT NOT NULL, state TEXT NOT NULL, scope TEXT NOT NULL, host TEXT NOT NULL,
    ip TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'waiting', created INTEGER NOT NULL, expires INTEGER NOT NULL)`);
}

export function newPairCode(): string {
  const bytes = new Uint8Array(8);
  crypto.getRandomValues(bytes);
  const chars = [...bytes].map((b) => PAIR_ALPHABET[b % PAIR_ALPHABET.length]).join("");
  return `${chars.slice(0, 4)}-${chars.slice(4)}`;
}

// Normalizes what a person typed: case, spaces and dashes do not matter.
export function normalizePairCode(input: string): string {
  return input.toUpperCase().replace(/[^0-9A-Z]/g, "");
}

export function hostAllowed(redirect: string, host: string): boolean {
  let url: URL;
  try {
    url = new URL(redirect);
  } catch {
    return false;
  }
  if (host === "localhost") {
    return url.protocol === "http:" && (url.hostname === "localhost" || url.hostname === "127.0.0.1");
  }
  const h = url.hostname.toLowerCase();
  return url.protocol === "https:" && (h === host || h.endsWith("." + host));
}

export interface Issued {
  access_token: string;
  refresh_token: string;
  expires_in: number;
  scope: string;
}

export async function issueTokens(sql: SqlStorage, grantId: string, scope: string, grantCreated: number): Promise<Issued> {
  const access = newSecret("ana_");
  const refresh = newSecret("anr_");
  const t = Date.now();
  sql.exec(
    "DELETE FROM tokens WHERE grant_id = ? AND kind = 'refresh' AND used = 1 AND hash NOT IN " +
      "(SELECT hash FROM tokens WHERE grant_id = ? AND kind = 'refresh' AND used = 1 ORDER BY expires DESC LIMIT ?)",
    grantId, grantId, USED_REFRESH_KEPT);
  sql.exec("DELETE FROM tokens WHERE grant_id = ? AND kind = 'access' AND expires < ?", grantId, t);
  sql.exec("INSERT INTO tokens (hash, grant_id, kind, expires) VALUES (?, ?, 'access', ?)", await sha256Hex(access), grantId, t + ACCESS_TTL_MS);
  sql.exec("INSERT INTO tokens (hash, grant_id, kind, expires) VALUES (?, ?, 'refresh', ?)", await sha256Hex(refresh), grantId,
    Math.min(t + REFRESH_TTL_MS, grantCreated + GRANT_MAX_MS));
  return { access_token: access, refresh_token: refresh, expires_in: ACCESS_TTL_MS / 1000, scope };
}

export function dropGrant(sql: SqlStorage, grantId: string): void {
  sql.exec("DELETE FROM tokens WHERE grant_id = ?", grantId);
  sql.exec("DELETE FROM codes WHERE grant_id = ?", grantId);
  sql.exec("DELETE FROM grants WHERE id = ?", grantId);
}

// Everything a revoked guest could still use: pairings, grants, tokens and the
// registered clients that only ever signed in as this guest.
export function dropGuest(sql: SqlStorage, guest: string): void {
  sql.exec("DELETE FROM pairings WHERE guest = ?", guest);
  sql.exec("DELETE FROM signins WHERE guest = ?", guest);
  const grants = sql.exec<{ id: string; client: string }>("SELECT id, client FROM grants WHERE guest = ?", guest).toArray();
  for (const g of grants) {
    dropGrant(sql, g.id);
    const other = sql.exec("SELECT 1 FROM grants WHERE client = ?", g.client).toArray().length > 0;
    if (!other) sql.exec("DELETE FROM clients WHERE id = ? AND kind = 'dcr'", g.client);
  }
}

export function sweep(sql: SqlStorage): number {
  const t = Date.now();
  sql.exec("DELETE FROM pairings WHERE expires < ?", t);
  sql.exec("DELETE FROM requests WHERE expires < ?", t);
  sql.exec("DELETE FROM failures WHERE at < ?", t - FAILURE_WINDOW_MS);
  sql.exec("DELETE FROM codes WHERE expires < ?", t);
  sql.exec("DELETE FROM signins WHERE expires < ?", t);
  sql.exec("DELETE FROM tokens WHERE expires < ?", t);
  sql.exec("DELETE FROM grants WHERE id NOT IN (SELECT DISTINCT grant_id FROM tokens) AND created < ?", t - CODE_TTL_MS);
  sql.exec("DELETE FROM clients WHERE kind = 'dcr' AND used = 0 AND created < ?", t - UNUSED_CLIENT_TTL_MS);
  sql.exec("DELETE FROM clients WHERE kind = 'cimd' AND used = 0 AND fetched < ?", t - CIMD_CACHE_MS);
  return sql.exec<{ n: number }>(
    "SELECT (SELECT COUNT(*) FROM pairings) + (SELECT COUNT(*) FROM tokens) + (SELECT COUNT(*) FROM clients) + (SELECT COUNT(*) FROM signins) AS n",
  ).toArray()[0].n;
}

export function newId(): string {
  return randomHex(16);
}
