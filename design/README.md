# Design

- `tokens.css` is the source of truth for color, type and shape. The app's views import it.
- `reference/` holds the four approved mockups as plain HTML plus screenshots:

| Screen | Size | Slices |
|---|---|---|
| `spot-page` | 390x844 | S2, S3, S4 |
| `matches` | 390x844 | S5, S6, S7 |
| `after-the-space` | 390x844 | delight.after ("Swap handles") |
| `wall` | 1920x1080 | S9 |

The people, handles and topics in the mockups are sample data.

## Rules the design critic checks

From `spec/spec.yaml` `design` and `delight`:

- Flat and digital. Hairline borders, 12-16px corners, no tilt, tape, paper texture or handwriting.
- A post's kind has one color everywhere: yellow = Stuck on, blue = Curious, green = Can help.
- Marker red (`--action`) is only for Join and Start.
- Text on a kind color or tint is always `--ink`. `--ink-soft` is for the board and white cards only.
- 44px tap targets, nothing scrolls sideways at 390px, contrast at least 4.5:1.
- Warmth comes from people: names, initials and each host's own words, shown in italics.

To compare a built screen, screenshot it at the same size and look at both side by side.
The editable mockups live on the Two Feet design canvas in Claude.
