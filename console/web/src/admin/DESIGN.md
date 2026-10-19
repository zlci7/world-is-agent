---
name: World Is Agent Administration
description: A clear Chinese administration workspace with solid surfaces and precise data alignment.
colors:
  accent: "#214fa4"
  accent-hover: "#183e85"
  canvas: "#f3f5f8"
  surface: "#fff"
  ink: "#243147"
  muted: "#57667a"
  line: "#dce2ea"
  field-line: "#abb8c9"
  placeholder: "#66768b"
  secondary-line: "#bac6d6"
  secondary-ink: "#30435e"
  secondary-hover: "#eef2f7"
  navigation-ink: "#40516b"
  navigation-hover: "#f1f4f9"
  navigation-active: "#e8eef9"
  text-action-hover: "#edf2fa"
  table-heading: "#f8fafc"
  table-heading-ink: "#52627a"
  table-line: "#e5e9ef"
  table-hover: "#fbfcff"
  badge-fill: "#edf2f8"
  badge-ink: "#405573"
  danger: "#a62e35"
  danger-hover: "#85252b"
  error-fill: "#fff5f4"
  error-ink: "#8a2931"
  error-line: "#e8c4c4"
  danger-badge-fill: "#fbeceb"
  success-fill: "#eff8f2"
  success-ink: "#245638"
  success-line: "#b8d7c3"
typography:
  headline:
    fontFamily: '"Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", sans-serif'
    fontSize: "25px"
    fontWeight: 700
    lineHeight: 1.4
  title:
    fontFamily: '"Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", sans-serif'
    fontSize: "17px"
    fontWeight: 700
    lineHeight: 1.5
  group-title:
    fontFamily: '"Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", sans-serif'
    fontSize: "15px"
    fontWeight: 700
    lineHeight: 1.65
  body:
    fontFamily: '"Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", sans-serif'
    fontSize: "14px"
    fontWeight: 400
    lineHeight: 1.65
  label:
    fontFamily: '"Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", sans-serif'
    fontSize: "14px"
    fontWeight: 500
    lineHeight: 1.65
  table:
    fontFamily: '"Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", sans-serif'
    fontSize: "13px"
    fontWeight: 400
    lineHeight: 1.65
  metadata:
    fontFamily: '"Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", sans-serif'
    fontSize: "12px"
    fontWeight: 400
    lineHeight: 1.65
  metric:
    fontFamily: '"Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", sans-serif'
    fontSize: "24px"
    fontWeight: 600
    lineHeight: 1.4
  technical:
    fontFamily: "Consolas, monospace"
    fontSize: "12px"
    fontWeight: 400
    lineHeight: 1.65
rounded:
  badge: "4px"
  field: "5px"
  button: "6px"
  section: "7px"
  login: "8px"
spacing:
  micro: "4px"
  tight: "8px"
  compact: "12px"
  regular: "16px"
  inset: "20px"
  section: "24px"
  major: "28px"
  spacious: "36px"
  workspace: "40px"
  bottom: "64px"
components:
  button-primary:
    backgroundColor: "{colors.accent}"
    textColor: "{colors.surface}"
    rounded: "{rounded.button}"
    padding: "9px 16px"
  button-primary-hover:
    backgroundColor: "{colors.accent-hover}"
  button-secondary:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.secondary-ink}"
    rounded: "{rounded.button}"
    padding: "9px 16px"
  button-secondary-hover:
    backgroundColor: "{colors.secondary-hover}"
  button-text:
    backgroundColor: "transparent"
    textColor: "{colors.accent}"
    rounded: "{rounded.button}"
    padding: "6px 4px"
  button-danger:
    backgroundColor: "{colors.danger}"
    textColor: "{colors.surface}"
    rounded: "{rounded.button}"
    padding: "9px 16px"
  button-danger-hover:
    backgroundColor: "{colors.danger-hover}"
  field:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.ink}"
    rounded: "{rounded.field}"
    padding: "9px 11px"
  navigation-active:
    backgroundColor: "{colors.navigation-active}"
    textColor: "{colors.accent}"
    rounded: "{rounded.field}"
    padding: "11px 14px"
  badge:
    backgroundColor: "{colors.badge-fill}"
    textColor: "{colors.badge-ink}"
    typography: "{typography.metadata}"
    rounded: "{rounded.badge}"
    padding: "3px 8px"
  section:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.ink}"
    rounded: "{rounded.section}"
    padding: "24px"
  login:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.ink}"
    rounded: "{rounded.login}"
    padding: "36px"
---

# Design System: World Is Agent Administration

## Overview

**Creative North Star: "Clear Administration"**

Solid white surfaces sit on a light gray-blue canvas. Chinese system typography, restrained blue actions, and aligned records make account operations and usage inspection easy to scan. The visual character is quiet, direct, and data-focused.

This design system governs the independent administration entry at `/admin/login` and the `/admin` workspace, including users, invitations, usage, and audit pages. The player console retains the separate [Pixel Game Windows design system](../../DESIGN.md). Administration uses its own [stylesheet](admin.css) and [application entry](AdminApp.vue).

**Key Characteristics:**

- Opaque white surfaces, fine borders, and gently curved corners.
- Blue actions and active navigation with explicit text labels.
- Desktop sidebar navigation and a wrapping top navigation on narrow screens.
- Full data tables with local horizontal scrolling and aligned numeric columns.

## Colors

The frontmatter extracts the shared palette from the administration stylesheet. Cool neutrals carry the structure; blue marks interaction.

### Primary

`accent` is the administration blue for primary actions, links, selected navigation text, radio controls, and focus outlines. `accent-hover` deepens primary actions and text actions on hover. `navigation-active` provides the pale selected-page fill; `text-action-hover` supports underlined inline actions.

### Neutral

`canvas` fills the page. `surface` fills the sidebar, login panel, content sections, fields, and secondary buttons. `ink` and `muted` distinguish content from supporting metadata. `line` separates the shell and content sections; `field-line` outlines native fields. Secondary controls and navigation use their dedicated ink, border, and hover roles.

`table-heading` and `table-heading-ink` distinguish column labels. `table-line` separates records and `table-hover` follows the hovered row. Neutral badges use `badge-fill` and `badge-ink`.

Error and success messages combine their named fill, ink, and line roles. Destructive confirmation actions use `danger` and `danger-hover`; unavailable account badges use `danger-badge-fill` and `error-ink`. Each state has a written label.

**The Solid Surface Rule.** Administration panels, navigation, and fields use opaque fills over the canvas.

## Typography

All interface copy uses Microsoft YaHei UI, Microsoft YaHei, PingFang SC, and the system sans-serif fallback. Technical identifiers use Consolas and monospace. The frontmatter records the heading, body, field-label, table, metadata, metric, and technical roles.

Page headings are bold; section titles are compact. Primary and secondary buttons use semibold text (600). Field labels use medium weight. Table headers and emphasized row values use semibold text. Paragraphs have a maximum measure (78ch).

Numeric table cells are right-aligned and use tabular numerals. Overview metrics and inline totals also use tabular numerals. Technical identifiers wrap within their available width. At the narrow breakpoint, workspace headings shrink (23px), metrics shrink (21px), and compact fact values use table-sized text (13px).

**The Numeric Alignment Rule.** Align numeric table headings and values to the right and preserve tabular figures.

## Layout

The desktop shell pairs a sticky, full-height sidebar (224px) with a flexible content column. Content has a maximum width (1600px) and padding (`36px 40px 64px`). Section panels use section spacing; summary facts form four equal columns. The centered login panel has a maximum width (400px) inside a viewport-height layout.

| Media query | Implemented layout |
| --- | --- |
| `max-width: 1050px` | Sidebar becomes 190px. Content padding becomes `28px 24px 48px`. Summary facts use two columns; invitation creation uses two fields with the action below. |
| `max-width: 700px` | Navigation becomes a static top section with wrapping links and the account row below. Content padding becomes `24px 16px 40px`. Headings and toolbars stack or wrap. Panels use `20px 16px`; invitation creation and audit filters use one column. Compact user facts use one column. Login padding becomes `28px 24px`. |

Tables retain their columns inside focusable horizontal scroll regions. Standard tables have a minimum width (680px); request details use 1020px, users use 1080px, and audit records use 1200px. Narrow screens show a written scrolling hint. Table cells wrap long content while numeric columns remain aligned. Pagination sits beneath the table, aligned to the end of the panel.

**The Contained Table Rule.** Keep horizontal data overflow within the table region so the surrounding page remains readable at narrow widths.

## Elevation & Depth

The administration workspace is flat. White surfaces, cool borders, and spacing establish hierarchy without box shadows. Selected navigation and hovered rows use pale tonal fills. Keyboard focus uses an accent outline (3px) with an offset (3px), including focusable table regions. Interactive state changes are immediate; the stylesheet defines no animation or transition tokens.

## Shapes

The form language uses fine one-pixel borders and a small radius scale. Badges are the tightest shape; fields, notices, and navigation share the field radius. Buttons, content sections, and the login panel step outward through their named radii. Native radio controls remain circular and use the administration accent.

## Components

### Buttons

Primary actions use blue fill and white text. Secondary actions use white fill, a visible cool border, and dark blue-gray text. Destructive confirmation buttons use the danger fill. Underlined text actions retain a clear hit area and use a pale blue hover fill. Ordinary buttons have a minimum height (42px); text actions use 40px. Disabled buttons use reduced opacity (.55) and a not-allowed cursor.

### Fields

Inputs, selects, and textareas are native controls with visible labels, white fill, a fine field border, and the field radius. Fields have a minimum height (42px), with label separation (6px). Textareas resize vertically. Number, date, email, and password inputs preserve native input behavior; radio choices retain their visible labels.

### Navigation

The administration navigation contains five text destinations: 总览, 用户管理, 邀请码, 用量审计, and 操作日志. The current page has a pale blue fill, blue bold text, and `aria-current="page"`. Desktop links form a vertical list; narrow-screen links wrap across the top section. Brand text and the account row use the same solid sidebar surface.

### Sections and summary facts

Content sections use white fill, a fine border, the section radius, and a consistent inset. Section headings align titles with supporting text or actions. Summary values use the metric role; labels use muted table-sized text. A divided inline-stat row carries smaller totals. Table-only sections let the table and pagination meet the panel edges.

### Tables and badges

Tables use compact text, a pale header fill, row dividers, and a subtle hover fill. Supporting timestamps and identifiers occupy separate lines within cells. Badges use a small rectangular fill and a written state; unavailable accounts use the danger badge variant. Empty table bodies show centered explanatory text.

### Notices and confirmation

Errors and successful operations appear beside the relevant controls in bordered inline messages. Read failures preserve the previous table data with a visible last-read timestamp. Destructive actions reveal an inline confirmation region. Focus moves into that region and returns to its originating control on cancellation. The invitation secret appears in a tinted, bordered area with a native read-only textarea and copy action.

## Do's and Don'ts

### Do

- **Do** use opaque white administration surfaces and the blue action palette.
- **Do** preserve readable Chinese system typography and aligned numeric columns.
- **Do** keep complete table columns inside keyboard-focusable horizontal scroll regions.
- **Do** pair every account, request, and feedback state with explicit text.
- **Do** retain visible focus when opening user details or cancelling confirmations.

### Don't

- **Don't** apply player-console farm scenery, translucent game windows, or game-connection navigation to administration.
- **Don't** substitute decorative typefaces for the Chinese system font stack.
- **Don't** make the page itself the horizontal scrolling surface for a data table.
- **Don't** use color alone to communicate state or destructive consequences.
