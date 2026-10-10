# Updating Shiplino

```bash
shiplino update            # install the newest release
shiplino update --check    # only say whether there's one
shiplino update --rollback # go back to the version before the last update
```

`shiplino update` downloads the release for your OS and CPU from GitHub, verifies it, replaces `~/.shiplino/bin/shiplino` (the binary your agents' hooks run), checks that the new binary still passes the hook test (prints nothing, exits 0), and restarts the background service. The version it replaced is kept as `~/.shiplino/bin/shiplino.previous`, so `--rollback` can switch back at any time; a second `--rollback` undoes the first.

`--channel stable|prerelease` overrides the channel for one run. `--force` lets a development build (built from source, version `0.0.0-dev`) be replaced by a release.

## How a download is verified

The same checks as the [installer](../scripts/install.sh), a little stricter:

1. Only HTTPS, including every redirect.
2. The release's `checksums.txt` is signed with Sigstore by this repository's release workflow. If [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) is installed, the signature must verify for exactly the tag being installed (`https://github.com/elephaant/shiplino/.github/workflows/release.yml@refs/tags/<tag>`, issuer `https://token.actions.githubusercontent.com`), or nothing is installed. Without cosign the signature isn't checked and the update says so; set `require_signature = true` (below) to refuse updates then.
3. The archive must match its SHA-256 in `checksums.txt`.
4. Only the `shiplino` binary is read from the archive (an entry with exactly that name, a regular file). No other entry is written anywhere, so archive paths like `../x` can't escape.
5. The new binary must run here and report the version that was asked for.
6. The version must be newer than the one running. Updates never downgrade; only `--rollback` goes back, and only to the binary you had.

The checksum alone proves the download matches what was published on the release page. The signature also proves the release page itself was built by this repository's release workflow, so install cosign if you can.

## Replacing a running binary

Hooks start the binary many times a minute, so it's never half-written:

- **macOS and Linux:** the new binary is written next to the old one and renamed over it in one atomic step. A hook starting at any moment runs either the old or the new version.
- **Windows** can't overwrite a running `.exe`, but it can rename it. The old binary is renamed to `shiplino.previous.exe` and the new one renamed into place. For a few microseconds between the two renames the path doesn't exist; a hook starting exactly then fails, which agents treat as a non-blocking hook error. An old previous binary that is still running is renamed aside and deleted by the next update.

The background service (systemd user service, LaunchAgent, Task Scheduler task or XDG autostart) is then restarted. If you run `shiplino daemon` yourself, restart it.

## Package managers

If the binary you run was installed by Homebrew, Scoop, winget, Nix or Snap, `shiplino update` refuses and prints that manager's command instead (for example `brew upgrade shiplino`). Run `shiplino setup` after upgrading so the hooks use the new version.

## Checking automatically (opt-in)

An update check is a request to `api.github.com` (it sends the usual request details: your IP address and a `User-Agent: shiplino/<version>` header; nothing about your sessions). Shiplino makes no network calls you didn't turn on, so checks are off by default. In `~/.shiplino/config.toml`:

```toml
[update]
check = false            # true: look for a new release once a day
auto_install = false     # true: also install it and restart the daemon (implies check)
channel = ""             # "stable", "prerelease", or "" to follow the installed version
require_signature = false
```

With `check = true` the daemon checks a minute after it starts and then once a day (restarts don't reset the clock). A newer release shows up in `shiplino doctor`, `shiplino status` and a banner in Settings. Nothing is installed.

With `auto_install = true` the daemon installs the release itself, with the same verification, and restarts into it. It only does this when it runs `~/.shiplino/bin/shiplino` and no package manager owns it. If you roll back, that version isn't installed automatically again; a newer one is.

`shiplino update` and `shiplino update --check` work whatever these settings say, because you asked.

## Channels

Releases are either stable (`v1.2.0`) or prereleases (`v1.2.0-alpha.1`, `-beta`, `-rc`). The default channel, `""`, follows what you have installed: a prerelease build gets the newest release including prereleases, and a stable build gets stable releases only. So while Shiplino is in alpha you get each alpha, and once you're on a stable release you stay on stable releases. Set `channel = "stable"` or `"prerelease"` to choose yourself.

## Files

| File | What |
|------|------|
| `~/.shiplino/bin/shiplino` | the binary hooks and the service run |
| `~/.shiplino/bin/shiplino.previous` | the version before the last update, for `--rollback` |
| `~/.shiplino/update.json` | the last check: when, the newest release, any error. Local only |

Nothing from an update check is stored in the database or synced.
