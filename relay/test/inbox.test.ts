import { SELF } from "cloudflare:test";
import { describe, expect, it } from "vitest";

const ADMIN = "test-admin-token";
const BASE = "https://relay.test";
let counter = 0;
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
