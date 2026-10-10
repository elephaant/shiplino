# How Shiplino calculates cost

Shiplino shows what each agent session **would cost at the provider's public API list prices**. It never calls a model and never sees your bill. This page explains where the numbers come from and how far to trust them.

## Two sources, best one wins

| Source | What it is | Covers | Granularity |
|--------|-----------|--------|-------------|
| **Computed** | Tokens from the agent's transcript × the bundled price table (`pkg/pricing/prices.json`) | Every model response the agent writes to its transcript, including subagents | Per response, live |
| **Reported** | The agent's own cost accounting, when it writes one (Claude Code records a running total per process, and can also export per-request cost over [OpenTelemetry](ingest.md)) | Everything the agent paid for, including calls that never appear in the transcript | Periodic |

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
- **No usage at all:** when a session (with its subagents) finished work without recording any token usage, because the agent's hooks or transcripts don't include it (e.g. Windsurf, Cursor transcripts, or a Copilot CLI session that is still running), its card says *no cost data* instead of showing $0, and Insights counts it separately. The session's `usage` field is `"none"` in that case and `"tokens"` once any usage arrives.

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
- **Sessions imported from another agent** (Codex Desktop can import Claude Code and Cursor history) are skipped: they copy that agent's messages and carry only a size estimate, not billed usage. The original agent's own record is used instead. `shiplino doctor` shows how many were skipped.
- **Cost:** tokens × OpenAI's list prices (gpt-5.6-terra, gpt-5.6-luna, gpt-5.5, gpt-5.3-codex; checked 2026-10-10). A response whose prompt is over 272K tokens uses the long-context rates. Fast (priority) and flex tiers use their multipliers when Codex records the service tier. Models OpenAI doesn't list (e.g. `codex-auto-review`) stay unpriced.
- **On a ChatGPT plan, Codex isn't billed per token.** The figure is then an API-equivalent cost: useful for comparing work, not money you spent.

## Gemini CLI

- **Tokens** come from the `tokens` of each response in Gemini CLI's chat recordings, one per API response, counted once per message id. The Gemini API includes cached tokens in its input count, so Shiplino stores the uncached part as input and the cached part as cache reads. Thinking tokens are billed as output and tool-use prompt tokens as input.
- **Cost:** tokens × the Gemini API's paid-tier Standard list prices (checked 2026-10-10). A prompt over 200K tokens uses the long-context rates on models that have them. Gemini 3.6 and 3.8 Flash list higher rates from 2027-01-01; responses from that day on use them.
- **With a Google sign-in (free tier or a Code Assist plan), Gemini CLI isn't billed per token.** The figure is then an API-equivalent cost.

## Cursor

Cursor's hooks carry no documented token counts. When its `afterAgentResponse` hook includes them (interactive sessions), Shiplino records them and prices them from the bundled table. Otherwise a Cursor session shows activity but no tokens or cost. Cursor's own usage dashboard is the source for what you were charged.

## Cline

Cline prices every model call itself, and Shiplino reads its numbers from Cline's own task files (`cost_source: reported`). Cline rewrites these files whole on every save, so Shiplino reads a changed file again (at most every 5 seconds) and counts each call once, keyed by the call.

- **SDK hosts** (the CLI, Kanban, the desktop app, the SDK build of the VS Code extension): `~/.cline/data/sessions/<id>/*.messages.json`. Each finished model call carries its tokens, model and cost. Cline counts cache reads and writes inside its input figure, so Shiplino stores the uncached part as input, as Cline's own telemetry does. Subagents have their own files and show as the session's subagents.
- **The classic extension** (VS Code and its forks, JetBrains): `tasks/<id>/ui_messages.json` in the editor's global storage (`saoudrizwan.claude-dev`) or in `~/.cline/data`. Each call's `api_req_started` message has its tokens and cost. Cline updates it while the response streams, so a call is counted once it's final: when the next call starts, when it was cancelled or failed, or when the file has been quiet for 2 minutes (so the last call of a task can show up to 2 minutes late).
- A classic task resumed in an SDK host copies its old totals into the new session file; those are skipped, since they were counted from the classic file.
- **When Cline's cost is $0** (a model it has no price for, or a subscription provider), the call is priced from Shiplino's table when the model is known (`computed`), and stays unpriced otherwise.

## GitHub Copilot CLI

Copilot CLI writes per-call usage only to subscribers of its live event stream. What it keeps on disk is its session log, `~/.copilot/session-state/<id>/events.jsonl`, and the log gets token totals only **when a session ends** (`session.shutdown`). So a running Copilot session shows no tokens; they appear when you exit it.

- Each shutdown has the session's running totals per model. A resumed session ends again later, so Shiplino counts each shutdown as the difference from the one before (if Copilot restarted its totals, as older versions do on resume, the new totals count whole).
- Copilot's input count includes cache reads and writes; Shiplino stores the uncached part as input. Reasoning tokens are part of the output.
- **Cost:** Copilot prices its calls in GitHub AI credits (`totalNanoAiu` per model; one credit is billed at $0.01), and that is the reported cost. When a model has no credits figure, its tokens are priced from Shiplino's table (Copilot's `claude-sonnet-4.5` is priced as `claude-sonnet-4-5`), or stay unpriced.
- Credits included in your plan aren't money you spent: like other plans, the figure is the list-price value of the usage.
- `shiplino doctor` notes that Copilot CLI's tokens arrive at session end.

## Aider (via `shiplino wrap`)

Aider writes a usage line to its chat history after each response: `Tokens: 2.1k sent, 512 received. Cost: $0.01 message, $0.05 session.` Shiplino uses Aider's own **session total** as the reported cost. Aider rounds token counts above 1,000 (`2.1k`), so the token figures are approximate (`tokens_rounded`). Cached prompt tokens are stored as cache reads/writes, not input. When Aider doesn't know a model's price it prints no cost, and the session shows tokens only.

## OpenCode

OpenCode prices every response itself, from its own model catalog, and Shiplino's plugin passes that through: each completed assistant message becomes one usage record with OpenCode's tokens and `cost_usd` (`cost_source: reported`). OpenCode reports input without cached tokens and output without reasoning tokens; Shiplino stores reasoning as output too (`reasoning_tokens` keeps the split). A session whose priced responses were all priced by OpenCode shows `cost_source: reported`. OpenCode reports $0 for models it has no price for and for some subscription providers; those responses are priced from Shiplino's table instead (API-equivalent), or stay unpriced, and the session then shows `computed`.

## Plan limits

On a flat-rate plan (Claude Pro/Max, ChatGPT plans for Codex) what matters is how much of your usage windows is left, not dollars. Shiplino shows each agent's windows and when they reset: in the header (`Codex 62% · resets 14:20`), on Insights, and at `GET /api/v1/limits`. Dollar figures for these agents are labeled **API-equivalent**.

| Agent | What it records | Shiplino shows |
|-------|-----------------|----------------|
| **Codex** | `rate_limits` in the `token_count` lines of its rollouts: used percent, window length and reset time of each window (usually 5 hours and weekly), and the plan | The agent's numbers (`source: reported`) |
| **Claude Code** | Only when a request is refused at a limit: a `rate_limit` error line with the window (`five_hour`, `seven_day`) and its reset time | "Limit reached · resets 14:20" (reported) |
| Claude Code, with the status line wrapper (opt-in) | `rate_limits` in its [status line](https://code.claude.com/docs/en/statusline) input: used percent and reset time of the 5-hour and weekly windows (Pro and Max plans) | The agent's numbers (`source: reported`) |
| Claude Code, otherwise | Nothing on disk. Claude Code passes the used percentages only to status line commands | An **estimate**: tokens used in the current 5-hour window and the last 7 days, with no percentage |

Notes:
- **The agent's numbers win.** Estimates are shown only for an agent on a plan that reports no percentages, and only for windows it reports nothing about.
- **Estimates have no percentage.** Plan quotas aren't published and change, so Shiplino doesn't guess them. A 5-hour estimate starts at the hour (UTC) of the first response after the previous window ended, as other usage tools count it; the real window may differ. The weekly estimate is a rolling 7 days, since the real week starts at a time only the provider knows.
- **Which agents are on a plan:** an agent counts as on a plan once it reports a limit (Codex does whenever you're signed in with ChatGPT). Set it yourself in `~/.shiplino/config.toml`; `api` hides an agent's windows:

  ```toml
  [limits.plans]
  claude-code = "plan"   # or "api"
  ```
- **Alerts:** a desktop notification once per window when an agent reports `limit_percent` or more used (default 80; `0` turns it off). Estimates never notify.
- **A window is dropped once it resets**, as the agents themselves do, until the agent reports the new one.
- **Privacy:** limit events hold only numbers, window names and times. They sync (if you turn sync on) like token counts.

### Claude Code's percentages: the status line wrapper

Claude Code passes the used percentage of each plan window only to [status line](https://code.claude.com/docs/en/statusline) commands, never to hooks or transcripts. To get them, turn on the wrapper (it's off by default):

```bash
shiplino setup --statusline        # or Settings → Agents → Claude Code status line → Turn on
shiplino setup --no-statusline     # put your own status line back
```

It changes one value in `~/.claude/settings.json`, after a backup, and shows the diff first (`--dry-run`, or the dialog on the Settings page):

```diff
   "statusLine": {
     "type": "command",
-    "command": "~/.claude/statusline.sh",
+    "command": "~/.shiplino/bin/shiplino statusline --wrap fi8uY2xhdWRlL3N0YXR1c2xpbmUuc2g",
     "padding": 2
   }
```

- **What you see stays the same.** `shiplino statusline` reads the input Claude Code sends, then runs your own command (decoded from `--wrap`) with the same input in the same shell Claude Code uses, and passes its output, errors and exit code through unchanged. It adds a few milliseconds. If anything on Shiplino's side fails, your command still runs.
- **No status line of your own?** The wrapper prints nothing. Claude Code still counts it as a status line, so it hides its footer hints (such as `esc to interrupt`) while one is set. `shiplino setup --statusline=minimal` shows a short line instead: `5h 23% · resets 14:00 · 7d 41%`.
- **What's recorded:** only the session id, Claude Code's version, the model id and the `five_hour` / `seven_day` windows (percent and reset time), appended to the spool like a hook event. Not the rest of the input (folder, cost, transcript path). Nothing at all on API billing, where Claude Code sends no windows. Pausing recording pauses this too.
- **Zero tokens.** A status line runs in Claude Code's interface, not the model: its output is shown, never sent to the model.
- **Undo is exact.** Your original command is stored in the wrapper's own `--wrap` argument, so `shiplino setup --no-statusline`, `shiplino uninstall` and the Settings page put back exactly the command you had, even without Shiplino's own files. If you had none, the `statusLine` entry is removed. If you later change your status line yourself (for example with `/statusline`), Shiplino leaves your new one alone.
- `shiplino doctor` shows whether the wrapper is on and what it wraps.

## How others do it

- **Transcript-only tools** sum transcript usage × a price table. They're simple and per-response, but they miss background calls and fees, as measured above.
- **The agent's built-in cost view** (e.g. Claude Code's session cost) uses its own accounting. It's complete for that agent, but it's session-level and only covers that one agent.
- **OpenTelemetry export** (where the agent supports it) sends per-request cost and token metrics. It's accurate and live, but it needs telemetry turned on in the agent's environment. Shiplino's OTLP receiver takes these in and compares them with the transcript figures, never adding the two (see [Sending events to Shiplino](ingest.md#how-telemetry-and-transcripts-fit-together)).
- **The provider's billing API** is authoritative, but organization-level and delayed, with no per-session or per-task breakdown.

Shiplino combines the first two automatically, with no setup, and keeps the source visible.
