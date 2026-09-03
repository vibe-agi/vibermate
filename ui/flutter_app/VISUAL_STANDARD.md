# ViberMate desktop visual standard

This file is an implementation contract, not a mood board. A screen is not
finished merely because it compiles or uses the shared theme.

## Product posture

ViberMate is a dense native desktop workbench for operating and auditing Agent
traffic. The primary task is reading the current state quickly; exact evidence
must remain available without making opaque identifiers the visual headline.

## Geometry

- Use a 4 logical-pixel layout grid. Two-pixel values are reserved for optical
  alignment, strokes, and micro gaps inside a control.
- Standard interactive height: 30. Compact toolbar/icon controls: 26. Tabs:
  32. Status indicators: 20.
- Compact list rows: 40-48 depending on whether they contain one or two lines.
- Control radius: 4. Bounded content surface: 8. Dialog: 10. Pills are fully
  rounded. Structural panes, tables, and rails stay square.
- Prefer 8/12/16 padding. A dialog should not become spacious simply because
  the window is large.
- Operational detail panes fill their available split-pane width. Reserve
  narrow max-width columns for prose and forms; do not leave an arbitrary
  empty half-screen beside tables, routes, or evidence plans.

## Type

- Page: 22/600. Dialog: 18/600. Section title: 13/600.
- Primary row/content: 14/500. Body and controls: 13/400-500. Metadata: 12/400.
  Utility labels: 11/600 and only for short tertiary labels.
- Do not use 700. Metadata must never be heavier or larger than the action or
  content it explains.
- A label and its count share a text baseline. Counts use tabular figures.

## Controls and state

- One visual treatment per role: filled for the single primary action, outlined
  for secondary actions, icon-only for row utilities, plain text for tertiary
  actions.
- State is not an action. Do not place a status pill inside an action cluster;
  use a compact dot-and-label status near the object metadata.
- Menus match the trigger width, use 26-high rows, regular-weight text, a 4px
  offset, and a subtle selected/focus treatment.
- Text fields and select triggers share the same 30px field box and 13px value
  style. Floating labels render at 11px/500; labels may not change the field
  height. Helper and validation text live below the field instead of stretching
  one control in a row.
- Focus-visible and selection are separate states. Keyboard focus must not look
  like the currently selected resource.

## Evidence and identifiers

- Show Agent, Workspace, Environment, Endpoint, Account display name, state,
  and recent activity first.
- Exact IDs remain available in a tooltip, copy action, or expanded evidence.
  Never lead with a long technical ID when a display name exists.
- Do not repeat the same Environment/Route/Account summary in a collapsed Turn
  header and again immediately inside its expanded evidence.
- Evidence paths are left-aligned reading aids, not centered decorative flow
  charts. The Turn timeline spine is the primary visual signature.

## Empty and loading states

- Empty states use a 20px Material outlined symbol, an 11px title, optional
  supporting text, and at most one compact action.
- Supporting copy is earned: omit it when the title and action already explain
  the state. Never repeat ownership rules or architecture notes in an empty
  state.
- Avoid novelty illustrations and oversized icon-plus-slogan compositions.
- Loading must preserve the surrounding layout and must never be used as visual
  evidence in screenshots.

## Conversation behavior

- The newest Turn is expanded on first entry. Expanding another Turn forms a
  single-open accordion.
- The whole Turn header is interactive, exposes expanded/collapsed semantics,
  and has a clear chevron and hover/focus response.
- A Turn keeps the same rounded silhouette when collapsed and expanded. Its
  timeline node aligns to the optical center of the first header row, not to
  the card's top border.
- New Turns auto-follow only while the reader remains near the bottom.
- Captures is the operational authority; Conversations is the cross-Capture
  derived audit index. Both reuse the same canonical timeline renderer and
  provide a clear route back to Capture context.

## Acceptance views

Every visual change is checked in a real packaged macOS App at desktop and
390px widths, in English and Simplified Chinese, with light and dark themes.
The desktop fixture includes 7-8 running Agents, history, long IDs, hundreds of
Turns, an empty state, a menu, a dialog, and keyboard focus.
