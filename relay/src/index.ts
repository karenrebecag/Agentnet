import { handleMcp } from "./mcp";
import { type Env, type Envelope, type NodeKind, type NodeRecord, TOKEN_RE, bearer, json, randomHex, sha256Hex, splitAddress, timingSafeEqual } from "./shared";

export { Directory } from "./directory";
export { Mailbox } from "./mailbox";

const MAX_ADMIN_BODY = 8 * 1024;

async function authenticate(request: Request, env: Env, kind: NodeKind, secret = bearer(request)): Promise<NodeRecord | null> {
  const directory = env.DIRECTORY.getByName("directory");
  // Malformed tokens never reach the single Directory object, so junk floods
  // cost it nothing.
  if (!secret || !TOKEN_RE.test(secret)) return null;
  const node = await directory.authenticate(await sha256Hex(secret));
  return node && node.kind === kind ? node : null;
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
    let body: { name?: unknown; kind?: unknown; targets?: unknown; inbox?: unknown };
    try {
      body = JSON.parse(raw);
    } catch {
      return json({ error: "invalid JSON" }, 400);
    }
    if (typeof body !== "object" || body === null) return json({ error: "expected a JSON object" }, 400);
    const targets = Array.isArray(body.targets) ? body.targets.filter((t): t is string => typeof t === "string") : [];
    const result = await directory.create(String(body.name ?? ""), body.kind as NodeKind, targets, body.inbox === true);
    return "error" in result ? json(result, 400) : json({ name: body.name, token: result.token }, 201);
  }
  if (path === "/v1/admin/contacts" && request.method === "GET") return json(await directory.listContacts());
  const contact = /^\/v1\/admin\/contacts\/([0-9a-f]{8})\/(approve|deny)$/.exec(path);
  if (contact && request.method === "POST") {
    const approve = contact[2] === "approve";
    const held = await directory.decideContact(contact[1], approve);
    if (!held) return json({ error: "no contact is waiting with that id" }, 404);
    const envelope = JSON.parse(held.envelope) as Envelope;
    const dest = splitAddress(held.address)!;
    if (held.deliver) {
      const sent = await env.MAILBOX.getByName(dest.node).enqueue(envelope);
      if (sent.status !== "rejected") return json({ delivered: true });
      await answerGuest(env, envelope, held.address, `[agentnet] Allowed, but the message could not be delivered: ${sent.error}.`);
      return json({ delivered: false, error: sent.error });
    }
    const why = approve ? "The agent restarted while this waited; ask again." : "The person did not allow this contact.";
    await answerGuest(env, envelope, held.address, `[agentnet] ${why}`);
    return json({ delivered: false });
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

// The guest waits on get_reply for its held message, so it hears the outcome
// as that reply instead of waiting forever.
async function answerGuest(env: Env, envelope: Envelope, from: string, text: string): Promise<void> {
  const guestNode = envelope.from.slice(0, envelope.from.indexOf("."));
  await env.MAILBOX.getByName(guestNode).enqueueReply({
    id: randomHex(16), ts: new Date().toISOString(), from, to: envelope.from, text, origin: "relay", reply_to: envelope.id,
  });
}

// A rotated or revoked token must not keep a socket that was opened with it.
async function closeSockets(env: Env, node: string): Promise<void> {
  await env.MAILBOX.getByName(node).closeAll();
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    const path = url.pathname;
    const browserOrigin = request.headers.get("Origin");

    // No route is for browsers: refusing any Origin blocks cross-site use.
    if (browserOrigin) return new Response("browser origins are not accepted", { status: 403 });

    if (path === "/healthz") return new Response("ok");
    if (path.startsWith("/v1/admin/")) return admin(request, env, path);

    if (path === "/v1/link") {
      const node = await authenticate(request, env, "machine");
      if (!node) return denied(request, env);
      const headers = new Headers(request.headers);
      headers.set("X-Agentnet-Node", node.name);
      headers.delete("Authorization");
      return env.MAILBOX.getByName(node.name).fetch(new Request(request, { headers }));
    }

    if (path === "/mcp") {
      // A connector URL carries the guest token as ?key=, because web clients
      // take a URL and nothing else. The header wins when both are present.
      const guest = await authenticate(request, env, "guest", bearer(request) ?? url.searchParams.get("key"));
      if (!guest) return denied(request, env);
      return handleMcp(request, env, guest);
    }

    return new Response("not found", { status: 404 });
  },
} satisfies ExportedHandler<Env>;
