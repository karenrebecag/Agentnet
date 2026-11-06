import { handleMcp } from "./mcp";
import { type Env, type NodeKind, type NodeRecord, TOKEN_RE, bearer, json, sha256Hex, timingSafeEqual } from "./shared";

export { Directory } from "./directory";
export { Mailbox } from "./mailbox";

const MAX_ADMIN_BODY = 8 * 1024;

async function authenticate(request: Request, env: Env, kind: NodeKind): Promise<NodeRecord | Response> {
  const token = bearer(request);
  // Malformed tokens never reach the single Directory object, so junk floods
  // cost it nothing.
  const node = token && TOKEN_RE.test(token) ? await env.DIRECTORY.getByName("directory").authenticate(await sha256Hex(token)) : null;
  if (node && node.kind === kind) return node;
  return denied(request, env);
}

// Valid tokens are never rate limited. Failures are counted per address so a
// scanner gets 429s instead of an endless supply of 401s.
async function denied(request: Request, env: Env): Promise<Response> {
  const ip = request.headers.get("CF-Connecting-IP") ?? "unknown";
  const outcome = env.AUTH_FAILURES ? await env.AUTH_FAILURES.limit({ key: ip }) : { success: true };
  if (!outcome.success) return new Response("too many failed authentications", { status: 429 });
  return new Response("unauthorized", { status: 401, headers: { "WWW-Authenticate": "Bearer" } });
}

async function admin(request: Request, env: Env, path: string): Promise<Response> {
  const token = bearer(request);
  if (!env.ADMIN_TOKEN || !token || !(await timingSafeEqual(token, env.ADMIN_TOKEN))) return denied(request, env);
  const directory = env.DIRECTORY.getByName("directory");

  if (path === "/v1/admin/nodes" && request.method === "GET") return json(await directory.list());
  if (path === "/v1/admin/nodes" && request.method === "POST") {
    const raw = await request.text();
    if (raw.length > MAX_ADMIN_BODY) return json({ error: "body too large" }, 413);
    let body: { name?: unknown; kind?: unknown; targets?: unknown };
    try {
      body = JSON.parse(raw);
    } catch {
      return json({ error: "invalid JSON" }, 400);
    }
    if (typeof body !== "object" || body === null) return json({ error: "expected a JSON object" }, 400);
    const targets = Array.isArray(body.targets) ? body.targets.filter((t): t is string => typeof t === "string") : [];
    const result = await directory.create(String(body.name ?? ""), body.kind as NodeKind, targets);
    return "error" in result ? json(result, 400) : json({ name: body.name, token: result.token }, 201);
  }
  const match = /^\/v1\/admin\/nodes\/([a-z][a-z0-9-]{0,31})\/(rotate|revoke)$/.exec(path);
  if (match && request.method === "POST") {
    const [, name, action] = match;
    if (action === "rotate") {
      const result = await directory.rotate(name);
      if ("error" in result) return json(result, 404);
      await closeSockets(env, name);
      return json({ token: result.token });
    }
    if (!(await directory.revoke(name))) return json({ error: "unknown node" }, 404);
    await closeSockets(env, name);
    return json({});
  }
  return json({ error: "not found" }, 404);
}

// A rotated or revoked token must not keep a socket that was opened with it.
async function closeSockets(env: Env, node: string): Promise<void> {
  await env.MAILBOX.getByName(node).closeAll();
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    // No browser should ever drive this API; refusing Origin blocks cross-site use.
    if (request.headers.has("Origin")) return new Response("browser origins are not accepted", { status: 403 });

    if (url.pathname === "/healthz") return new Response("ok");
    if (url.pathname.startsWith("/v1/admin/")) return admin(request, env, url.pathname);

    if (url.pathname === "/v1/link") {
      const node = await authenticate(request, env, "machine");
      if (node instanceof Response) return node;
      const headers = new Headers(request.headers);
      headers.set("X-Agentnet-Node", node.name);
      headers.delete("Authorization");
      return env.MAILBOX.getByName(node.name).fetch(new Request(request, { headers }));
    }

    if (url.pathname === "/mcp") {
      const guest = await authenticate(request, env, "guest");
      if (guest instanceof Response) return guest;
      return handleMcp(request, env, guest);
    }

    return new Response("not found", { status: 404 });
  },
} satisfies ExportedHandler<Env>;
