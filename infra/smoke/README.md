# Smoke service

A throwaway service that proves the deploy path before the event. None of it is reused by the app.

It answers:

| Question | Where to look |
|---|---|
| Does CI build, push and deploy? | `/healthz` returns `ok <git sha>` after a push to main |
| Do server-sent events survive the Cloudflare tunnel and phone wifi? | The live counter on `/`. Background the tab, come back; it should resume within 2 s |
| Can the cluster reach PlanetScale and Elastic Cloud? | "Dependencies from the cluster" on `/` |
| Does Clerk sign-in work on the real hostname? | Sign in on `/`; "server sees" shows your Clerk user id |
| Do traces reach Dash0? | Dash0 → Tracing, service `two-feet` |

Run locally:

```bash
cd infra/smoke
go mod tidy                     # first time only; commit go.sum
set -a; source ../../.env; set +a
go run .
open http://localhost:8080
```

`/server migrate` exists and does nothing, so the chart's migrate job has a target if it's
turned on before the app replaces this service.
