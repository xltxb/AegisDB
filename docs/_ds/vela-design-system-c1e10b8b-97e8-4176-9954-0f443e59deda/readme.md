# Vela Design System

> **Vela** is a fictional internet-platform brand created for this design system: a **realtime collaboration & data platform** ("以光速前行 — move at the speed of light"). The look is **young & energetic in light mode, futuristic & neon in dark** — built on a vivid azure blue with a cyan glow accent. Use it to mock SaaS consoles, mobile apps, marketing pages, and decks that need to feel like a modern, confident tech product.

Vela = the constellation "the Sails." The logomark is a stylized sail with a glowing star node.

---

## Sources & provenance

This system was **not** derived from an existing codebase — the attached repo (`github.com/xltxb/design`) was empty at build time, so the brand, tokens, components and screens here are an **original creation**. If you later populate that repo (or point us at the real product), we can re-baseline everything against the true source for higher fidelity.

- Input repo (empty at time of authoring): `https://github.com/xltxb/design`

**Fonts are Google Fonts stand-ins**, chosen to match the intended Vela voice. Swap for licensed brand fonts when available (see *Visual Foundations → Type*).

---

## How this system is organized

| Path | What's there |
|---|---|
| `styles.css` | Root entry — `@import` manifest only. **Consumers link this one file.** |
| `tokens/` | `colors.css`, `typography.css`, `spacing.css`, `effects.css`, `motion.css`, `fonts.css`, `base.css` |
| `components/forms/` | Button, Input, Select, Checkbox, Switch |
| `components/core/` | Card, Badge, Avatar, Tabs, Alert, Spinner |
| `ui_kits/web/` | **Vela Console** — SaaS dashboard (login → overview → projects → realtime → members → settings) |
| `ui_kits/mobile/` | **Vela Mobile** — phone app (home · projects · settings) with tab bar |
| `slides/` | 6 deck templates (title, section, stat, content, quote, closing) |
| `guidelines/` | Foundation specimen cards (colors, type, spacing, brand) |
| `assets/` | Logo (mark + light/dark wordmark) |
| `SKILL.md` | Agent-Skill manifest for use in Claude Code |

Components are consumed via the compiled bundle: `const { Button, Card } = window.VelaDesignSystem_c1e10b`.

---

## CONTENT FUNDAMENTALS — how Vela writes

**Voice:** confident, fast, technical-but-human. Vela talks like an engineer who ships. It is never salesy or fluffy.

- **Bilingual 中英混排.** UI is Chinese-first with English running mates for product nouns and metrics: `概览 Overview`, `实时 Realtime`, `p50 延迟`. Marketing headlines lean Chinese; code/metrics/labels stay English + mono.
- **Person:** addresses the user as **你** ("晚上好,Lin 👋 — 这是你团队今天的概况"). Refers to itself as **Vela**, not "we/我们" in UI.
- **Casing:** English UI labels are **Title Case** for nav, **sentence case** for body. Mono eyebrows are **UPPERCASE** with wide tracking (`REALTIME PLATFORM`).
- **Numbers are the hero.** Lead with concrete metrics — `99.98%`, `12ms`, `40k+`, `1.24M`. Always pair a number with a one-line unit/label. Deltas use `+12.4%` / `-0.4%`, never prose.
- **Tone of verbs:** imperative and short — `开始使用`, `新建项目`, `升级到 Pro →`. CTAs often end with a `→`.
- **Emoji:** used **very sparingly** — a single 👋 in a greeting, occasionally ⚡. Never decorative emoji in lists or as bullets/icons (use Lucide). Never more than one per screen.
- **Length:** microcopy is terse. Helper text is one sentence. Empty states get one line + one action.

**Examples in the wild:** `晚上好,Lin 👋`, `用量达到本月配额的 60%`, `部署了 realtime-gateway v2.4`, `免费创建你的第一个 Vela 工作区,5 分钟接入实时能力。`

---

## VISUAL FOUNDATIONS

**Color.** Brand is **Azure** (`--azure-500 #3b6ef6`) — vivid, saturated, optimistic. The signature accent is **Cyan** (`--cyan-400 #2dcde6`), used as a *neon glow* and for data highlights / live status. Violet is a rare tertiary pop. Neutrals are a **cool, slightly-blue Slate** ramp (never warm gray). Semantic: green/amber/red. **Always use semantic aliases** (`--surface-card`, `--text-body`, `--accent`) in product UI; raw ramps are for data-viz.

- **Gradients:** allowed but disciplined — **azure → cyan** only (the brand glow). **Never** azure → violet/purple (that reads generic). Gradients appear on: hero fills, the usage bar, big-stat numerals (`-webkit-background-clip:text`), and the mobile "upgrade" tile.
- **Imagery vibe:** cool, deep blue-blacks (`#0a0c14`) with cyan/azure radial glows. Dark scenes feel like a night sky (fitting the constellation brand). No photography baked in — bring your own; keep it cool-toned.

**Type.** Display = **Space Grotesk** (geometric, techy) for headlines & hero numerals, tracked tight (`-0.02em` to `-0.03em`). Body/UI = **Manrope** + **Noto Sans SC** for 中英混排, line-height 1.5–1.7. Mono = **JetBrains Mono** for code, metrics, and UPPERCASE eyebrows. Scale runs `--text-xs (12)` → `--text-6xl (60)`; slides go bigger (up to 84px).

**Spacing & layout.** 4px base grid (`--space-*`). Generous whitespace; cards breathe (`padding md = 24px`, `lg = 32px`). Containers max ~1480px. Control heights 32 / 40 / 48px; touch targets ≥ 44px on mobile.

**Corners.** Medium-rounded house style: controls/cards inner `--radius-md 10px`, panels `--radius-lg 14px`, hero blocks `--radius-xl 20px`, pills/avatars `--radius-full`. Never sharp (0) except full-bleed; never pill-shaped buttons.

**Borders.** Hairline `1px` everywhere — `--border-subtle` (cards) and `--border-default` (inputs). In dark mode borders become translucent white (`rgba(255,255,255,.07–.22)`) instead of solid.

**Shadows & elevation.** Light mode = soft, **cool slate-tinted** shadows (`--shadow-xs → xl`), never black. Cards default to `flat` (hairline) or `raised` (`shadow-md`). **Dark mode drops drop-shadows** and leans on borders + **neon glow** instead.

**Glow (signature).** `--glow-sm` (azure) / `--glow-md` (cyan) — a tight colored ring + soft colored shadow. Used on: primary CTA **hover**, focus states in dark, and `Card elevation="glow"`. Use sparingly in light mode; freely (but tastefully) in dark.

**Motion.** Quick & confident. Default easing `--ease-out cubic-bezier(.16,1,.3,1)` (smooth decel), durations `--dur-fast 140ms` / `--dur-base 220ms`. **No bounce on UI** — reserve `--ease-spring` for playful accents only (the Switch thumb, badges). All durations collapse to 0 under `prefers-reduced-motion`.

**Interaction states.**
- *Hover:* primary button darkens (`--accent-hover`) **and** gains glow; secondary/ghost fill with `--surface-sunken`; cards (`interactive`) lift `-2px` + `shadow-lg`.
- *Press:* buttons `translateY(1px) scale(.99)` (a subtle physical push), card lift returns to 0.
- *Focus:* 2px `--accent` outline at 2px offset, or a 3px `--focus-ring` halo on inputs/switches.
- *Disabled:* `opacity .5`, `cursor not-allowed`, no transform.

**Transparency & blur.** The top bar uses `backdrop-filter: blur(8px)` over a translucent surface (`color-mix`). Overlays/scrims use `--surface-overlay`. Blur tokens: `--blur-sm/md/lg`.

**Two themes.** Light is the default (clean, white cards on `--slate-50`). Dark is deep blue-black (`#0a0c14` page, `#141826` cards) with brighter accents and glow. Toggle with `<html data-theme="dark">` — every semantic token re-resolves; raw ramps stay fixed.

---

## ICONOGRAPHY

- **Icon set: [Lucide](https://lucide.dev)** (MIT) — clean 2px-stroke line icons that match Vela's geometric, technical feel. **This is a substitution** (no brand icon set existed in the source); swap if the real product standardizes on another set.
- **Delivery:** loaded from CDN (`unpkg.com/lucide`) and rendered via `window.lucide.createIcons()` against `<i data-lucide="name">` elements. Icons inherit `currentColor` and a 2px stroke.
- **Sizing:** 16px (inline/badges), 18px (nav, buttons), 20–22px (mobile, feature tiles). Never below 14px.
- **Color:** icons take the text color of their context (`--text-muted` for nav idle, `--accent-text` when active/featured). Featured icons sit in a `--radius-md` chip with `--accent-subtle` background.
- **No hand-drawn SVG icons, no emoji-as-icon, no unicode glyphs.** The only bespoke SVG is the **logo** (`assets/vela-*.svg`) and inline data-viz (sparklines, charts), which are intentionally not icons.
- Common icons in use: `layout-dashboard, folder-kanban, activity, users, settings, search, bell, moon, sun, plus, filter, rocket, user-plus, alert-triangle, trending-up, zap, shield-check, box, arrow-right`.

---

## Component index

**Forms** — `Button` (primary/secondary/outline/ghost/danger × sm/md/lg, loading, icons), `Input` (label/hint/error/icons), `Select`, `Checkbox`, `Switch`.
**Core** — `Card` (flat/raised/glow, interactive), `Badge` (5 colors × soft/solid, dot), `Avatar` (initials fallback, presence), `Tabs` (pill/line), `Alert` (4 tones), `Spinner`.

Each component dir has a `.jsx` (impl), `.d.ts` (props contract), `.prompt.md` (when/how), and a `@dsCard` HTML thumbnail.

## UI kits & templates
- `ui_kits/web/index.html` — **Vela Console** (also a *Web App* starting point)
- `ui_kits/mobile/index.html` — **Vela Mobile** (also a *Mobile App* starting point)
- `slides/*.html` — 6 deck templates

---

*To go deeper on the real product, populate `github.com/xltxb/design` and ask for a re-baseline.*
