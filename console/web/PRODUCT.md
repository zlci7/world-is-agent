# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Players connecting their local games to World Is Agent, people operating a local Runtime, and invitation-beta administrators.

## Product Purpose

Connect game characters to contextual dialogue, persistent experiences, and scheduled interactions. The console presents connection state and recorded task outcomes.

## Operating Context

The local console belongs to one local Runtime. Hosted access uses invitation-based email and password accounts, personal WIA device keys, and server-owned user identities. Games and actions execute on players' computers.

## Capabilities and Constraints

The hosted development scope is Windows Stardew Valley, one active device per account, a platform-managed model, independent account data, task views, recent turns, explicit preferences, and usage limits. Accounts use verified-email invitation registration, mailbox verification for existing users and one-use password recovery. The separate `/admin` website uses administrator-scoped login, invitation management, user availability and daily-limit overrides, read-only runtime state, whole-service usage audit and bounded CSV export. Private commands initialize or grant administrators. Deployment, real mailbox delivery and real-game acceptance are recorded separately from source implementation.

## Brand Commitments

World Is Agent / WIA. Chinese interface. The local console and hosted player website preserve the pixel farm scenery, square window frames, warm palette and readable window opacity in DESIGN.md. The separate administrator website uses a plain light background, opaque surfaces, compact tables and blue actions, with its own scoped design documentation. Alignment and readable states apply to both.

## Product Principles

- Report connection, world readiness, and task outcomes separately.
- Derive data ownership from authenticated server identities.
- Keep player settings distinct from NPC definitions and confirmed game facts.
- Present errors and recovery actions near the operation that failed.
