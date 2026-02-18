# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Current State

Design phase. No source code yet. Key design documents:

| File | Description |
|---|---|
| `ARCHITECTURE.md` | Full system architecture, philosophy, data flow, risk register |
| `docs/mvp-roles-v0.1.md` | MVP role definitions v0.1 (superseded) |
| `docs/mvp-roles-v0.2.md` | MVP role definitions v0.2 (superseded) |
| `docs/mvp-roles-v0.3.md` | MVP role definitions v0.3 (superseded) |
| `docs/mvp-roles-v0.4.md` | MVP role definitions v0.4 (superseded) |
| `docs/mvp-roles-v0.5.md` | MVP role definitions v0.5 — current |

## Environment Configuration

Two env files are present:

- `.env` — points to a Volcengine/Ark LLM endpoint (`ark.cn-beijing.volces.com`)
- `.env.ds` — points to DeepSeek API (`api.deepseek.com`)

Both follow the OpenAI-compatible API convention with `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL`. Copy whichever is relevant to `.env` when starting development, or set the variables directly in your shell.
