# Documentation

This directory contains current status documents, architecture notes, ADRs, phase plans, acceptance records, and exploratory design material.

## Public Sources Of Truth

Start here when you want the current repository state:

- [Project README](../README.md)
- [Architecture](../ARCHITECTURE.md)
- [Status](STATUS.md)
- [Development Guide](development/guide.md)
- [Testing Guide](development/testing.md)

## Architecture And Decisions

- [Runtime Architecture Baseline](<summary/GameAgent Runtime 整体架构设计规范.md>)
- [Multi-game Compatibility and Agent Binding](<summary/GameAgent 多游戏兼容性与 Agent Binding 决策.md>)
- [Phase6 Async Action Protocol Strategy ADR](<phase06/GameAgent MVP0 Phase6 Async Action Protocol Strategy ADR.md>)

These documents preserve detailed architectural reasoning and constraints. Use the public sources above for the current GitHub-facing summary.

## Phase Plans And Acceptance Records

Each phase has its own directory, and sub-phases live inside their parent phase (`Phase5.5` and `Phase5.6` under `phase05`, `Phase6.5` and `Phase6.6` under `phase06`). Phase documents are implementation planning and validation records:

- [Phase1](phase01/)
- [Phase2](phase02/)
- [Phase3](phase03/)
- [Phase4](phase04/)
- [Phase5](phase05/)
- [Phase6](phase06/)
- [Phase7](phase07/)
- [Phase8](phase08/)
- [Phase9](phase09/)

[docs/STATUS.md](STATUS.md) is the public source of truth for current capability status and known limits. Phase documents can contain internal terminology, temporary implementation plans, and historical acceptance details, including references to documents that no longer exist in the repository; those records are kept as written.

## Exploratory Notes

Exploratory documents may contain older names, open questions, or draft ideas:

- [Context Architecture](<summary/Context/Context架构设计.md>)
- [Compatibility Discussion](兼容性探讨.md)
- [Adapter Notes](adapter/)
- [Archive](archive/)
- [Project Progress Notes](pro/)

Treat these as design notes unless a public source of truth links to them as a current decision.
