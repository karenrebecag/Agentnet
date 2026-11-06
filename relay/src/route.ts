import {
  type Env,
  type Envelope,
  type NodeRecord,
  type Origin,
  GUEST_AGENT,
  MAX_TEXT_GUEST,
  MAX_TEXT_MACHINE,
  now,
  randomHex,
  splitAddress,
} from "./shared";

export type SendResult =
  | { status: "delivered" | "queued"; id: string }
  | { status: "awaiting_approval"; id: string; contact: string }
  | { status: "rejected"; error: string };

export interface SendRequest {
  sender: NodeRecord;
  fromAgent: string;
  to: string;
  text: string;
  replyTo?: string;
  // Lets the caller record an ask under this id before the message can be answered.
  id?: string;
}

function reject(error: string): SendResult {
  return { status: "rejected", error };
}

/**
 * Every message, from a machine or a guest, passes this one gate. The sender
 * comes from the authenticated token, never from the client, and the same
 * "not allowed" answer covers missing and forbidden targets.
 */
export async function route(env: Env, req: SendRequest): Promise<SendResult> {
  const { sender } = req;
  const text = req.text.trim();
  const max = sender.kind === "guest" ? MAX_TEXT_GUEST : MAX_TEXT_MACHINE;
  if (text === "") return reject("empty message");
  if (new TextEncoder().encode(text).length > max) return reject(`message larger than ${max} bytes`);

  const dest = splitAddress(req.to);
  if (!dest) return reject("invalid address, expected <node>.<agent>");
  const target = await env.DIRECTORY.getByName("directory").get(dest.node);
  if (!target || target.revoked) return reject("recipient not allowed");

  const from = `${sender.name}.${req.fromAgent}`;
  const origin: Origin = sender.kind === "guest" ? "external" : "relay";
  const box = env.MAILBOX.getByName(dest.node);

  if (target.kind === "guest") {
    // A guest only ever receives answers to its own asks, from the agent it asked.
    if (sender.kind !== "machine" || dest.agent !== GUEST_AGENT) return reject("recipient not allowed");
    // Without reply_to this is an unsolicited message: only for a guest that
    // opted in, and only from an agent the guest itself may ask.
    if (!req.replyTo && !(target.inbox && (await mayWriteFirst(env, target, sender, req.fromAgent, from)))) return reject("recipient not allowed");
  } else if (sender.kind === "guest" && isOpen(sender)) {
    // An open guest names any agent online; the person allows each agent
    // session the first time, and the first message waits for that answer.
    const announced = target.agents.find((a) => a.name === dest.agent);
    if (!announced) return reject("that agent is not online");
    // Without a session id an approval would bind to the name alone.
    if (!announced.session) return reject("that agent's machine runs an older agentnet; update it to talk to it");
    const session = announced.session;
    const envelope: Envelope = { id: req.id ?? randomHex(16), ts: now(), from, to: req.to, text, origin };
    const directory = env.DIRECTORY.getByName("directory");
    if (!(await directory.contactAllowed(sender.name, req.to, session))) {
      const held = await directory.requestContact(sender.name, req.to, session, JSON.stringify(envelope));
      if ("error" in held) return reject(held.error);
      return { status: "awaiting_approval", id: envelope.id, contact: held.id };
    }
    return box.enqueue(envelope);
  } else if (sender.kind === "guest") {
    const announced = target.agents.find((a) => a.name === dest.agent);
    if (!sender.targets.includes(req.to) || !announced?.accept_external) return reject("recipient not allowed");
  }

  const envelope: Envelope = { id: req.id ?? randomHex(16), ts: now(), from, to: req.to, text, origin };
  if (req.replyTo) envelope.reply_to = req.replyTo;
  if (target.kind === "guest") return req.replyTo ? box.enqueueReply(envelope) : box.enqueueInbox(envelope);
  return box.enqueue(envelope);
}

/** A guest created without targets reaches any agent the person allows. */
export function isOpen(guest: NodeRecord): boolean {
  return guest.kind === "guest" && guest.targets.length === 0;
}

// An agent writes first to a guest only if the guest could ask it: a listed
// target, or, for an open guest, an agent session the person allowed.
async function mayWriteFirst(env: Env, guest: NodeRecord, sender: NodeRecord, agent: string, from: string): Promise<boolean> {
  if (!isOpen(guest)) return guest.targets.includes(from);
  const session = sender.agents.find((a) => a.name === agent)?.session;
  return session ? env.DIRECTORY.getByName("directory").contactAllowed(guest.name, from, session) : false;
}
