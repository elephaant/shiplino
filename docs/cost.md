# How Shiplino calculates cost

Shiplino shows what each agent session **would cost at the provider's public API list prices**. It never calls a model and never sees your bill. This page explains where the numbers come from and how far to trust them.

## Two sources, best one wins

| Source | What it is | Covers | Granularity |
|--------|-----------|--------|-------------|
| **Computed** | Tokens from the agent's transcript × the bundled price table (`pkg/pricing/prices.json`) | Every model response the agent writes to its transcript, including subagents | Per response, live |
| **Reported** | The agent's own cost accounting, when it writes one (Claude Code records a running total per process) | Everything the agent paid for, including calls that never appear in the transcript | Periodic |

Each session shows the **reported** figure when the agent provides one, and otherwise the **computed** one. The UI says which (`cost_source`). Per-response computed costs stay available for timelines and per-model breakdowns.

### Why both

Transcripts don't contain every API call an agent makes. Claude Code, for example, makes background calls (such as summarizing fetched web pages with a small model) and pays web-search fees that don't show up as transcript responses. On real sessions, transcript-only cost came out between **2% and 33% lower** than Claude Code's own total. With the reported figure, Shiplino matches the agent exactly.

## The computed formula

For each model response, counted **once** (some agents write one response across several transcript lines that repeat the same usage, so responses are keyed by their id):

```
cost = ( input        × input rate
       + output       × output rate
       + cache reads  × cache-read rate
       + cache writes (5-minute) × 1.25 × input rate
       + cache writes (1-hour)   × 2   × input rate ) / 1,000,000
       × fast-mode multiplier     (if the response used fast mode)
       × US-inference multiplier  (1.1, if inference_geo was "us" on models that charge it)
     + web searches × $10 / 1,000
```

Details that matter:
- **Cache writes come in two prices.** The usage object says how many tokens went to the 5-minute vs the 1-hour cache. Claude Code often uses the 1-hour cache, so assuming the 5-minute price would undercount.
- **Cache reads are priced per model.** The ratio to the input price is not the same for every model.
- **Some models have a long-context tier.** Example: Claude Haiku 5.5 charges higher rates for the whole request when its prompt, cache tokens included, is over 100,000 tokens.
- **Model ids** are matched through provider prefixes, date suffixes and variant tags (`us.anthropic.…`, `…-20251001`, `…[1m]`).
- **Unknown models** are shown as *unpriced* rather than guessed.
- **Announced price changes are dated.** A model can list rates that apply from a given day (00:00 UTC). Each response is priced at the rates in effect when it was made, so history keeps its old price after a change.

## How accurate is it?

- **Token counts are exact:** they're what the provider reported for each response.
- **The dollar figure is an estimate of list-price cost**, accurate when all of these hold:
  - you pay per token at standard API rates (direct API key);
  - the price table is current (it's bundled with each release and records the date it was checked);
  - the agent writes complete transcripts (or a reported total).
- **It is not your bill.** Things Shiplino can't see:
  - **Subscription plans** (e.g. Claude Pro/Max) don't charge per token. The figure is an *API-equivalent* cost, useful for comparing work, not money you spent.
  - **Negotiated discounts, credits, taxes.**
  - **Cloud marketplaces** (Amazon Bedrock, Google Vertex AI) have their own prices.
  - Calls the agent made that appear in neither its transcript nor its own report.

For billing, the source of truth is your provider's console or usage and cost API.

## Codex

- **Tokens** come from Codex's own `token_usage_record` lines in its rollout files, one per API response. Shiplino counts each `response_id` once. OpenAI includes cached tokens in `input_tokens`, so Shiplino stores the uncached part as input and the cached part as cache reads.
- Summed this way, the totals match Codex's own running `thread_token_usage` for the thread. The exception is a thread you rewound: Codex's total then drops the abandoned branch, but those calls were made, so Shiplino keeps them.
- **Cost:** tokens × OpenAI's list prices (gpt-5.6-terra, gpt-5.6-luna, gpt-5.5, gpt-5.3-codex; checked 2026-10-10). A response whose prompt is over 272K tokens uses the long-context rates. Fast (priority) and flex tiers use their multipliers when Codex records the service tier. Models OpenAI doesn't list (e.g. `codex-auto-review`) stay unpriced.
- **On a ChatGPT plan, Codex isn't billed per token.** The figure is then an API-equivalent cost: useful for comparing work, not money you spent.

## Gemini CLI

- **Tokens** come from the `tokens` of each response in Gemini CLI's chat recordings, one per API response, counted once per message id. The Gemini API includes cached tokens in its input count, so Shiplino stores the uncached part as input and the cached part as cache reads. Thinking tokens are billed as output and tool-use prompt tokens as input.
- **Cost:** tokens × the Gemini API's paid-tier Standard list prices (checked 2026-10-10). A prompt over 200K tokens uses the long-context rates on models that have them. Gemini 3.6 and 3.8 Flash list higher rates from 2027-01-01; responses from that day on use them.
- **With a Google sign-in (free tier or a Code Assist plan), Gemini CLI isn't billed per token.** The figure is then an API-equivalent cost.

## Cursor

Cursor's hooks carry no documented token counts. When its `afterAgentResponse` hook includes them (interactive sessions), Shiplino records them and prices them from the bundled table. Otherwise a Cursor session shows activity but no tokens or cost. Cursor's own usage dashboard is the source for what you were charged.

## Aider (via `shiplino wrap`)

Aider writes a usage line to its chat history after each response: `Tokens: 2.1k sent, 512 received. Cost: $0.01 message, $0.05 session.` Shiplino uses Aider's own **session total** as the reported cost. Aider rounds token counts above 1,000 (`2.1k`), so the token figures are approximate (`tokens_rounded`). Cached prompt tokens are stored as cache reads/writes, not input. When Aider doesn't know a model's price it prints no cost, and the session shows tokens only.

## How others do it

- **Transcript-only tools** sum transcript usage × a price table. They're simple and per-response, but they miss background calls and fees, as measured above.
- **The agent's built-in cost view** (e.g. Claude Code's session cost) uses its own accounting. It's complete for that agent, but it's session-level and only covers that one agent.
- **OpenTelemetry export** (where the agent supports it) sends per-request cost and token metrics. It's accurate and live, but it needs telemetry turned on in the agent's environment. Shiplino's OTLP receiver can take these in.
- **The provider's billing API** is authoritative, but organization-level and delayed, with no per-session or per-task breakdown.

Shiplino combines the first two automatically, with no setup, and keeps the source visible.
