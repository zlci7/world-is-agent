---
name: World Is Agent Console
description: A local Chinese game console with pixel scenery and layered windows.
colors:
  farm-bg: "#f7e5bd"
  farm-surface: "#fff0ca"
  farm-surface-alt: "#f6dfac"
  farm-ink: "#482f20"
  farm-muted: "#62442d"
  farm-line: "#b88a53"
  farm-accent: "#80532a"
  farm-accent-ink: "#fff4d7"
  farm-accent-soft: "#edce8f"
  farm-good: "#285638"
  farm-warning: "#873d1b"
  farm-danger: "#8b2e2c"
  farm-edge: "#80532f"
  farm-shade: "#543923"
  farm-highlight: "#fff5d2"
  farm-window: "rgb(247 229 189 / 82%)"
  farm-rail: "rgb(231 190 129 / 88%)"
  farm-panel: "rgb(255 240 202 / 96%)"
  farm-panel-alt: "rgb(246 223 172 / 96%)"
  farm-window-mobile: "rgb(247 229 189 / 90%)"
  farm-rail-mobile: "rgb(231 190 129 / 94%)"
  colony-bg: "#20313b"
  colony-surface: "#293f49"
  colony-surface-alt: "#344e55"
  colony-ink: "#f0f0df"
  colony-muted: "#cdd8cf"
  colony-line: "#77919a"
  colony-accent: "#edc27f"
  colony-accent-ink: "#24333b"
  colony-accent-soft: "#4c5c55"
  colony-good: "#b4dfb8"
  colony-warning: "#f2c482"
  colony-danger: "#ffc0b5"
  colony-edge: "#15252e"
  colony-shade: "#0a1720"
  colony-highlight: "#526a72"
  colony-window: "rgb(32 49 59 / 84%)"
  colony-rail: "rgb(41 60 69 / 90%)"
  colony-panel: "rgb(41 63 73 / 97%)"
  colony-panel-alt: "rgb(52 78 85 / 97%)"
  colony-window-mobile: "rgb(32 49 59 / 92%)"
  colony-rail-mobile: "rgb(41 60 69 / 96%)"
typography:
  display:
    fontFamily: '"Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", sans-serif'
    fontSize: "26px"
    fontWeight: 700
    lineHeight: 1.4
    letterSpacing: ".04em"
  headline:
    fontFamily: '"Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", sans-serif'
    fontSize: "28px"
    fontWeight: 700
    lineHeight: 1.4
    letterSpacing: ".04em"
  title:
    fontFamily: '"Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", sans-serif'
    fontSize: "24px"
    fontWeight: 700
    lineHeight: 1.5
  panel-title:
    fontFamily: '"Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", sans-serif'
    fontSize: "16px"
    fontWeight: 700
    lineHeight: 1.5
  body:
    fontFamily: '"Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", sans-serif'
    fontSize: "14px"
    fontWeight: 400
    lineHeight: 1.8
  task-body:
    fontFamily: '"Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", sans-serif'
    fontSize: "14px"
    fontWeight: 400
    lineHeight: 1.9
  label:
    fontFamily: '"Microsoft YaHei UI", "Microsoft YaHei", "PingFang SC", sans-serif'
    fontSize: "12px"
    fontWeight: 400
    lineHeight: 1.8
  technical:
    fontFamily: "Consolas, monospace"
    fontSize: "12px"
    fontWeight: 400
    lineHeight: 1.8
rounded:
  square: "0px"
  status-dot: "50%"
spacing:
  micro: "4px"
  icon: "6px"
  tight: "8px"
  compact: "12px"
  regular: "16px"
  inset: "20px"
  section: "24px"
  major: "32px"
  shell-bottom: "48px"
  empty-state: "64px"
components:
  button-primary:
    backgroundColor: "{colors.farm-accent}"
    textColor: "{colors.farm-accent-ink}"
    rounded: "{rounded.square}"
    padding: "8px 16px"
  button-secondary:
    backgroundColor: "{colors.farm-surface}"
    textColor: "{colors.farm-ink}"
    rounded: "{rounded.square}"
    padding: "8px 16px"
  button-hover:
    backgroundColor: "{colors.farm-accent-soft}"
    textColor: "{colors.farm-ink}"
  input:
    backgroundColor: "{colors.farm-surface}"
    textColor: "{colors.farm-ink}"
    rounded: "{rounded.square}"
    padding: "8px 12px"
  navigation-active:
    backgroundColor: "{colors.farm-surface}"
    textColor: "{colors.farm-ink}"
    rounded: "{rounded.square}"
    padding: "10px 12px"
  surface:
    backgroundColor: "{colors.farm-panel}"
    textColor: "{colors.farm-ink}"
    rounded: "{rounded.square}"
  task-selected:
    backgroundColor: "{colors.farm-panel-alt}"
    textColor: "{colors.farm-ink}"
    rounded: "{rounded.square}"
    padding: "16px 12px"
  status-detail:
    backgroundColor: "{colors.farm-surface-alt}"
    textColor: "{colors.farm-muted}"
    rounded: "{rounded.square}"
    padding: "6px 10px"
---

# Design System: World Is Agent Console

## Overview

**Creative North Star: "Pixel Game Windows"**

Pixel landscapes sit behind framed, translucent windows. Warm farmland accompanies Stardew Valley; a cool colony landscape accompanies RimWorld. Chinese system typography, aligned information, and compact controls keep appointments and configuration readable.

The Vue console uses the same layout and components in both themes. The loaded game determines the palette and scenery. Theme replacement is immediate; configuration progress and errors appear beside the controls.

**Key Characteristics:**

- Square frames, inset highlights, and short hard shadows.
- Layered background alpha with fully opaque reading text.
- A list-and-detail workbench that becomes a single-pane mobile view.
- Text and icons distinguish task states and world availability.

## Colors

The frontmatter records the two palettes from [the production stylesheet](src/style.css). Each `farm-*` and `colony-*` suffix maps to the same CSS custom property on the application container. Component primitives show the farm defaults; the colony palette replaces their corresponding roles.

### Primary

`accent` colors primary actions, active tabs, icons, and focus outlines. `accent-ink` provides primary-button text; `accent-soft` supplies control hover backgrounds. The farm theme uses brown; the colony theme uses amber.

### Neutral

`ink` and `muted` provide primary and supporting text. `surface` and `surface-alt` are opaque control fills. `line` separates records and outlines fields; `edge`, `shade`, and `highlight` construct the pixel frames. `window`, `rail`, `panel`, and `panel-alt` carry the layered background alpha. Mobile increases window and rail opacity while retaining the same panel fills.

### Status

`good` colors ready worlds and successful tasks, `warning` colors unavailable worlds and paused tasks, and `danger` colors failed tasks and errors. Waiting and cancelled tasks use muted text; running tasks use the accent. Every task state also has a Chinese label and icon.

**The Layered Fill Rule.** Apply transparency to background colors. Keep normal text and controls fully opaque; disabled controls use the implemented reduced opacity.

## Typography

The interface uses Microsoft YaHei UI, Microsoft YaHei, PingFang SC, and a sans-serif fallback. Technical identifiers use Consolas and monospace. Pixel character comes from scenery and frames; text uses ordinary readable system glyphs.

The frontmatter records desktop display, page heading, detail title, panel title, body, label, and technical roles. Page descriptions and navigation use a compact intermediate size (13px); supporting metadata generally uses labels, with small timestamps and footer text (11px). Task instructions preserve line breaks and use the more open task-body leading. Long instructions and identifiers wrap within their columns.

At 820px and below, the masthead display shrinks to 24px. At 620px and below, the masthead becomes 22px, page headings 24px, and detail titles 22px. Mobile supporting text generally uses 11px; footer text uses 10px.

## Layout

The centered masthead and shell share a maximum width (1248px). The desktop shell has a left rail (192px), a flexible workspace, and a minimum height (800px). Workspace and panel padding normally use section spacing. The appointment workbench uses `minmax(280px, .95fr) minmax(0, 1.2fr)` with a section-sized gap. Settings use `minmax(260px, .85fr) minmax(0, 1.15fr)`.

| Media query | Implemented layout |
| --- | --- |
| `min-width: 1550px` | Shell and masthead maximum width becomes 1328px; workspace padding becomes 32px. |
| `max-width: 1420px` | Shell and masthead receive 24px horizontal margins. |
| `max-width: 1150px` | Shell margins become 20px; rail becomes 168px. Workbench uses `minmax(244px, 1fr) minmax(0, 1.15fr)` with a 16px gap; detail padding becomes 20px. |
| `max-width: 820px` | Navigation becomes a horizontal header; its icons and the rail footer are hidden. Workbench remains two columns: `minmax(244px, .9fr) minmax(0, 1.15fr)`. |
| `max-width: 620px` | Shell margins become 12px; workspace padding becomes `20px 16px`. Navigation wraps beneath the brand. The workbench shows either the list or the selected detail. Settings and overview stack; the overview band becomes vertical. The log table retains time, actor, and status columns. |

The world selector and availability share a context strip. On mobile the selector occupies one row and availability moves below a divider. Pane headers, list rows, metadata, and footers align to their respective container insets. The body supports a minimum viewport width (320px).

## Elevation & Depth

Depth comes from transparent fills, solid frames, inset highlights, and hard offset shadows. The shell alone adds a broad ambient shadow. The fixed scenery uses `center top / cover`, pixelated rendering, and no pointer interaction.

- Shell: `0 6px 0 var(--shade), 0 18px 35px #16312435`.
- Panel: `inset 0 0 0 2px var(--highlight), 0 3px 0 var(--shade)`.
- Button and active navigation: `2px 2px 0 var(--shade)`; pressed buttons translate by `1px 1px` and use `1px 1px 0 var(--shade)`.
- Selected task: `inset 0 0 0 1px var(--highlight)` inside a one-pixel edge.

**The Loaded Game Rule.** Update scenery and palette when the Runtime reports the loaded game. A radio selection alone does not change the theme. Retain the last loaded theme during a connection interruption.

## Shapes

Controls, windows, avatars, and status badges have square corners. Controls and panels use two-pixel borders; the shell uses four pixels, reduced to three on mobile. Circular status dots are small (6px). Most icons are outlined SVGs (18px square, 24-unit viewBox, 1.6 stroke width); status icons use 12px and empty-state icons use 32px.

## Components

### Buttons and fields

Primary buttons use accent and accent-ink; secondary buttons use surface and ink. Buttons and fields have a minimum height (40px). Text actions use an accent label with a clear hit area and no frame. Hover changes the fill immediately. Keyboard focus uses a three-pixel accent outline with a four-pixel offset. Disabled buttons, inputs, and selects use `opacity: .6` and a not-allowed cursor.

Fields have visible labels above them, a two-pixel line border, and supporting text below when needed. Password fields mask the entered API key. Configuration failures appear as inline alerts; pending submissions use disabled controls and nearby progress text.

### Navigation and tabs

Active navigation uses a surface fill, edge border, and short shadow; `aria-current="page"` identifies the current page. Collection tabs use an accent underline (3px), a text label, and a small count. The read-only task status filter remains a native select.

### Task list and detail

Rows are full-width buttons with a minimum height (136px). Each row aligns an initial avatar, actor name, state, instruction, and timestamp. Selection adds a panel-alt fill, edge border, and inset highlight; `aria-pressed` expresses selection. Detail shows the agreement, task facts, outcome, and a native advanced-details disclosure.

At mobile widths, opening a task focuses the visible return button. Returning to the list restores focus to the selected row. World availability and task state retain separate labels. Read failures keep existing records accompanied by a synchronization notice and last successful read time. Loading, empty, unsupported, and disconnected states occupy framed content areas with explicit messages.

### Configuration and notices

Game choices are framed labels with a native radio control; selection changes the fill and border. The currently loaded game has its own text label. Applying a game or model disables navigation and shows an inline application notice. Model submission displays its connection-check progress and field-level context inside the same panel.

Notices use panel-alt fill, a line border, and regular internal spacing. Error notices use danger text. Advanced information uses native `details` and `summary`, with a plus/minus indicator and wrapping technical identifiers.

## Do's and Don'ts

### Do

- **Do** reuse the paired theme roles and the existing square frames.
- **Do** preserve aligned insets and readable Chinese system typography.
- **Do** keep world availability, task state, and configuration progress explicit in text.
- **Do** retain keyboard focus when mobile readers move between list and detail.

### Don't

- **Don't** lower normal text opacity to create translucent windows.
- **Don't** preview a theme from an unapplied game selection.
- **Don't** add theme-transition overlays, animations, or decorative UI.
