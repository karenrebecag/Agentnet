import { SELF } from "cloudflare:test";
import { describe, expect, it } from "vitest";

const ADMIN = "test-admin-token";
const BASE = "https://relay.test";
const CLAUDE_CB = "https://claude.ai/api/mcp/auth_callback";
let counter = 0;
// The OAuth routes are rate limited per address; each test speaks from its own.
const ip = () => ({ "CF-Connecting-IP": `192.0.2.${counter % 250}` });
const unique = (p: string) => `${p}${++counter}${Math.random().toString(36).slice(2, 6)}`;

async function admin(path: string, body?: unknown) {
  return SELF.fetch(`${BASE}${path}`, {
    method: body === undefined ? "GET" : "POST",
    headers: { Authorization: `Bearer ${ADMIN}`, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

async function guest(targets: string[], inbox = false) {
  const name = unique("g");
  const res = await admin("/v1/admin/nodes", { name, kind: "guest", targets, inbox });
  expect(res.status).toBe(201);
  return { name, token: ((await res.json()) as { token: string }).token };
}

async function pair(name: string, host = ""): Promise<string> {
  const res = await admin(`/v1/admin/nodes/${name}/pair`, host ? { for: host } : {});
  expect(res.status).toBe(201);
  return ((await res.json()) as { code: string }).code;
}

async function registerClient(redirect = CLAUDE_CB): Promise<string> {
  const res = await SELF.fetch(`${BASE}/oauth/register`, {
    method: "POST",
    headers: { "Content-Type": "application/json", ...ip() },
    body: JSON.stringify({ client_name: "Test client", redirect_uris: [redirect] }),
  });
  expect(res.status).toBe(201);
  return ((await res.json()) as { client_id: string }).client_id;
}

function b64url(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

async function pkce() {
  const verifier = b64url(crypto.getRandomValues(new Uint8Array(32)));
  const challenge = b64url(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier))));
  return { verifier, challenge };
}

async function consent(clientId: string, challenge: string, redirect = CLAUDE_CB): Promise<string> {
  const q = new URLSearchParams({
    response_type: "code", client_id: clientId, redirect_uri: redirect, code_challenge: challenge,
    code_challenge_method: "S256", state: "st4te", resource: `${BASE}/mcp`,
  });
  const res = await SELF.fetch(`${BASE}/oauth/authorize?${q}`, { headers: ip() });
  expect(res.status).toBe(200);
  const html = await res.text();
  return /name="request" value="(req_[^"]+)"/.exec(html)![1];
}

async function submit(request: string, code: string, origin = BASE, ip = `198.51.100.${counter % 250}`) {
  return SELF.fetch(`${BASE}/oauth/authorize`, {
    method: "POST",
    redirect: "manual",
    headers: { "Content-Type": "application/x-www-form-urlencoded", Origin: origin, "CF-Connecting-IP": ip },
    body: new URLSearchParams({ request, code, action: "approve" }).toString(),
  });
}

async function tokenCall(params: Record<string, string>) {
  return SELF.fetch(`${BASE}/oauth/token`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded", ...ip() },
    body: new URLSearchParams(params).toString(),
  });
}

async function signIn(guestName: string, host = "claude.ai") {
  const clientId = await registerClient();
  const { verifier, challenge } = await pkce();
  const request = await consent(clientId, challenge);
  const res = await submit(request, await pair(guestName, host));
  expect(res.status).toBe(303);
  const back = new URL(res.headers.get("Location")!);
  expect(back.searchParams.get("state")).toBe("st4te");
  const tok = await tokenCall({
    grant_type: "authorization_code", code: back.searchParams.get("code")!, redirect_uri: CLAUDE_CB,
    client_id: clientId, code_verifier: verifier, resource: `${BASE}/mcp`,
  });
  expect(tok.status).toBe(200);
  return { clientId, ...((await tok.json()) as { access_token: string; refresh_token: string }) };
}

async function tools(token: string) {
  return SELF.fetch(`${BASE}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list" }),
  });
}

describe("discovery", () => {
  it("points an unauthenticated client at the authorization server", async () => {
    const res = await SELF.fetch(`${BASE}/mcp`, { method: "POST" });
    expect(res.status).toBe(401);
    expect(res.headers.get("WWW-Authenticate")).toContain(`resource_metadata="${BASE}/.well-known/oauth-protected-resource"`);
    const prm = (await (await SELF.fetch(`${BASE}/.well-known/oauth-protected-resource`)).json()) as any;
    expect(prm).toMatchObject({ resource: `${BASE}/mcp`, authorization_servers: [BASE] });
    const as = (await (await SELF.fetch(`${BASE}/.well-known/oauth-authorization-server`)).json()) as any;
    expect(as.code_challenge_methods_supported).toEqual(["S256"]);
    expect(as.client_id_metadata_document_supported).toBe(true);
    expect(as.token_endpoint_auth_methods_supported).toEqual(["none"]);
  });
});

describe("sign-in", () => {
  it("gives a client a token that acts as the paired guest, and refresh rotates", async () => {
    const g = await guest(["m.claude"]);
    const s = await signIn(g.name);
    expect((await tools(s.access_token)).status).toBe(200);

    const next = await tokenCall({ grant_type: "refresh_token", refresh_token: s.refresh_token, client_id: s.clientId });
    expect(next.status).toBe(200);
    const fresh = (await next.json()) as { access_token: string };
    // The old refresh token again means a copy leaked: the whole chain dies.
    const reuse = await tokenCall({ grant_type: "refresh_token", refresh_token: s.refresh_token, client_id: s.clientId });
    expect(((await reuse.json()) as any).error).toBe("invalid_grant");
    expect((await tools(fresh.access_token)).status).toBe(401);
  });

  it("only accepts a code for the host it was issued for", async () => {
    const g = await guest(["m.claude"]);
    const clientId = await registerClient();
    const { challenge } = await pkce();
    const res = await submit(await consent(clientId, challenge), await pair(g.name, "perplexity.ai"));
    expect(res.status).toBe(200);
    expect(await res.text()).toContain("That code is for perplexity.ai, but this client returns to claude.ai");
  });

  it("refuses the consent form from another origin", async () => {
    const g = await guest(["m.claude"]);
    const { challenge } = await pkce();
    const request = await consent(await registerClient(), challenge);
    expect((await submit(request, await pair(g.name, "claude.ai"), "https://evil.example")).status).toBe(403);
  });

  it("rejects a token request with the wrong PKCE verifier", async () => {
    const g = await guest(["m.claude"]);
    const clientId = await registerClient();
    const { challenge } = await pkce();
    const res = await submit(await consent(clientId, challenge), await pair(g.name, "claude.ai"));
    const code = new URL(res.headers.get("Location")!).searchParams.get("code")!;
    const tok = await tokenCall({
      grant_type: "authorization_code", code, redirect_uri: CLAUDE_CB, client_id: clientId,
      code_verifier: (await pkce()).verifier,
    });
    expect(((await tok.json()) as any).error).toBe("invalid_grant");
  });

  it("locks out an address after five wrong codes without cancelling anyone's pairing", async () => {
    const g = await guest(["m.claude"]);
    const clientId = await registerClient();
    const { challenge } = await pkce();
    const good = await pair(g.name, "claude.ai");
    let request = await consent(clientId, challenge);
    for (let i = 0; i < 5; i++) {
      const html = await (await submit(request, "WRNG-CODE", BASE, "203.0.113.9")).text();
      request = /name="request" value="(req_[^"]+)"/.exec(html)![1];
    }
    const blocked = await submit(request, good, BASE, "203.0.113.9");
    expect(await blocked.text()).toContain("too many wrong codes");
    // The person's own sign-in, from another address, still works.
    const fresh = await consent(clientId, challenge);
    expect((await submit(fresh, good, BASE, "203.0.113.10")).status).toBe(303);
  });

  it("refuses to bind a code to a host where anyone gets a subdomain", async () => {
    const g = await guest(["m.claude"]);
    expect((await admin(`/v1/admin/nodes/${g.name}/pair`, { for: "vercel.app" })).status).toBe(400);
  });

  it("rotating the guest ends the sessions it signed in", async () => {
    const g = await guest(["m.claude"]);
    const s = await signIn(g.name);
    await admin(`/v1/admin/nodes/${g.name}/rotate`, {});
    expect((await tools(s.access_token)).status).toBe(401);
  });

  it("does not resolve a client_id that points at an IP address", async () => {
    const { challenge } = await pkce();
    const q = new URLSearchParams({
      response_type: "code", client_id: "https://127.0.0.1/client.json", redirect_uri: CLAUDE_CB,
      code_challenge: challenge, code_challenge_method: "S256",
    });
    expect((await SELF.fetch(`${BASE}/oauth/authorize?${q}`)).status).toBe(400);
  });

  it("revoking the guest ends every token it signed in with", async () => {
    const g = await guest(["m.claude"]);
    const s = await signIn(g.name);
    await admin(`/v1/admin/nodes/${g.name}/revoke`, {});
    expect((await tools(s.access_token)).status).toBe(401);
    const r = await tokenCall({ grant_type: "refresh_token", refresh_token: s.refresh_token, client_id: s.clientId });
    expect(((await r.json()) as any).error).toBe("invalid_grant");
  });
});

describe("client names", () => {
  it("drops control characters a client puts in its name", async () => {
    const res = await SELF.fetch(`${BASE}/oauth/register`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...ip() },
      body: JSON.stringify({ client_name: "Claude\u001b[2K\r\nclaude.ai\tok", redirect_uris: [CLAUDE_CB] }),
    });
    expect(((await res.json()) as { client_name: string }).client_name).toBe("Claude[2Kclaude.aiok");
  });
});

describe("sign-in with a code that names no host", () => {
  async function waiting(guestName: string) {
    const clientId = await registerClient();
    const { verifier, challenge } = await pkce();
    const res = await submit(await consent(clientId, challenge), await pair(guestName));
    expect(res.status).toBe(200);
    const html = await res.text();
    const poll = /wait=(anw_[^"]+)"/.exec(html)![1];
    const listed = (await (await admin("/v1/admin/signins")).json()) as { id: string; host: string; guest: string }[];
    const mine = listed.find((s) => s.guest === guestName)!;
    expect(mine.host).toBe("claude.ai");
    const again = () => SELF.fetch(`${BASE}/oauth/authorize?wait=${poll}`, { redirect: "manual", headers: ip() });
    return { clientId, verifier, id: mine.id, again };
  }

  it("holds the client until the person approves the host on their machine", async () => {
    const g = await guest(["m.claude"]);
    const w = await waiting(g.name);
    expect((await w.again()).status).toBe(200);
    expect((await admin(`/v1/admin/signins/${w.id}/approve`, {})).status).toBe(200);
    const back = await w.again();
    expect(back.status).toBe(303);
    const tok = await tokenCall({
      grant_type: "authorization_code", code: new URL(back.headers.get("Location")!).searchParams.get("code")!,
      redirect_uri: CLAUDE_CB, client_id: w.clientId, code_verifier: w.verifier,
    });
    expect((await tools(((await tok.json()) as { access_token: string }).access_token)).status).toBe(200);
    // The page cannot be replayed into a second code.
    expect((await w.again()).status).toBe(400);
  });

  it("sends the client away with access_denied when the person says no", async () => {
    const g = await guest(["m.claude"]);
    const w = await waiting(g.name);
    await admin(`/v1/admin/signins/${w.id}/deny`, {});
    const back = await w.again();
    expect(new URL(back.headers.get("Location")!).searchParams.get("error")).toBe("access_denied");
  });
});

describe("inbox", () => {
  async function link(name: string, token: string, agent: string) {
    const res = await SELF.fetch(`${BASE}/v1/link`, { headers: { Upgrade: "websocket", Authorization: `Bearer ${token}` } });
    const ws = res.webSocket!;
    const frames: any[] = [];
    ws.accept();
    ws.addEventListener("message", (e) => frames.push(JSON.parse(e.data as string)));
    ws.send(JSON.stringify({ type: "hello", agents: [{ name: agent, kind: "claude", accept_external: true }] }));
    await new Promise((r) => setTimeout(r, 200));
    return {
      send: async (to: string) => {
        ws.send(JSON.stringify({ type: "send", ref: "r", from: agent, to, text: "news" }));
        for (let i = 0; i < 40 && !frames.some((f) => f.type === "sent"); i++) await new Promise((r) => setTimeout(r, 50));
        return frames.splice(frames.findIndex((f) => f.type === "sent"), 1)[0];
      },
    };
  }

  async function call(token: string, name: string) {
    const res = await SELF.fetch(`${BASE}/mcp`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/call", params: { name, arguments: { wait_seconds: 1 } } }),
    });
    return ((await res.json()) as any).result;
  }

  it("lets a target write first only to a guest that opted in", async () => {
    const m = unique("m");
    const mTok = ((await (await admin("/v1/admin/nodes", { name: m, kind: "machine" })).json()) as { token: string }).token;
    const machine = await link(m, mTok, "claude");
    const open = await guest([`${m}.claude`], true);
    const closed = await guest([`${m}.claude`], false);

    expect((await machine.send(`${open.name}.mcp`)).status).not.toBe("rejected");
    expect((await machine.send(`${closed.name}.mcp`)).status).toBe("rejected");

    const got = await call(open.token, "wait");
    expect(got.structuredContent.messages.map((x: any) => x.text)).toEqual(["news"]);
    expect((await call(open.token, "inbox")).structuredContent.messages).toEqual([]);
  });
});
