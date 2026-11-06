import { SELF } from "cloudflare:test";
import { describe, expect, it } from "vitest";

const ADMIN = "test-admin-token";
const BASE = "https://relay.test";
let counter = 0;
const unique = (prefix: string) => `${prefix}${++counter}${Math.random().toString(36).slice(2, 6)}`;

async function adminCall(path: string, body?: unknown, token = ADMIN) {
  return SELF.fetch(`${BASE}${path}`, {
    method: body === undefined ? "GET" : "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

async function addNode(name: string, kind: "machine" | "guest", targets: string[] = []): Promise<string> {
  const res = await adminCall("/v1/admin/nodes", { name, kind, targets });
  expect(res.status).toBe(201);
  return ((await res.json()) as { token: string }).token;
}

interface Link {
  ws: WebSocket;
  next(type: string): Promise<any>;
  send(frame: unknown): void;
}

async function connect(token: string): Promise<Link> {
  const res = await SELF.fetch(`${BASE}/v1/link`, { headers: { Upgrade: "websocket", Authorization: `Bearer ${token}` } });
  expect(res.status).toBe(101);
  const ws = res.webSocket!;
  const frames: any[] = [];
  const waiters: Array<() => void> = [];
  ws.accept();
  ws.addEventListener("message", (event) => {
    frames.push(JSON.parse(event.data as string));
    waiters.splice(0).forEach((wake) => wake());
  });
  return {
    ws,
    send: (frame) => ws.send(JSON.stringify(frame)),
    async next(type) {
      for (;;) {
        const index = frames.findIndex((f) => f.type === type);
        if (index >= 0) return frames.splice(index, 1)[0];
        await new Promise<void>((resolve) => {
          waiters.push(resolve);
          setTimeout(resolve, 2000);
        });
        if (!frames.some((f) => f.type === type)) {
          const again = frames.findIndex((f) => f.type === type);
          if (again < 0 && ws.readyState !== WebSocket.OPEN) throw new Error(`socket closed waiting for ${type}`);
        }
      }
    },
  };
}

async function mcp(token: string, method: string, params: Record<string, unknown> = {}) {
  const res = await SELF.fetch(`${BASE}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json", Accept: "application/json, text/event-stream" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 1, method, params }),
  });
  return { status: res.status, body: res.status === 200 ? ((await res.json()) as any) : null };
}

async function tool(token: string, name: string, args: Record<string, unknown>) {
  const { body } = await mcp(token, "tools/call", { name, arguments: args });
  return body.result as { isError?: boolean; structuredContent?: any; content: Array<{ text: string }> };
}

describe("admin", () => {
  it("refuses a wrong admin token and never lists token hashes", async () => {
    expect((await adminCall("/v1/admin/nodes", undefined, "nope")).status).toBe(401);
    const name = unique("m");
    await addNode(name, "machine");
    const listed = await (await adminCall("/v1/admin/nodes")).text();
    expect(listed).toContain(name);
    expect(listed).not.toMatch(/hash/);
  });

  it("rejects reserved and reused names", async () => {
    expect((await adminCall("/v1/admin/nodes", { name: "ext", kind: "machine" })).status).toBe(400);
    const name = unique("m");
    await addNode(name, "machine");
    await adminCall(`/v1/admin/nodes/${name}/revoke`, {});
    expect((await adminCall("/v1/admin/nodes", { name, kind: "machine" })).status).toBe(400);
  });
});

describe("link", () => {
  it("refuses unknown tokens, guest tokens and browser origins", async () => {
    const bad = await SELF.fetch(`${BASE}/v1/link`, { headers: { Upgrade: "websocket", Authorization: "Bearer an_nope" } });
    expect(bad.status).toBe(401);
    const guest = await addNode(unique("g"), "guest", ["x.y"]);
    const asGuest = await SELF.fetch(`${BASE}/v1/link`, { headers: { Upgrade: "websocket", Authorization: `Bearer ${guest}` } });
    expect(asGuest.status).toBe(401);
    const origin = await SELF.fetch(`${BASE}/healthz`, { headers: { Origin: "https://evil.example" } });
    expect(origin.status).toBe(403);
  });

  it("queues for an offline machine, redelivers until acked, and stamps from", async () => {
    const a = unique("a");
    const b = unique("b");
    const aTok = await addNode(a, "machine");
    const bTok = await addNode(b, "machine");
    const la = await connect(aTok);
    await la.next("welcome");
    la.send({ type: "hello", agents: [{ name: "claude", kind: "claude", accept_external: false }] });

    la.send({ type: "send", ref: "r1", from: "claude", to: `${b}.codex`, text: "hola" });
    expect(await la.next("sent")).toMatchObject({ ref: "r1", status: "queued" });

    la.send({ type: "send", ref: "r2", from: "someone-else", to: `${b}.codex`, text: "spoof" });
    expect(await la.next("sent")).toMatchObject({ ref: "r2", status: "rejected" });

    const lb = await connect(bTok);
    await lb.next("welcome");
    const first = await lb.next("deliver");
    expect(first.envelope).toMatchObject({ from: `${a}.claude`, to: `${b}.codex`, text: "hola", origin: "relay" });
    lb.ws.close();

    const again = await connect(bTok);
    await again.next("welcome");
    const redelivered = await again.next("deliver");
    expect(redelivered.envelope.id).toBe(first.envelope.id);
    again.send({ type: "ack", seq: redelivered.seq });
    again.ws.close();

    const third = await connect(bTok);
    await third.next("welcome");
    third.send({ type: "hello", agents: [] });
    await new Promise((r) => setTimeout(r, 300));
    const pending = await Promise.race([third.next("deliver"), new Promise((r) => setTimeout(() => r(null), 500))]);
    expect(pending).toBeNull();
  });

  it("closes the socket and kills the token on revoke", async () => {
    const name = unique("r");
    const token = await addNode(name, "machine");
    const link = await connect(token);
    await link.next("welcome");
    const closed = new Promise((resolve) => link.ws.addEventListener("close", resolve));
    await adminCall(`/v1/admin/nodes/${name}/revoke`, {});
    await closed;
    const retry = await SELF.fetch(`${BASE}/v1/link`, { headers: { Upgrade: "websocket", Authorization: `Bearer ${token}` } });
    expect(retry.status).toBe(401);
  });
});

describe("guests", () => {
  it("reach only listed, opted-in agents and get the reply", async () => {
    const m = unique("m");
    const mTok = await addNode(m, "machine");
    const link = await connect(mTok);
    await link.next("welcome");
    link.send({
      type: "hello",
      agents: [
        { name: "open", kind: "claude", accept_external: true },
        { name: "closed", kind: "claude", accept_external: false },
      ],
    });
    await new Promise((r) => setTimeout(r, 200));
    const gTok = await addNode(unique("g"), "guest", [`${m}.open`, `${m}.closed`]);

    expect((await mcp(gTok, "initialize", { protocolVersion: "2025-06-18" })).body.result.protocolVersion).toBe("2025-06-18");
    const list = await tool(gTok, "list_agents", {});
    expect(list.structuredContent.agents.map((a: any) => a.name)).toEqual([`${m}.open`]);
    expect((await tool(gTok, "ask", { to: `${m}.closed`, text: "hi" })).isError).toBe(true);
    expect((await tool(gTok, "ask", { to: `${m}.other`, text: "hi" })).isError).toBe(true);

    const asking = tool(gTok, "ask", { to: `${m}.open`, text: "ping", wait_seconds: 5 });
    const delivered = await link.next("deliver");
    expect(delivered.envelope.origin).toBe("external");
    link.send({ type: "send", ref: "x", from: "open", to: delivered.envelope.from, text: "pong", reply_to: delivered.envelope.id });
    const answer = await asking;
    expect(answer.structuredContent).toMatchObject({ status: "answered", reply: "pong", from: `${m}.open` });
  });

  it("cannot be written to without a matching ask", async () => {
    const m = unique("m");
    const link = await connect(await addNode(m, "machine"));
    await link.next("welcome");
    link.send({ type: "hello", agents: [{ name: "a", kind: "claude", accept_external: true }] });
    const g = unique("g");
    await addNode(g, "guest", [`${m}.a`]);
    link.send({ type: "send", ref: "z", from: "a", to: `${g}.mcp`, text: "unsolicited", reply_to: "0".repeat(32) });
    expect(await link.next("sent")).toMatchObject({ status: "rejected" });
  });

  it("cannot read another guest's reply id", async () => {
    const m = unique("m");
    const link = await connect(await addNode(m, "machine"));
    await link.next("welcome");
    link.send({ type: "hello", agents: [{ name: "a", kind: "claude", accept_external: true }] });
    await new Promise((r) => setTimeout(r, 200));
    const alice = await addNode(unique("g"), "guest", [`${m}.a`]);
    const eve = await addNode(unique("g"), "guest", [`${m}.a`]);
    const sent = await tool(alice, "ask", { to: `${m}.a`, text: "secret", wait_seconds: 0 });
    const stolen = await tool(eve, "get_reply", { id: sent.structuredContent.id, wait_seconds: 0 });
    expect(stolen.isError).toBe(true);
  });
});
