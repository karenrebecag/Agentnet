import { randomHex } from "./shared";

// Open guests reach an agent session only after the person allows that pair.
// The rows live in the Directory's database, next to the nodes, so revoking a
// guest and dropping its contacts happen in one place.

// An open guest's first message to an agent waits this long for a yes, and a
// refusal holds this long before the same pair may ask again.
export const CONTACT_TTL_MS = 10 * 60_000;
export const MAX_CONTACTS_PER_GUEST = 10;
export const MAX_CONTACTS = 200;
// An approval lasts as long as the agent's session; this only bounds the row.
export const CONTACT_KEPT_MS = 30 * 86_400_000;

export function schema(sql: SqlStorage): void {
  sql.exec(`CREATE TABLE IF NOT EXISTS contacts (
    id TEXT PRIMARY KEY, guest TEXT NOT NULL, address TEXT NOT NULL, session TEXT NOT NULL,
    status TEXT NOT NULL, envelope TEXT, created INTEGER NOT NULL, expires INTEGER NOT NULL)`);
  sql.exec("CREATE INDEX IF NOT EXISTS contacts_pair ON contacts (guest, address, session)");
}

export function dropGuest(sql: SqlStorage, guest: string): void {
  sql.exec("DELETE FROM contacts WHERE guest = ?", guest);
}

export function sweep(sql: SqlStorage): number {
  sql.exec("DELETE FROM contacts WHERE expires < ?", Date.now());
  return sql.exec<{ n: number }>("SELECT COUNT(*) AS n FROM contacts").toArray()[0].n;
}

export function newId(): string {
  return randomHex(4);
}
