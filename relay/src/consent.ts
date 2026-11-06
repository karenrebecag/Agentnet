import type { Signin } from "./directory";

// The only HTML the relay serves. Its one script is the inline one below,
// allowed by hash, and the pages work without it. They cannot be framed, and
// their form can only post back to this host.
//
// The layout follows Arc's sign-in card and OTP input (uiarc.dev), redrawn in
// plain HTML and CSS: one slot per character of the pairing code.

const CODE_LENGTH = 8;

// Moves focus between slots and spreads a pasted code across them. Without it
// each slot is still an ordinary field and Tab moves between them.
const SCRIPT = `(()=>{const s=[...document.querySelectorAll(".slot input")];if(!s.length)return;
const clean=v=>v.toUpperCase().replace(/[^0-9A-Z]/g,"");
const fill=(i,v)=>{for(let k=0;k<v.length&&i+k<s.length;k++)s[i+k].value=v[k];s[Math.min(i+v.length,s.length-1)].focus();};
s.forEach((el,i)=>{
el.addEventListener("input",()=>{const v=clean(el.value);if(v.length>1){el.value="";fill(i,v);return;}el.value=v;if(v&&i<s.length-1)s[i+1].focus();});
el.addEventListener("paste",e=>{e.preventDefault();fill(i,clean(e.clipboardData.getData("text")||""));});
el.addEventListener("keydown",e=>{
if(e.key==="Backspace"&&!el.value&&i>0){e.preventDefault();s[i-1].value="";s[i-1].focus();}
if(e.key==="ArrowLeft"&&i>0){e.preventDefault();s[i-1].focus();}
if(e.key==="ArrowRight"&&i<s.length-1){e.preventDefault();s[i+1].focus();}});});})();`;

let scriptHash: Promise<string> | undefined;
function hashOfScript(): Promise<string> {
  scriptHash ??= crypto.subtle.digest("SHA-256", new TextEncoder().encode(SCRIPT)).then((d) => btoa(String.fromCharCode(...new Uint8Array(d))));
  return scriptHash;
}

const BASE_HEADERS = {
  "Content-Type": "text/html; charset=utf-8",
  "X-Frame-Options": "DENY",
  // Not no-referrer: under it the browser posts the consent form with
  // Origin: null, and the same-origin check refuses it. same-origin still
  // sends nothing to the client the page redirects to.
  "Referrer-Policy": "same-origin",
  "X-Content-Type-Options": "nosniff",
  "Cache-Control": "no-store",
};

// form-action also governs where the form's response may redirect, so the
// consent page lists the one client origin it is about to send the person to.
function headers(redirectOrigin: string, script: string): Record<string, string> {
  const scriptSrc = script ? `'sha256-${script}'` : "'none'";
  return {
    ...BASE_HEADERS,
    "Content-Security-Policy": `default-src 'none'; style-src 'unsafe-inline'; script-src ${scriptSrc}; form-action 'self' ${redirectOrigin}; frame-ancestors 'none'; base-uri 'none'`,
  };
}

// Arc's semantic tokens, with values for light and dark.
const STYLE = `
:root{color-scheme:light dark;
--background:#fafafa;--surface:#fff;--surface-muted:#f4f4f5;--foreground:#18181b;--text-secondary:#52525b;--text-muted:#71717a;
--border:#e4e4e7;--border-strong:#d4d4d8;--accent:#2563eb;--accent-strong:#1d4ed8;--accent-subtle:#eff4ff;--danger:#dc2626;--warning:#b45309;
--radius-surface:16px;--radius-control:10px;--ease:cubic-bezier(.2,0,0,1)}
@media (prefers-color-scheme:dark){:root{
--background:#0b0b0c;--surface:#141416;--surface-muted:#1c1c1f;--foreground:#f4f4f5;--text-secondary:#a1a1aa;--text-muted:#8b8b93;
--border:#27272a;--border-strong:#3f3f46;--accent:#3b82f6;--accent-strong:#60a5fa;--accent-subtle:#172036;--danger:#f87171;--warning:#fbbf24}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px 16px;background:var(--background);color:var(--foreground);
font:15px/1.5 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;-webkit-font-smoothing:antialiased}
.card{width:min(100%,432px);border:1px solid var(--border);border-radius:var(--radius-surface);background:var(--surface);padding:32px}
h1{margin:0 0 8px;font-size:26px;font-weight:400;letter-spacing:-.01em;line-height:1.2;text-wrap:balance}
.lead{margin:0 0 24px;color:var(--text-secondary);font-size:14px;text-wrap:pretty}
.details{margin:0 0 24px;border-top:1px solid var(--border)}
.details div{display:flex;justify-content:space-between;gap:16px;border-bottom:1px solid var(--border);padding:12px 0;font-size:14px}
.details dt{color:var(--text-secondary)}.details dd{margin:0;text-align:right;overflow-wrap:anywhere;font-weight:500}
.host{color:var(--accent)}
.note{margin:0 0 24px;border:1px solid var(--border);border-radius:var(--radius-control);background:var(--surface-muted);padding:12px;font-size:13px;color:var(--text-secondary)}
.note.warn{border-color:color-mix(in srgb,var(--warning) 40%,var(--border));color:var(--warning)}
.note.error{border-color:color-mix(in srgb,var(--danger) 40%,var(--border));color:var(--danger)}
label.field{display:block;margin-bottom:8px;font-size:14px;font-weight:500}
.slots{display:flex;align-items:center;gap:8px;width:fit-content;max-width:100%;margin-bottom:8px}
.slot{display:grid;width:42px;height:48px;flex:0 1 42px;min-width:0}
.slot input{width:100%;height:100%;min-width:0;border:1px solid var(--border);border-radius:var(--radius-control);background:var(--surface);
color:var(--foreground);caret-color:var(--accent);font:inherit;font-size:18px;font-variant-numeric:tabular-nums;text-align:center;text-transform:uppercase;outline:none;
transition:border-color .15s var(--ease),background-color .15s var(--ease),box-shadow .15s var(--ease)}
.slot input:not(:placeholder-shown){border-color:var(--accent);background:var(--accent-subtle)}
.slot input:focus{border-color:var(--accent-strong);box-shadow:0 0 0 1px var(--accent-strong)}
.slots[aria-invalid=true] .slot input{border-color:var(--danger)}
.dash{color:var(--text-muted);flex:none}
.hint{margin:0 0 24px;color:var(--text-muted);font-size:12px}
.actions{display:grid;gap:8px}
button{height:44px;border-radius:var(--radius-control);font:inherit;font-size:15px;font-weight:500;cursor:pointer;transition:background-color .15s var(--ease),border-color .15s var(--ease)}
button.primary{border:1px solid var(--accent);background:var(--accent);color:#fff}
button.secondary{border:1px solid var(--border);background:transparent;color:var(--foreground)}
@media (hover:hover){button.primary:hover{background:var(--accent-strong)}button.secondary:hover{background:var(--surface-muted)}}
code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.92em}
.waiting{display:flex;align-items:center;gap:12px;margin:0 0 24px;color:var(--text-secondary);font-size:14px}
.pulse{width:10px;height:10px;flex:none;border-radius:50%;background:var(--accent);animation:pulse 1.4s var(--ease) infinite}
@keyframes pulse{0%,100%{opacity:.35;transform:scale(.8)}50%{opacity:1;transform:scale(1)}}
.id{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;letter-spacing:.06em}
@media (max-width:380px){.card{padding:24px}.slots{gap:5px}.slot{height:44px}}
@media (prefers-reduced-motion:reduce){*{transition:none!important;animation:none!important}}
@media (forced-colors:active){.slot input:focus{outline:2px solid CanvasText;outline-offset:2px}}
`;

function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
}

function page(title: string, body: string, opts: { status?: number; redirectOrigin?: string; head?: string; script?: string } = {}): Response {
  const script = opts.script ? `<script>${SCRIPT}</script>` : "";
  const html = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">${opts.head ?? ""}
<title>${esc(title)}</title><style>${STYLE}</style></head><body><main class="card">${body}</main>${script}</body></html>`;
  return new Response(html, { status: opts.status ?? 200, headers: headers(opts.redirectOrigin ?? "", opts.script ?? "") });
}

export interface ConsentView {
  request: string;
  client: string;
  clientId: string;
  redirect: string;
  message?: string;
}

function slots(): string {
  const one = (i: number) =>
    `<span class="slot"><input name="c${i}" placeholder=" " inputmode="text" autocapitalize="characters" autocomplete="${i === 0 ? "one-time-code" : "off"}" spellcheck="false" aria-label="Pairing code, character ${i + 1} of ${CODE_LENGTH}"${i === 0 ? " autofocus" : ""}></span>`;
  const half = CODE_LENGTH / 2;
  return [...Array(half).keys()].map(one).join("") + `<span class="dash" aria-hidden="true">-</span>` + [...Array(half).keys()].map((i) => one(i + half)).join("");
}

export async function consentPage(v: ConsentView): Promise<Response> {
  const redirect = new URL(v.redirect);
  const loopback = redirect.hostname === "localhost" || redirect.hostname === "127.0.0.1";
  return page("Connect to agentnet", `
<h1>Connect to agentnet</h1>
<p class="lead">Type the pairing code from <code>agentnet watch</code>. It decides which guest this client becomes and which agents it can talk to.</p>
<dl class="details">
  <div><dt>Client</dt><dd>${esc(v.client)}</dd></div>
  <div><dt>Returns to</dt><dd class="host">${esc(redirect.host)}</dd></div>
</dl>
${loopback ? `<p class="note warn">This client returns to a program on this computer. Any local program can claim that address, so continue only if you started this sign-in.</p>` : ""}
${v.message ? `<p class="note error" role="alert">${esc(v.message)}</p>` : ""}
<form method="post" action="/oauth/authorize">
  <input type="hidden" name="request" value="${esc(v.request)}">
  <label class="field" id="code-label">Pairing code</label>
  <div class="slots" role="group" aria-labelledby="code-label"${v.message ? ` aria-invalid="true"` : ""}>${slots()}</div>
  <p class="hint">Letters and digits. Pasting the whole code works.</p>
  <div class="actions">
    <button class="primary" type="submit" name="action" value="approve">Continue</button>
    <button class="secondary" type="submit" name="action" value="deny">Cancel</button>
  </div>
</form>`, { redirectOrigin: redirect.origin, script: await hashOfScript() });
}

/** The code the person typed, from the slots or from a single field. */
export function typedCode(form: FormData): string {
  const single = form.get("code");
  if (typeof single === "string" && single !== "") return single.slice(0, 32);
  let code = "";
  for (let i = 0; i < CODE_LENGTH; i++) code += String(form.get(`c${i}`) ?? "").slice(0, 2);
  return code;
}

// Reloading needs no script: a meta refresh asks again every few seconds.
export function waitingPage(poll: string, s: Signin): Response {
  return page("Approve on your computer", `
<h1>Approve on your computer</h1>
<p class="waiting"><span class="pulse" aria-hidden="true"></span><span>agentnet watch is asking you to let this client in.</span></p>
<dl class="details">
  <div><dt>Sign-in</dt><dd class="id">${esc(s.id)}</dd></div>
  <div><dt>Returns to</dt><dd class="host">${esc(s.host)}</dd></div>
  <div><dt>As guest</dt><dd>${esc(s.guest)}</dd></div>
</dl>
<p class="note">Approve only if watch shows the same sign-in, <span class="id">${esc(s.id)}</span>. Without watch open: <code>agentnet relay signins approve ${esc(s.id)}</code>. This page moves on by itself.</p>`,
    { head: `<meta http-equiv="refresh" content="3;url=/oauth/authorize?wait=${encodeURIComponent(poll)}">` });
}

export function errorPage(message: string, status: number): Response {
  return page("Sign-in failed", `<h1>Sign-in failed</h1><p class="note error">${esc(message)}</p><p class="lead">Close this page and start again from the client.</p>`, { status });
}
