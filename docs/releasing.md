# Releasing

For maintainers. Pushing a tag `vX.Y.Z` (or `vX.Y.Z-alpha.N`) runs [release.yml](../.github/workflows/release.yml):

1. Tests, then the web app is built so it's embedded in the binary.
2. [GoReleaser](../.goreleaser.yaml) builds six archives (macOS, Linux, Windows; amd64 and arm64), writes `checksums.txt`, signs it with Sigstore (keyless, bound to this workflow), and creates the GitHub release.
3. It generates a Homebrew cask (`Casks/shiplino.rb`) and a Scoop manifest (`shiplino.json`) and pushes them to `elephaant/homebrew-tap` and `elephaant/scoop-bucket`, **only if the `HOMEBREW_TAP_TOKEN` secret is set**. Without it they're kept as the run's `package-manifests` artifact and the release still succeeds.
4. [npm.yml](../.github/workflows/npm.yml) downloads the release, verifies the Sigstore signature of `checksums.txt` and every archive's SHA-256, builds the `shiplino` package and six platform packages (`@shiplino/<os>-<cpu>`, each holding that platform's binary unchanged) and publishes them with npm provenance, **only if the `NPM_TOKEN` secret is set** (or trusted publishing is on, below). Otherwise the job notes that it skipped and succeeds.

Prereleases are published everywhere too. On npm they get the `latest` tag until a stable version exists, then `next`.

## One-time setup

Nothing here is needed for GitHub releases or the install scripts. Each step turns on one more channel.

### Homebrew and Scoop

1. Create two **public** repositories in the `elephaant` organization, each with a README so the default branch (`main`) exists:
   - `elephaant/homebrew-tap` (Homebrew finds it as `elephaant/tap`)
   - `elephaant/scoop-bucket`
2. Create a **fine-grained personal access token** (GitHub → Settings → Developer settings → Personal access tokens → Fine-grained tokens):
   - Resource owner: `elephaant` (if the organization requires approval for fine-grained tokens, approve it in the organization's settings)
   - Repository access: only `elephaant/homebrew-tap` and `elephaant/scoop-bucket`
   - Permissions: **Contents: Read and write** (Metadata: read-only is added automatically). Nothing else.
   - An expiry date you'll be reminded of; the release keeps working without it, it just stops updating the tap and bucket.
3. In `elephaant/shiplino` → Settings → Secrets and variables → Actions, add it as the repository secret **`HOMEBREW_TAP_TOKEN`**.

The next release pushes the cask and the manifest. Users then install with:

```bash
brew install --cask elephaant/tap/shiplino
scoop bucket add elephaant https://github.com/elephaant/scoop-bucket
scoop install elephaant/shiplino
```

The binaries aren't notarized by Apple, so the cask removes the macOS quarantine flag after install (Homebrew has already checked the SHA-256). To publish a release made before the token was set, commit the files from that run's `package-manifests` artifact to the two repositories by hand (`Casks/shiplino.rb` in the tap, `shiplino.json` at the root of the bucket).

### npm

1. Check that the package name `shiplino` is still free (`npm view shiplino` returns a 404), and create the free **npm organization `shiplino`**, which owns the `@shiplino/*` platform packages.
2. Create a **granular access token** on npmjs.com (Access Tokens → Generate New Token → Granular): read and write for packages, scoped to all packages of your account and the `shiplino` organization (the packages don't exist yet, so they can't be selected one by one). If your account requires two-factor authentication for publishing, allow the token to bypass it, since CI can't type a code.
3. Add it as the repository secret **`NPM_TOKEN`**.
4. To publish a release that already exists: Actions → npm → Run workflow, with the tag (for example `v0.1.0-alpha.4`). Versions already on npm are skipped, so it's safe to run again.

After the first publish, switch to token-free [trusted publishing](https://docs.npmjs.com/trusted-publishers) (npm checks the GitHub Actions identity instead of a stored token):

1. On npmjs.com, for each of the seven packages (`shiplino`, `@shiplino/darwin-arm64`, `@shiplino/darwin-x64`, `@shiplino/linux-arm64`, `@shiplino/linux-x64`, `@shiplino/win32-arm64`, `@shiplino/win32-x64`): Settings → Trusted publishing → GitHub Actions, organization `elephaant`, repository `shiplino`, workflow `release.yml`. Add a second entry with workflow `npm.yml` if you want manual runs to work too.
2. Add the repository **variable** (not secret) `NPM_TRUSTED_PUBLISHING` = `true`.
3. Delete the `NPM_TOKEN` secret and the token, and set each package's publishing access to require two-factor authentication and disallow tokens.

## Checking a release config locally

```bash
goreleaser check
goreleaser release --snapshot --clean --skip=sign   # writes dist/, publishes nothing
packaging/npm/test.sh                               # npm packages from fake archives (Linux x64)
```

The snapshot writes the cask to `dist/homebrew/Casks/shiplino.rb` and the manifest to `dist/scoop/shiplino.json`.
