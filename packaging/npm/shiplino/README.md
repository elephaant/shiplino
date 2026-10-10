# shiplino

The flight recorder and kanban board for your AI coding agents (Claude Code, Codex, Cursor and more). See the [project page](https://github.com/elephaant/shiplino) for what it does.

```bash
npm install -g shiplino
shiplino setup
```

`shiplino setup` connects the agents it finds, starts the background service and serves the board at http://localhost:4777.

## How this package works

It contains no install scripts. npm installs one of the platform packages below (an optional dependency, chosen by your OS and CPU); it holds the release binary from [GitHub Releases](https://github.com/elephaant/shiplino/releases), unchanged. Before publishing, the release workflow verifies each archive against the release's `checksums.txt` and its Sigstore signature, and the packages are published with npm provenance.

| Package | Platform |
|---------|----------|
| `@shiplino/darwin-arm64` | macOS, Apple silicon |
| `@shiplino/darwin-x64` | macOS, Intel |
| `@shiplino/linux-arm64` | Linux, arm64 |
| `@shiplino/linux-x64` | Linux, x64 |
| `@shiplino/win32-arm64` | Windows, arm64 |
| `@shiplino/win32-x64` | Windows, x64 |

## Updating

```bash
npm install -g shiplino@latest
shiplino setup
```

`shiplino update` knows it was installed by npm and prints this command instead of replacing the binary. Run `shiplino setup` after upgrading so your agents' hooks use the new version.

License: Apache-2.0.
