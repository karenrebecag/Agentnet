import type { ClientRecord } from "./directory";
import { consentPage, errorPage, typedCode, waitingPage } from "./consent";
import { type Env, json } from "./shared";

// The relay is its own OAuth 2.1 authorization server for guests that sign in
// through a client (claude.ai, Perplexity, ...). The person proves they own the
// guest with a pairing code bound to the client's host; nothing else is a login.

const SCOPE = "mcp";
const MAX_BODY = 8 * 1024;
const MAX_METADATA = 16 * 1024;
const VERIFIER_RE = /^[A-Za-z0-9._~-]{43,128}$/;
const CHALLENGE_RE = /^[A-Za-z0-9_-]{43}$/;

// A client names itself; the name reaches terminals, so control and format
// characters that could redraw what the approver reads are dropped.
function cleanName(raw: unknown, fallback: string): string {
  const name = typeof raw === "string" ? raw.replace(/[\p{C}]/gu, "").trim().slice(0, 80) : "";
  return name || fallback;
}

const noStore = { "Cache-Control": "no-store", Pragma: "no-cache" };

export function resourceUrl(origin: string): string {
  return `${origin}/mcp`;
}

export function protectedResourceMetadata(origin: string): Response {
  return json({
    resource: resourceUrl(origin),
    authorization_servers: [origin],
    scopes_supported: [SCOPE],
    bearer_methods_supported: ["header"],
  });
}

export function authorizationServerMetadata(origin: string): Response {
  return json({
    issuer: origin,
    authorization_endpoint: `${origin}/oauth/authorize`,
    token_endpoint: `${origin}/oauth/token`,
    registration_endpoint: `${origin}/oauth/register`,
    response_types_supported: ["code"],
    grant_types_supported: ["authorization_code", "refresh_token"],
    code_challenge_methods_supported: ["S256"],
    token_endpoint_auth_methods_supported: ["none"],
    client_id_metadata_document_supported: true,
    scopes_supported: [SCOPE, "offline_access"],
  });
}

/** The 401 that tells a client where to start signing in. */
export function challenge(origin: string, status = 401): Response {
  return new Response("unauthorized", {
    status,
    headers: { "WWW-Authenticate": `Bearer resource_metadata="${origin}/.well-known/oauth-protected-resource", scope="${SCOPE}"` },
  });
}

// ---- redirect URIs ----

function isLoopback(u: URL): boolean {
  return u.protocol === "http:" && (u.hostname === "localhost" || u.hostname === "127.0.0.1");
}

function validRedirect(raw: string): boolean {
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return false;
  }
  return u.hash === "" && u.username === "" && (u.protocol === "https:" || isLoopback(u));
}

/** Exact match, except that a loopback redirect may use any port (RFC 8252 7.3). */
export function redirectMatches(registered: string[], requested: string): boolean {
  if (registered.includes(requested)) return true;
  let r: URL;
  try {
    r = new URL(requested);
  } catch {
    return false;
  }
  if (!isLoopback(r)) return false;
  return registered.some((raw) => {
    const g = new URL(raw);
    return isLoopback(g) && g.hostname === r.hostname && g.pathname === r.pathname && g.search === r.search;
  });
}

// ---- clients ----

/** Finds a client: a registered one, or an https URL that hosts its own metadata. */
async function resolveClient(env: Env, id: string): Promise<ClientRecord | string> {
  const directory = env.DIRECTORY.getByName("directory");
  const known = await directory.client(id);
  if (known) return known;
  if (!id.startsWith("https://")) return "unknown client_id";
  const fetched = await fetchMetadataDocument(id);
  if (typeof fetched === "string") return fetched;
  await directory.cacheMetadataClient(fetched);
  return fetched;
}

// The client_id is a URL a stranger chose, so the fetch is narrow: https, a
// public hostname, the default port, no redirects, a short timeout, a small body.
async function fetchMetadataDocument(id: string): Promise<ClientRecord | string> {
  let url: URL;
  try {
    url = new URL(id);
  } catch {
    return "client_id is not a URL";
  }
  const host = url.hostname;
  if (url.port !== "" || url.pathname === "/" || url.username !== "" || url.hash !== "") return "client_id URL is not allowed";
  if (host === "localhost" || /^[\d.]+$/.test(host) || host.startsWith("[") || !host.includes(".")) return "client_id host is not allowed";
  let res: Response;
  try {
    res = await fetch(url.toString(), { redirect: "manual", headers: { Accept: "application/json" }, signal: AbortSignal.timeout(5000) });
  } catch {
    return "could not fetch the client metadata document";
  }
  if (res.status !== 200) return `client metadata document returned ${res.status}`;
  const text = await readCapped(res, MAX_METADATA);
  if (text === null) return "client metadata document is too large";
  let doc: { client_id?: unknown; client_name?: unknown; redirect_uris?: unknown; token_endpoint_auth_method?: unknown };
  try {
    doc = JSON.parse(text);
  } catch {
    return "client metadata document is not JSON";
  }
  if (doc.client_id !== id) return "client metadata document does not name itself";
  const redirects = Array.isArray(doc.redirect_uris) ? doc.redirect_uris.filter((r): r is string => typeof r === "string" && validRedirect(r)) : [];
  if (redirects.length === 0) return "client metadata document has no usable redirect_uris";
  if (doc.token_endpoint_auth_method !== undefined && doc.token_endpoint_auth_method !== "none") return "only public clients are supported";
  const name = cleanName(doc.client_name, host);
  return { id, kind: "cimd", name, redirects };
}

// Reads at most max bytes, so a huge response cannot fill the Worker's memory.
async function readCapped(res: Response, max: number): Promise<string | null> {
  if (Number(res.headers.get("Content-Length") ?? "0") > max || !res.body) return res.body ? null : "";
  const reader = res.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > max) {
      await reader.cancel();
      return null;
    }
    chunks.push(value);
  }
  const all = new Uint8Array(size);
  let at = 0;
  for (const c of chunks) {
    all.set(c, at);
    at += c.byteLength;
  }
  return new TextDecoder().decode(all);
}

function clientIp(request: Request): string {
  return request.headers.get("CF-Connecting-IP") ?? "unknown";
}

export async function register(request: Request, env: Env): Promise<Response> {
  const raw = await request.text();
  if (raw.length > MAX_BODY) return json({ error: "invalid_client_metadata", error_description: "body too large" }, 400);
  let body: { redirect_uris?: unknown; client_name?: unknown; token_endpoint_auth_method?: unknown };
  try {
    body = JSON.parse(raw);
  } catch {
    return json({ error: "invalid_client_metadata", error_description: "invalid JSON" }, 400);
  }
  if (typeof body !== "object" || body === null) return json({ error: "invalid_client_metadata" }, 400);
  const redirects = Array.isArray(body.redirect_uris) ? body.redirect_uris.filter((r): r is string => typeof r === "string") : [];
  if (redirects.length === 0 || redirects.length > 5 || !redirects.every(validRedirect)) {
    return json({ error: "invalid_redirect_uri", error_description: "1 to 5 https or loopback redirect_uris" }, 400);
  }
  if (body.token_endpoint_auth_method !== undefined && body.token_endpoint_auth_method !== "none") {
    return json({ error: "invalid_client_metadata", error_description: "only token_endpoint_auth_method none is supported" }, 400);
  }
  const name = cleanName(body.client_name, new URL(redirects[0]).host);
  const result = await env.DIRECTORY.getByName("directory").register(name, redirects, clientIp(request));
  if ("error" in result) return json({ error: "temporarily_unavailable", error_description: result.error }, 429);
  return json({
    client_id: result.id,
    client_id_issued_at: Math.floor(Date.now() / 1000),
    client_name: result.name,
    redirect_uris: result.redirects,
    grant_types: ["authorization_code", "refresh_token"],
    response_types: ["code"],
    token_endpoint_auth_method: "none",
  }, 201);
}

// ---- authorize ----

export async function authorizeGet(request: Request, env: Env, origin: string): Promise<Response> {
  const p = new URL(request.url).searchParams;
  const wait = p.get("wait");
  if (wait !== null) return pollWaiting(env, wait);
  const clientId = p.get("client_id") ?? "";
  const redirect = p.get("redirect_uri") ?? "";
  const client = clientId ? await resolveClient(env, clientId) : "client_id is required";
  if (typeof client === "string") return errorPage(client, 400);
  // Until the redirect is proven to belong to the client, errors stay on this
  // page: redirecting them would make the relay an open redirector.
  if (!redirectMatches(client.redirects, redirect)) return errorPage("redirect_uri is not registered for this client", 400);
  if (p.get("response_type") !== "code") return errorPage("response_type must be code", 400);
  const challengeValue = p.get("code_challenge") ?? "";
  if (p.get("code_challenge_method") !== "S256" || !CHALLENGE_RE.test(challengeValue)) return errorPage("PKCE with S256 is required", 400);
  const resource = p.get("resource");
  if (resource !== null && resource.replace(/\/$/, "") !== resourceUrl(origin)) return errorPage("resource must be this relay's /mcp", 400);
  const state = p.get("state") ?? "";
  if (state.length > 1024) return errorPage("state is too long", 400);
  const id = await env.DIRECTORY.getByName("directory").startRequest({ client: client.id, redirect, challenge: challengeValue, state, scope: SCOPE });
  return consentPage({ request: id, client: client.name, clientId: client.id, redirect });
}

export async function authorizePost(request: Request, env: Env): Promise<Response> {
  const form = await request.formData();
  const id = String(form.get("request") ?? "");
  const directory = env.DIRECTORY.getByName("directory");
  if (!id.startsWith("req_")) return errorPage("this sign-in page is not valid; start again from the client", 400);
  if (form.get("action") === "deny") {
    const req = await directory.deny(id);
    if (!req) return errorPage("this sign-in page expired", 400);
    const back = new URL(req.redirect);
    back.searchParams.set("error", "access_denied");
    if (req.state) back.searchParams.set("state", req.state);
    return Response.redirect(back.toString(), 303);
  }
  const before = await directory.peekRequest(id);
  const result = await directory.approve(id, typedCode(form), clientIp(request));
  if ("redirect" in result) return Response.redirect(result.redirect, 303);
  if ("waiting" in result) return pollWaiting(env, result.waiting);
  if ("retry" in result && before) {
    const client = await directory.client(before.client);
    return consentPage({ request: result.retry, client: client?.name ?? before.client, clientId: before.client, redirect: before.redirect, message: result.message });
  }
  return errorPage("description" in result ? result.description : "sign-in failed", 400);
}

// The browser reloads this until the person answers on their machine; only
// then does the client get a code.
async function pollWaiting(env: Env, poll: string): Promise<Response> {
  if (!poll.startsWith("anw_") || poll.length > 128) return errorPage("this sign-in page is not valid; start again from the client", 400);
  const result = await env.DIRECTORY.getByName("directory").pollSignin(poll);
  if ("redirect" in result) return Response.redirect(result.redirect, 303);
  if ("waiting" in result) return waitingPage(poll, result.waiting);
  return errorPage(result.description, 400);
}

// ---- token ----

export async function token(request: Request, env: Env, origin: string): Promise<Response> {
  const type = request.headers.get("Content-Type") ?? "";
  if (!type.startsWith("application/x-www-form-urlencoded")) return tokenError("invalid_request", "use application/x-www-form-urlencoded");
  const raw = await request.text();
  if (raw.length > MAX_BODY) return tokenError("invalid_request", "body too large");
  const p = new URLSearchParams(raw);
  const clientId = p.get("client_id") ?? "";
  if (!clientId) return tokenError("invalid_client", "client_id is required", 401);
  const resource = p.get("resource");
  if (resource !== null && resource.replace(/\/$/, "") !== resourceUrl(origin)) return tokenError("invalid_target", "resource must be this relay's /mcp");
  const directory = env.DIRECTORY.getByName("directory");
  let result;
  switch (p.get("grant_type")) {
    case "authorization_code": {
      const verifier = p.get("code_verifier") ?? "";
      if (!VERIFIER_RE.test(verifier)) return tokenError("invalid_request", "code_verifier is required");
      result = await directory.exchange(p.get("code") ?? "", clientId, p.get("redirect_uri") ?? "", verifier);
      break;
    }
    case "refresh_token":
      result = await directory.refresh(p.get("refresh_token") ?? "", clientId);
      break;
    default:
      return tokenError("unsupported_grant_type", "authorization_code or refresh_token");
  }
  if ("error" in result) return tokenError(result.error, result.description);
  return new Response(JSON.stringify({ ...result, token_type: "Bearer" }), {
    headers: { "Content-Type": "application/json", ...noStore },
  });
}

function tokenError(error: string, description: string, status = 400): Response {
  return new Response(JSON.stringify({ error, error_description: description }), {
    status,
    headers: { "Content-Type": "application/json", ...noStore },
  });
}
