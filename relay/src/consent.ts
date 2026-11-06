// The only HTML the relay serves. No script runs on it, it cannot be framed,
// and its form can only post back to this host.

// form-action also governs where the form's response may redirect, so the
// consent page lists the one client origin it is about to send the person to.
function headers(redirectOrigin = ""): Record<string, string> {
  return {
    ...HEADERS,
    "Content-Security-Policy": `default-src 'none'; style-src 'unsafe-inline'; form-action 'self' ${redirectOrigin}; frame-ancestors 'none'; base-uri 'none'`,
  };
}

const HEADERS = {
  "Content-Type": "text/html; charset=utf-8",
  "X-Frame-Options": "DENY",
  "Referrer-Policy": "no-referrer",
  "X-Content-Type-Options": "nosniff",
  "Cache-Control": "no-store",
};

const STYLE = `
:root{color-scheme:light dark;--fg:#1e1e1e;--muted:#6e6e6e;--line:#d0d0d0;--bg:#fafafa;--accent:#0b6e8e;--warn:#9a6700}
@media (prefers-color-scheme:dark){:root{--fg:#e6e6e6;--muted:#8c8c8c;--line:#3a3a3a;--bg:#141414;--accent:#2aa8cc;--warn:#e5a93b}}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 ui-monospace,SFMono-Regular,Menlo,monospace}
main{max-width:560px;margin:8vh auto;padding:0 16px}
h1{font-size:18px;margin:0 0 16px}
.box{border:1px solid var(--line);padding:16px;margin:16px 0}
.k{color:var(--muted)}
.host{color:var(--accent);font-weight:bold}
.warn{color:var(--warn)}
input[type=text]{font:inherit;font-size:20px;letter-spacing:.15em;padding:8px;width:12ch;border:1px solid var(--line);background:transparent;color:inherit}
button{font:inherit;padding:8px 16px;margin-right:8px;border:1px solid var(--line);background:transparent;color:inherit;cursor:pointer}
button.primary{border-color:var(--accent);color:var(--accent)}
`;

function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
}

function page(title: string, body: string, status = 200, redirectOrigin = ""): Response {
  const html = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>${esc(title)}</title><style>${STYLE}</style></head><body><main>${body}</main></body></html>`;
  return new Response(html, { status, headers: headers(redirectOrigin) });
}

export interface ConsentView {
  request: string;
  client: string;
  clientId: string;
  redirect: string;
  message?: string;
}

export function consentPage(v: ConsentView): Response {
  const redirect = new URL(v.redirect);
  const loopback = redirect.hostname === "localhost" || redirect.hostname === "127.0.0.1";
  return page("agentnet: sign in a guest", `
<h1>Let a client in as an agentnet guest</h1>
<div class="box">
  <div><span class="k">client</span> ${esc(v.client)}</div>
  <div><span class="k">id</span> ${esc(v.clientId)}</div>
  <div><span class="k">returns to</span> <span class="host">${esc(redirect.host)}</span>${esc(redirect.pathname)}</div>
  ${loopback ? `<p class="warn">This client returns to a program on this computer. Any local program can claim that address: continue only if you started this sign-in yourself.</p>` : ""}
</div>
<p>Enter the pairing code from <code>agentnet relay node pair &lt;guest&gt; --for ${esc(redirect.hostname)}</code>. The code decides which guest this client becomes and which agents it may ask.</p>
${v.message ? `<p class="warn">${esc(v.message)}</p>` : ""}
<form method="post" action="/oauth/authorize">
  <input type="hidden" name="request" value="${esc(v.request)}">
  <p><input type="text" name="code" autocomplete="one-time-code" autocapitalize="characters" spellcheck="false" maxlength="12" placeholder="XXXX-XXXX" autofocus></p>
  <p><button class="primary" type="submit" name="action" value="approve">Allow</button><button type="submit" name="action" value="deny">Deny</button></p>
</form>`, 200, redirect.origin);
}

export function errorPage(message: string, status: number): Response {
  return page("agentnet: sign-in failed", `<h1>Sign-in failed</h1><p>${esc(message)}</p><p class="k">Close this page and start again from the client.</p>`, status);
}
