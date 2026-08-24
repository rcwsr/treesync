# treesync

Continuously mirror a git worktree's live state (tracked + uncommitted + untracked
files, excluding gitignored) into another worktree in real time — similar to termic's
"Spotlight" feature. Intended for the case where an AI coding agent works in an isolated
worktree and you want to see/run that work from your main checkout (e.g. a dev server or
editor rooted at the repo root) without ever touching that checkout's real branch.

The target worktree is switched to a detached HEAD before syncing begins, so nothing is
ever committed to or mutated on its real branch; stopping the watch restores the
target's original checkout.

## Installation

```sh
# Homebrew (macOS/Linux)
brew install rcwsr/tap/treesync

# go install
go install github.com/rcwsr-dev/treesync/cmd/treesync@latest

# from source
git clone https://github.com/rcwsr-dev/treesync
cd treesync && go build -o treesync ./cmd/treesync
```

Or download a prebuilt binary from the [releases page](https://github.com/rcwsr-dev/treesync/releases).

## Usage

```sh
# from inside the agent's worktree, sync into the repo's main checkout:
treesync watch

# or specify source/target explicitly:
treesync watch <source> <target>

# one-shot sync instead of continuous watching:
treesync sync [source] [target]

# stop a running watch (also restores the target):
treesync stop [target]

# check whether a watch is running for a target:
treesync status [target]
```

`source` defaults to the worktree containing the current directory; `target` defaults to
the repository's main worktree. Both can be overridden by passing them explicitly.

Flags on `watch`/`sync`: `--debounce-ms` (default 200), `--force` (allow watching into a
target with uncommitted changes by auto-stashing them first), `--log-level`
(debug/info/warn/error, `watch` only).

## Safety

- Sync is one-way: source → target. Editing the target while sync is running will be
  overwritten by the next sync cycle — don't do it.
- `watch` refuses to start if the target has uncommitted changes, unless `--force` is
  passed.
- If the process is killed uncleanly (e.g. `kill -9`, a crash) while the target is
  detached, the next `treesync watch` invocation for that target automatically restores
  it before starting a new session.

## Development

```sh
go build ./...
go vet ./...
go test ./...
```

`go test ./...` includes an integration suite (`test/integration`) that exercises real
temporary git worktrees end to end, including crash recovery.
