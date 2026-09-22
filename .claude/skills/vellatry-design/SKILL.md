---
name: vellatry-design
description: Vellatry's design language for everything people see - the dashboard (web/app, web/components), the landing page, sign-in, the setup wizard, report and email templates, and product copy. Use it before designing, restyling or writing copy for any page or component, and when building a mock for review.
---

# Vellatry design

Vellatry sells one thing: numbers a marketing team can defend to their CMO. The design
has to look like that promise. It should feel like a well-set report: calm, precise,
evidence first. It should not look like an AI startup: no gradients everywhere, no
glassmorphism, no robot or sparkle imagery, no dark-mode-by-default neon.

## Rules that are not negotiable

1. **No UI framework, no component library, no chart library, no icon library**
   (decision 57). One stylesheet, `web/app/globals.css`, plus a CSS module for a page
   that needs its own layout. Charts are inline SVG (`Chart` in `components/ui.tsx`).
   Icons, when unavoidable, are inline SVG with `aria-hidden`.
2. **Never invent evidence.** Marketing copy and mocks show no customer names or
   logos, no testimonials, no statistics, and no pricing, certifications or
   integrations that the code does not have. Product illustrations use "Your brand" and
   "Competitor A/B/C", never real or made-up brand names, and carry the caption
   "Illustration" whenever they show figures.
3. **Describe what is shipped.** Every feature sentence must be true of the code today.
   Check the README's Status section before claiming a capability.
4. **Numbers in the product come from the api.** A page formats them with
   `lib/format.ts` (en-AU), shows `-` for missing, and never computes a figure the
   backend did not.

## Tokens

Defined once on `:root` in `globals.css`. Use the variables, never raw hex, in new CSS.

| Token | Value | Use |
| --- | --- | --- |
| `--ink` | `#101828` | Text, primary buttons, the brand |
| `--muted` | `#475467` | Secondary text |
| `--faint` | `#98a2b3` | Captions, disabled, excluded lookalikes |
| `--line` | `#eaecf0` | Hairlines, borders, table rules |
| `--bg` | `#f6f7f9` | App background, alternate sections |
| `--panel` | `#ffffff` | Cards, inputs |
| `--accent` | `#1d4ed8` | Links, focus, data lines, brand mentions |
| `--good` / `--bad` / `--warn` | green / red / amber | Meaning only, never decoration |
| `--radius` | `8px` | Cards; controls use 6px, pills 999px |

Mentions have fixed colours everywhere they appear (tester, answers, reports):
brand `#dbeafe` on `--accent`, competitor `#fef0c7` on `--warn`, excluded lookalike
struck through in `--faint`.

## Type

- UI and body: the system sans stack on `body` (15px/1.55). Numbers in tables and tiles
  use `font-variant-numeric: tabular-nums`.
- Marketing display (landing page, sign-in panel, and later the report cover):
  Instrument Serif 400 via `next/font` (`web/lib/fonts.ts`), large, line-height 1.05,
  letter-spacing -0.01em. Never in the app's UI, never for numbers, never for body
  text.
- Headings are sentence case. Page titles 22px, card titles 16px, marketing H1 with
  `clamp(40px, 6vw, 72px)`.

## Layout and spacing

- Spacing steps: 4, 8, 12, 16, 24, 32, 48, 64, 96 px.
- The app: 232px sidebar, content max 1180px. Marketing: content max 1120px, sections
  96px apart on desktop and 64px on mobile.
- Every page works at 375px wide with 16px side gutters and no horizontal scroll.
  Grids collapse to one column below 640px.

## Components (reuse before writing new ones)

- `components/ui.tsx`: `Card`, `Tile`, `MetricTile`, `Table`, `Chart`, `Pill`, `Empty`,
  `ErrorNote`, `Loading`, `RangePicker`.
- `components/brand.tsx`: `ListEditor` (chips), `Tester` (highlighted mentions),
  `Recognition`, `Differentiators`, `CompetitorsEditor`, `TopicPicker`.
- `components/GoogleConnect.tsx`, `components/Shell.tsx` (layout and gate),
  `components/Nav.tsx`.
- Buttons: `button.primary` (ink) for the one main action in a view; plain `button`
  (outlined) for everything else. At most one primary per card.
- Clerk's screens are themed from the same tokens in `lib/clerkAppearance.ts`. Change
  both together.

## Copy

- Plain, specific Australian English: organisation, colour, optimise, "Search
  Console", "AI Overviews". Say what happens: "Vellatry reads your home page to
  suggest topics", not "AI-powered insights".
- No exclamation marks, no emojis, no hype words (revolutionary, supercharge, unlock,
  seamless, 10x, game-changing).
- Missing data is stated with its fix ("Search Console isn't connected. Connect it.").
  An empty state says what will fill it.
- The model is never the hero. Code counts; a model may word a sentence.

## Accessibility

- WCAG AA contrast. `--faint` is for captions only, never for text someone must read.
- Visible focus: 2px `--accent` outline, 2px offset. Never remove outlines.
- Semantic landmarks and one `h1` per page. Illustrations of the product get
  `aria-label` or are `aria-hidden` with the point made in text nearby.
- Respect `prefers-reduced-motion`. Motion is optional and never carries meaning.

## Before calling a design done

1. `npx tsc --noEmit` and `npm run build` in `web/`.
2. Look at it at 1280px and 375px in the browser pane; tab through it with the
   keyboard.
3. Re-read every sentence against rules 2 and 3 above.
