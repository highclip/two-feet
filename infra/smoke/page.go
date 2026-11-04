package main

const pageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Two Feet smoke test</title>
<style>
  :root { --board:#F3F5F4; --card:#fff; --line:#DDE2E0; --ink:#16191C; --soft:#5E666D; --ok:#2A7649; --bad:#B23824; }
  * { box-sizing: border-box }
  body { margin:0; background:var(--board); color:var(--ink); font:15px/1.45 system-ui, sans-serif;
         padding: 20px max(16px, env(safe-area-inset-left)) 40px max(16px, env(safe-area-inset-right)) }
  main { max-width: 520px; margin: 0 auto; display:flex; flex-direction:column; gap:14px }
  h1 { margin:0; font-size:28px } .soft { color:var(--soft) }
  section { background:var(--card); border:1px solid var(--line); border-radius:14px; padding:14px 16px }
  h2 { margin:0 0 8px; font-size:12px; letter-spacing:.1em; text-transform:uppercase; color:var(--soft) }
  .big { font-size:44px; font-weight:700; font-variant-numeric: tabular-nums; line-height:1 }
  dl { margin:0; display:grid; grid-template-columns:auto 1fr; gap:4px 12px } dt { color:var(--soft) }
  dd { margin:0; overflow-wrap:anywhere } .ok { color:var(--ok) } .bad { color:var(--bad) }
  button { min-height:44px; border:1px solid var(--ink); border-radius:12px; background:transparent; padding:0 14px; font:inherit; font-weight:600 }
</style>
</head>
<body>
<main>
  <div>
    <h1>Two Feet smoke test</h1>
    <div class="soft">version <b>{{.Version}}</b></div>
  </div>

  <section>
    <h2>Live (server-sent events)</h2>
    <div class="big" id="n">–</div>
    <dl>
      <dt>status</dt><dd id="status">connecting</dd>
      <dt>reconnects</dt><dd id="reconnects">0</dd>
      <dt>longest gap</dt><dd id="gap">–</dd>
    </dl>
    <p class="soft">Background this tab for a minute, then come back. The count should resume within 2 seconds.</p>
  </section>

  <section>
    <h2>Dependencies from the cluster</h2>
    <dl id="checks"><dt>loading</dt><dd></dd></dl>
  </section>

  <section>
    <h2>Clerk sign-in</h2>
    <div id="signin"></div>
    <dl><dt>server sees</dt><dd id="whoami">signed out</dd></dl>
    <button type="button" id="signout" hidden>Sign out</button>
  </section>
</main>

<script>
  // Live counter. Tracks reconnects and the longest gap between ticks.
  let reconnects = -1, last = 0, gap = 0;
  const es = new EventSource('/sse');
  es.onopen = () => { reconnects++; document.getElementById('reconnects').textContent = reconnects;
                      document.getElementById('status').textContent = 'connected'; document.getElementById('status').className = 'ok'; };
  es.onerror = () => { document.getElementById('status').textContent = 'reconnecting…'; document.getElementById('status').className = 'bad'; };
  es.addEventListener('tick', (e) => {
    const d = JSON.parse(e.data), now = Date.now();
    if (last) { gap = Math.max(gap, now - last); document.getElementById('gap').textContent = (gap / 1000).toFixed(1) + ' s'; }
    last = now;
    document.getElementById('n').textContent = d.n;
  });

  // Dependency checks.
  fetch('/api/checks').then(r => r.json()).then(c => {
    const dl = document.getElementById('checks'); dl.textContent = '';
    for (const [k, v] of Object.entries(c)) {
      const dt = document.createElement('dt'), dd = document.createElement('dd');
      dt.textContent = k; dd.textContent = v;
      dd.className = v.startsWith('ok') || v === 'configured' ? 'ok' : (v.startsWith('error') ? 'bad' : '');
      dl.append(dt, dd);
    }
  });
</script>
{{if .ClerkScript}}
<script async crossorigin="anonymous" data-clerk-publishable-key="{{.ClerkPK}}" src="{{.ClerkScript}}"></script>
<script>
  window.addEventListener('load', async () => {
    await window.Clerk.load();
    const who = document.getElementById('whoami'), out = document.getElementById('signout');
    if (!window.Clerk.user) { window.Clerk.mountSignIn(document.getElementById('signin')); return; }
    out.hidden = false; out.onclick = () => window.Clerk.signOut().then(() => location.reload());
    const token = await window.Clerk.session.getToken();
    const r = await fetch('/api/whoami', { headers: { Authorization: 'Bearer ' + token } });
    who.textContent = r.ok ? JSON.stringify(await r.json()) : 'HTTP ' + r.status;
    who.className = r.ok ? 'ok' : 'bad';
  });
</script>
{{else}}
<script>document.getElementById('signin').textContent = 'CLERK_PUBLISHABLE_KEY is not set.';</script>
{{end}}
</body>
</html>
`
