---
name: vellatry-design
description: Vellatry's design language for everything people see - the dashboard (web/app, web/components), the landing page, sign-in, the setup wizard, report and email templates, and product copy. Use it before designing, restyling or writing copy for any page or component, and when building a mock for review.
---

# Vellatry design

Vellatry sells one thing: numbers a marketing team can defend to their CMO. The look is
**paper and highlighter**: warm paper, heavy black type, and a lime highlighter that
marks where you are mentioned, with coral for where a competitor was named instead. It
should feel like a marked-up report, confident and precise.

What it must not look like is the default AI landing page. That means none of:
- a serif-italic accent in the headline (Instrument Serif and its cousins);
- Inter, Geist or Plus Jakarta as the only face;
- purple-to-blue gradients or glassmorphism;
- sparkle or robot icons, or a "bento" grid of vague claims.

## Rules that are not negotiable

1. **No UI framework, no component library, no chart library, no icon library, no
   animation library** (decision 57). One stylesheet, `web/app/globals.css`, plus a CSS
   module for a page or component that needs its own layout. Charts are inline SVG.
   Icons, when unavoidable, are inline SVG with `aria-hidden`.
2. **Never invent evidence.** Marketing copy and mocks show no customer names or
   logos, no testimonials, no statistics, and no pricing, certifications or
   integrations that the code does not have. Product illustrations use "Your brand" and
   "Competitor A/B/C", never real or made-up brand names, and carry an "Illustration"
   caption whenever they show figures.
3. **Describe what is shipped.** Every feature sentence must be true of the code today.
   Check the README's Status section before claiming a capability. A product mock
   mirrors a real page's layout and wording (the tour in `components/ProductTour.tsx`
   follows Today, a blindspot, Test my setup and Fixes).
4. **Numbers in the product come from the api.** A page formats them with
   `lib/format.ts` (en-AU), shows `-` for missing, and never computes a figure the
   backend did not.

## Colour

Defined once on `:root` in `globals.css`. Use the variables, never raw hex, in new CSS.

| Token | Value | Use |
| --- | --- | --- |
| `--ink` | `#15171c` | Text, primary buttons, the brand |
| `--muted` | `#52555c` | Secondary text |
| `--faint` | `#8d8a82` | Captions, disabled, excluded lookalikes |
| `--line` | `#e6e1d5` | Hairlines, borders, table rules |
| `--bg` | `#f4f1ea` | Paper: the app background and marketing pages. Nothing is flat white. |
| `--panel` | `#ffffff` | Cards and inputs in the app (marketing cards use `#fffdf8`) |
| `--night` | `#111318` | Dark bands, code, the sign-in panel |
| `--lime` / `--lime-soft` / `--lime-ink` | `#d6f25e` / `#ecf8b8` / `#3b4d00` | You: your mentions, highlights, "live", the call to action |
| `--coral` / `--coral-soft` / `--coral-ink` | `#ff6a3d` / `#ffe2d7` / `#b3370f` | Them: a competitor named instead, a blindspot |
| `--accent` | `#1d4ed8` | Links, focus rings, data lines in the app |
| `--good` / `--bad` / `--warn` | green / red / amber | Meaning only, never decoration |

Mentions have fixed colours everywhere they appear (tester, answers, reports, mocks):
your brand in lime (`--lime-soft` on `--lime-ink`), a competitor in coral
(`--coral-soft` on `--coral-ink`), an excluded lookalike struck through in `--faint`.
Lime on white text fails contrast: lime is always a background under ink.

## Type

- **One family: Archivo** (`web/lib/fonts.ts`, via `next/font`, self-hosted), a
  grotesque with a width axis. The app and body text use it at normal width, 15px/1.55.
- **Marketing headlines** stretch it: weight 800, `font-variation-settings: 'wdth' 112`
  to `118`, tight leading (0.96 to 1.02) and letter-spacing around -0.03em, with
  `text-wrap: balance`. H1 is `clamp(44px, 6.4vw, 86px)`. App titles stay at normal
  width, 17 to 22px.
- **Labels and kickers** (section eyebrows, tags, step numbers) are the system
  monospace in uppercase, 11 to 13px: they read as data, not decoration.
- Numbers in tables and tiles use `font-variant-numeric: tabular-nums`.
- Headings are sentence case.

## Motion

Motion is part of the marketing pages and a light touch in the app. CSS keyframes and
transitions only, with three small client components for what CSS cannot read:
`ProductTour` (autoplay state), `CountUp` (a number counting when it scrolls into view)
and `PointerEffects` (`data-tilt` leans toward the cursor, `data-lens` tints the hero's
graph paper under it). Pointer effects are mouse-only and never run with reduced motion.

- **Every animation runs from a hidden or partial state to the element's ordinary
  style** (`from` keyframes only, `animation-fill-mode: both`). A browser without the
  feature, or with motion reduced, then simply shows the final state.
- **Show the product working.** The live check on the landing page plays the product's
  own story: a question goes out, the answer arrives, mentions colour in, the verdict,
  then the fix goes live. Prefer that over decorative motion.
- Scroll reveals use `animation-timeline: view()` inside `@supports`: progressive
  enhancement, no observer scripts.
- Loops (the ticker, a pulse) must pause on hover or stop within a few seconds, and
  the tour's autoplay stops for good once someone clicks.
- **Reduced motion means no movement, not no change.** Under
  `prefers-reduced-motion: reduce`, whatever would rise or slide fades in place, and
  every loop, tilt, bob, drift and autoplay stops. Colour-ins and wipes stay: they
  aren't motion. Windows' "Animation effects" switch sets this, so many work laptops
  have it on. Test both ways.

## Layout and spacing

- Spacing steps: 4, 8, 12, 16, 24, 32, 48, 64, 96, 104 px.
- The app: 232px sidebar, content max 1180px. Marketing: content max 1160px, sections
  104px apart on desktop and 72px on mobile.
- Radius: 16px for marketing cards, 8 to 10px for app cards, 6px for controls, 999px
  for pills and marketing buttons.
- Every page works at 375px wide with 16px side gutters and no horizontal scroll.
  Grids collapse to one column below 640px.

## Components (reuse before writing new ones)

- `components/ui.tsx`: `Card`, `Tile`, `MetricTile`, `Table`, `Chart`, `Pill`, `Empty`,
  `ErrorNote`, `Loading`, `RangePicker`.
- `components/brand.tsx`: `ListEditor` (chips), `Tester` (highlighted mentions),
  `Recognition`, `Differentiators`, `CompetitorsEditor`, `TopicPicker`.
- `components/Mark.tsx`: the logo, a ring with one lime segment (the blindspot).
- `components/ProductTour.tsx`: the app in a window, for marketing.
- `components/AuthFrame.tsx`, `components/GoogleConnect.tsx`, `components/Shell.tsx`
  (layout and gate), `components/Nav.tsx`.
- Buttons: one primary (ink) per view; everything else outlined. On lime, the
  secondary is an ink outline.
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
- Visible focus: 2px `--accent` outline, 2 to 3px offset. Never remove outlines.
- Semantic landmarks and one `h1` per page. Illustrations of the product get an
  `aria-label` or are `aria-hidden` with the point made in text nearby. Tabs are real
  tabs (`role="tablist"`, `aria-selected`, `aria-controls`).

## Before calling a design done

1. `npx tsc --noEmit` and `npm run build` in `web/`.
2. Look at it at 1280px and 375px, and check for horizontal overflow
   (`document.documentElement.scrollWidth === innerWidth`). The browser pane follows
   the machine's reduced-motion setting, and headless Edge
   (`msedge --headless=new --screenshot`) gives a full-page capture.
3. Look at it with motion reduced too, and tab through it with the keyboard.
4. Re-read every sentence against rules 2 and 3 above.
