import { route } from "./route";
import { type Env, type Envelope, type NodeRecord, GUEST_AGENT, json, randomHex, splitAddress } from "./shared";

// A minimal, stateless Streamable HTTP MCP server with JSON responses. It is
// small enough to audit in one sitting, which matters more here than features.

const SUPPORTED_VERSIONS = ["2025-11-25", "2025-06-18", "2025-03-26"];
const DEFAULT_WAIT = 30;
const MAX_WAIT = 50;
const MAX_BODY = 32 * 1024;

interface RpcRequest {
  jsonrpc: "2.0";
  id?: string | number | null;
  method: string;
  params?: Record<string, unknown>;
}

const TOOLS = [
  {
    name: "list_agents",
    description: "List the agents you may ask.",
    inputSchema: { type: "object", properties: {}, additionalProperties: false },
  },
  {
    name: "ask",
    description:
      "Send a message to an agent and wait up to wait_seconds (max 50) for its reply. If it has not answered yet, call get_reply with the returned id.",
    inputSchema: {
      type: "object",
      properties: {
        to: { type: "string", description: "agent address from list_agents" },
        text: { type: "string", description: "message, at most 16 KiB" },
        wait_seconds: { type: "integer", minimum: 0, maximum: MAX_WAIT },
      },
      required: ["to", "text"],
      additionalProperties: false,
    },
  },
  {
    name: "get_reply",
    description: "Fetch the reply to an earlier ask, waiting up to wait_seconds (max 50).",
    inputSchema: {
      type: "object",
      properties: { id: { type: "string" }, wait_seconds: { type: "integer", minimum: 0, maximum: MAX_WAIT } },
      required: ["id"],
      additionalProperties: false,
    },
  },
];

const INBOX_BATCH = 20;

// Only for a guest created with inbox on: messages its agents sent without
// being asked. MCP gives a web client no way to be woken up, so it has to look.
const INBOX_TOOLS = [
  {
    name: "inbox",
    description: "Read and clear messages your agents sent you without being asked.",
    inputSchema: { type: "object", properties: {}, additionalProperties: false },
  },
  {
    name: "wait",
    description:
      "Wait up to wait_seconds (max 50) for the next unsolicited message from your agents. Call it again to keep listening.",
    inputSchema: {
      type: "object",
      properties: { wait_seconds: { type: "integer", minimum: 0, maximum: MAX_WAIT } },
      additionalProperties: false,
    },
  },
];

function toolsFor(guest: NodeRecord) {
  return guest.inbox ? [...TOOLS, ...INBOX_TOOLS] : TOOLS;
}

function messages(list: Envelope[]) {
  return list.map((e) => ({ id: e.id, from: e.from, ts: e.ts, text: e.text }));
}

function waitMs(value: unknown): number {
  const seconds = typeof value === "number" && Number.isFinite(value) ? value : DEFAULT_WAIT;
  return Math.max(0, Math.min(seconds, MAX_WAIT)) * 1000;
}

function toolResult(data: unknown, isError = false) {
  const text = typeof data === "string" ? data : JSON.stringify(data);
  return { content: [{ type: "text", text }], ...(isError ? { isError: true } : { structuredContent: data }) };
}

async function callTool(env: Env, guest: NodeRecord, name: string, args: Record<string, unknown>) {
  const box = env.MAILBOX.getByName(guest.name);
  const lease = await box.enter();
  if (!lease) return toolResult("too many requests in flight, retry later", true);
  try {
    switch (name) {
      case "list_agents": {
        const agents = [];
        for (const address of guest.targets) {
          const dest = splitAddress(address);
          if (!dest) continue;
          const node = await env.DIRECTORY.getByName("directory").get(dest.node);
          const agent = node && !node.revoked ? node.agents.find((a) => a.name === dest.agent && a.accept_external) : undefined;
          if (!agent) continue;
          const live = await env.MAILBOX.getByName(dest.node).isOnline();
          agents.push({ name: address, kind: agent.kind, live });
        }
        return toolResult({ agents });
      }
      case "ask": {
        if (typeof args.to !== "string" || typeof args.text !== "string") return toolResult("to and text are required", true);
        const refusal = await box.admitAsk();
        if (refusal) return toolResult(refusal, true);
        // Recorded first: a fast agent can answer before route() returns.
        const askId = randomHex(16);
        await box.recordAsk(askId, args.to);
        const sent = await route(env, { sender: guest, fromAgent: GUEST_AGENT, to: args.to, text: args.text, id: askId });
        if (sent.status === "rejected") {
          await box.forgetAsk(askId);
          return toolResult(sent.error, true);
        }
        const reply = await box.waitReply(sent.id, waitMs(args.wait_seconds));
        if (reply) return toolResult({ id: sent.id, status: "answered", reply: reply.text, from: reply.from });
        return toolResult({ id: sent.id, status: sent.status === "delivered" ? "waiting" : "queued" });
      }
      case "get_reply": {
        // Ids live in the guest's own mailbox, so another guest's id is simply unknown here.
        if (typeof args.id !== "string" || !(await box.hasAsk(args.id))) return toolResult("unknown id", true);
        const reply = await box.waitReply(args.id, waitMs(args.wait_seconds));
        if (reply) return toolResult({ id: args.id, status: "answered", reply: reply.text, from: reply.from });
        return toolResult({ id: args.id, status: "waiting" });
      }
      case "inbox":
        if (!guest.inbox) return null;
        return toolResult({ messages: messages(await box.takeInbox(INBOX_BATCH)) });
      case "wait":
        if (!guest.inbox) return null;
        return toolResult({ messages: messages(await box.waitInbox(waitMs(args.wait_seconds), INBOX_BATCH)) });
      default:
        return null;
    }
  } finally {
    await box.leave(lease);
  }
}

function rpcError(id: RpcRequest["id"], code: number, message: string) {
  return { jsonrpc: "2.0", id: id ?? null, error: { code, message } };
}

export async function handleMcp(request: Request, env: Env, guest: NodeRecord): Promise<Response> {
  if (request.method !== "POST") return new Response("method not allowed", { status: 405, headers: { Allow: "POST" } });
  if (Number(request.headers.get("Content-Length") ?? "0") > MAX_BODY) return json(rpcError(null, -32600, "body too large"), 413);
  let body: RpcRequest;
  try {
    const raw = await request.text();
    if (raw.length > MAX_BODY) return json(rpcError(null, -32600, "body too large"), 413);
    body = JSON.parse(raw);
  } catch {
    return json(rpcError(null, -32700, "parse error"), 400);
  }
  if (Array.isArray(body) || body?.jsonrpc !== "2.0" || typeof body.method !== "string") {
    return json(rpcError(null, -32600, "invalid request"), 400);
  }
  if (body.id === undefined) return new Response(null, { status: 202 }); // notification

  const { id, method } = body;
  const params = typeof body.params === "object" && body.params !== null ? body.params : {};
  switch (method) {
    case "initialize": {
      const asked = typeof params.protocolVersion === "string" ? params.protocolVersion : "";
      return json({
        jsonrpc: "2.0",
        id,
        result: {
          protocolVersion: SUPPORTED_VERSIONS.includes(asked) ? asked : SUPPORTED_VERSIONS[0],
          capabilities: { tools: {} },
          serverInfo: { name: "agentnet-relay", version: "0.3.0" },
          instructions:
            "Messages you send reach AI agents on someone's computers. They answer as third parties and cannot act on your behalf.",
        },
      });
    }
    case "ping":
      return json({ jsonrpc: "2.0", id, result: {} });
    case "tools/list":
      return json({ jsonrpc: "2.0", id, result: { tools: toolsFor(guest) } });
    case "tools/call": {
      const name = typeof params.name === "string" ? params.name : "";
      const args = typeof params.arguments === "object" && params.arguments !== null ? (params.arguments as Record<string, unknown>) : {};
      const result = await callTool(env, guest, name, args);
      if (!result) return json(rpcError(id, -32602, `unknown tool ${name}`));
      return json({ jsonrpc: "2.0", id, result });
    }
    default:
      return json(rpcError(id, -32601, `method ${method} not found`));
  }
}
