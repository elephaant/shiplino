# SDKs

Report sessions, turns, tool calls and token usage from your own agents (Claude Agent SDK, OpenAI Agents SDK, LangGraph, plain code) to the local Shiplino daemon. They show up on the board like any other agent.

- [`ts/`](ts/): `@shiplino/sdk` for Node 20+, no runtime dependencies
- [`python/`](python/): `shiplino` for Python 3.9+, standard library only

Both send [universal events](../docs/event-format.md) to the daemon's [ingest API](../docs/ingest.md). Neither is published to a package registry yet: install from this repository (see each README).
