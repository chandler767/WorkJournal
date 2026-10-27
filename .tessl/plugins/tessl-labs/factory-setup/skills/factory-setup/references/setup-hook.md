# Writing the setup hook

`.tessl/cloud-launch-setup.sh` runs from the clone root before every cloud
run.

- **Starts with `set -eo pipefail`.** Installers are often pipelines
  (`curl ... | bash`), and `set -e` alone passes when the download fails. Leave
  out `-u`; `nvm.sh` is not safe under it.
- **Echoes each step before running it.** A failing run's log then shows which
  step broke.
- **Only what a cloud run needs to build and test.** Leave out CI-only steps
  such as browser or Docker downloads, unless the tests need them.
- **No root, no build tools.** The cloud machine can't install system
  packages. If a dependency builds from source, prefer its prebuilt binary. If
  that means changing the project's build config (for example `allowBuilds` in
  `pnpm-workspace.yaml`), ask the user first.
- **Installs land in `~/.local/bin`.** The hook runs in a child process, so
  `export PATH=...`, `nvm use`, `fnm use`, or sourcing a profile does not reach
  the agent. Put anything you install in `~/.local/bin`, or link it there.

## Versions

Accept a compatible range, never the exact version CI pins.

1. Read the range from the repository's own declarations: `engines`,
   `.nvmrc`, `.node-version`, `.tool-versions`, `packageManager`,
   `rust-toolchain.toml`, `go.mod`, `.python-version`. Treat the version in a
   CI setup action as a hint for the major version, not a requirement for the
   patch.
2. Keep both bounds a range states. `engines: ">=20 <22"` or `^20` excludes
   Node 22 as surely as Node 18. A bare major version, such as `24` in
   `.nvmrc`, or a CI version with no declaration behind it, is a floor: accept
   that major or newer. An exact version in a version file, such as `24.18.1`
   in `.nvmrc` or `3.12.4` in `.python-version`, is a hint like CI's: accept
   the same major (the same minor for Python).
3. Check the tool already on the machine first. Use it when it satisfies the
   range, for example any Node 24 or newer when the repository asks for 24.
   Install only when the tool is missing or outside the range.
4. Prefer the repository's own package-manager pin over a global install. When
   `packageManager` is set, `export COREPACK_ENABLE_DOWNLOAD_PROMPT=0`, then
   run `corepack enable --install-directory ~/.local/bin`. Node 25 and later
   don't bundle Corepack, so if `corepack` is missing, install it first with
   `npm install -g --prefix ~/.local corepack`.
5. Fail with a message naming the tool, the range, and the version found when
   neither the installed tool nor an install satisfies the range.
