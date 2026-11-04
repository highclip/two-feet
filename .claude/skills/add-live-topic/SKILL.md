---
name: add-live-topic
description: Push a live update to open pages over SSE. Use whenever a slice needs a count, card or list to update without a reload.
---

# Adding a live update

Read `spec.live` first. There is one SSE hub in `app/internal/live`, built by S4. Never open a
second stream or add polling.

## Pattern

1. **Publish after the write commits**, never inside the transaction:

```go
if err := tx.Commit(ctx); err != nil { return err }
h.live.Publish(live.SpaceTopic(spaceID), live.Fragment{
    Target: "#space-" + space.Slug + "-seats",   // element id the client swaps
    HTML:   views.Seats(space, signups),          // a templ component, rendered server-side
})
```

2. **Subscribe on the page** with the htmx SSE extension. The topic list is set by the server when
   it renders the page, never taken from the client's query string as-is:

```html
<div hx-ext="sse" sse-connect="/e/cnh/live?t={{ signed topic list }}">
  <div id="space-abc123-seats" sse-swap="space-abc123-seats">...</div>
</div>
```

3. **Use the right topic**: `event`, `spot:<id>`, `space:<id>`, or `person:<id>` (only ever the
   current person's own id).

## Test it

Every live update gets a test that opens two clients, makes the change in one, and checks the other
receives the fragment within 2 seconds. Put it next to the slice's other acceptance tests.

## Never

- Send another person's location, email or full post text over SSE.
- Push match results live; they show as "N new matches" instead (`spec.live.what_does_not`).
