---
name: zero-token-check
description: Verify Shiplino's zero-token contract, i.e. that `shiplino hook` prints nothing, always exits 0, makes no network calls, and stays under the time budget, and that adapters register only command-type hooks. Use after touching cmd/shiplino, internal/shim, internal/spool, adapter installers or plugins, or when the user asks "does this cost tokens?".
---

# Zero-token check

Rules: `.claude/rules/zero-token-contract.md`.

## 1. Build

```bash
make build
```

## 2. Output and exit code (must print only `exit=0` for every case)

```bash
export HOME="$(mktemp -d)"   # never touch the real home
B=./bin/shiplino
for agent in claude-code codex cursor gemini-cli copilot-cli windsurf cline opencode; do
  for input in '{}' '{"session_id":"t1","hook_event_name":"PreToolUse"}' 'not json' ''; do
    out=$(printf '%s' "$input" | $B hook --agent "$agent" 2>&1); code=$?
    [ -n "$out" ] || [ $code -ne 0 ] && echo "FAIL agent=$agent input=$input code=$code out=$out"
  done
done
head -c 10000000 /dev/urandom | $B hook --agent claude-code >/dev/null 2>&1; echo "big input exit=$?"
HOME=/nonexistent $B hook --agent claude-code </dev/null; echo "no home exit=$?"
```

## 3. Static checks

```bash
go list -deps ./internal/shim | grep -E 'net/http|database/sql|sqlite|toml|log/slog' && echo "FAIL: heavy import in shim"
grep -rn 'fmt.Print\|os.Stdout\|os.Stderr\|log\.' internal/shim/ && echo "REVIEW: possible output in shim"
grep -rn '"type": *"\(prompt\|agent\)"\|failClosed' pkg/adapters/ && echo "FAIL: non-command hook or failClosed"
```

## 4. Timing

```bash
hyperfine -N --warmup 20 "sh -c 'echo {} | ./bin/shiplino hook --agent claude-code'"
```
Target: p50 < 3 ms, p99 < 8 ms.

## 5. Contract test

```bash
go test ./internal/shim/ -run Contract -v
```

Report pass/fail per section with the exact failing output.
