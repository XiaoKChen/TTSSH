# ttssh

An interactive SSH manager for the terminal. A keyboard-driven dashboard lists
your recent connections; pick an SSH key — from a local folder or straight out
of the encrypted key vault — enter the target, then SSH in or copy files
to/from the remote host without leaving the dashboard.

Works on Windows, macOS, and Linux. The terminal UI is built into the binary,
so the only external requirement is the standard `ssh`/`scp` client
(preinstalled on macOS/Linux, and included with Windows 10+ as the built-in
OpenSSH client).

## Usage

```
ttssh
```

`ttssh` opens a full-screen dashboard:

- **Header** — the version, the vault state (`☁ vault` or `vault off`), and the
  active key folder.
- **Connections** (left) — your 10 most recent connections, most recent first,
  with the key name (`☁ unit` for vault keys) and how long ago you used it.
  Press `/` to filter.
- **Details** (right) — the selected connection's target, key, last use, and
  the actions available. Hidden when the terminal is narrower than 80 columns.
- **Status line** — results and warnings (`✓` success, `!` warning, `✗` error),
  cleared after a few seconds, and a spinner while ttssh waits on the vault or
  the remote host.
- **Footer** — the keys that work on the current screen; `?` shows them all.

From the dashboard:

- **enter** — SSH into the selected host (`ssh -i <key> user@host`). The
  dashboard steps aside while the session runs and comes back when it ends.
- **u** — copy a file TO the host: enter a local folder (default `.`), filter
  its files, enter the remote destination (default `~/`), and ttssh runs `scp`.
- **d** — copy a file FROM the host: enter a remote folder (default `~`); ttssh
  lists its files over SSH, you filter and pick one, then enter the local
  destination (default `.`). If the remote can't be listed (or would need a
  password/passphrase prompt), you type the remote path instead.
- **n** — set up a new connection:
  - **Pick a key** — vault keys (when the vault is configured) and the `*.key`
    files in your key folder (default `~/.ssh`); the details pane shows each
    key's path, size, and modified time, or its vault fingerprint and status.
    Press `f` to switch to another folder (and optionally save it as the
    default), or `p` to download vault keys into a folder.
  - **Enter the target** — username and IP/hostname. The connection is added
    to the top of the list, ready for enter/u/d.
- **x** — remove the selected connection from the list (asks y/n first).
- **X** — clear every recent connection (asks y/n first).

`esc` clears an active filter or goes back one step, from any screen. Recent
entries whose key file has been deleted are pruned automatically;
`ttssh clear-recents` also forgets the whole list from the command line.

### Keybindings

| Where | Key | Action |
|---|---|---|
| Everywhere | `↑`/`↓`, `k`/`j` | move |
| | `pgup`/`pgdn` | page |
| | `/` | filter the list (type to narrow, `enter` to apply) |
| | `esc` | clear the filter, or go back one screen |
| | `?` | toggle full help |
| | `q` | quit (`ctrl+c` quits even while typing) |
| Dashboard | `enter` | SSH into the selected connection |
| | `u` | upload a file to the host |
| | `d` | download a file from the host |
| | `n` | new connection |
| | `x` | remove the selected connection (`y` confirms) |
| | `X` | clear all recent connections (`y` confirms) |
| Key picker | `enter` | use the selected key |
| | `f` | change the key folder |
| | `p` | download vault keys to a folder |
| Folder browser | `enter` | open the selected folder |
| | `←`/`h`/`backspace` | parent folder (a drive list at a Windows drive root) |
| | `space` or `.` | use the current folder |
| | `t` | type a path (`~` is expanded) |

While a filter or a text field is focused, letter keys type text instead of
triggering actions.

## Key vault (Key-Upload-TUI database)

ttssh can read the Turso/libSQL database that Key-Upload-TUI uploads SSH keys
into, decrypting them client-side with the same v1 contract (HKDF-SHA256
per-unit key, AES-256-GCM, unit id as AAD). Nothing is ever uploaded — ttssh
is read-only against the vault.

When the vault is configured, the key picker lists every stored unit as a
`☁ unit-id` entry next to your local `*.key` files (with fingerprint, creation
date, and status in the details pane; revoked units are marked `(revoked)`). Picking one decrypts the key to a
private temp file used just for that session and deleted when ttssh exits.
Vault keys also work from the recents list — they are re-fetched on use, so
no key material is persisted between runs.

### Credentials

The vault needs the same three values as the uploader's `.env`:

- `DB_URL` — `libsql://…` or `https://…`
- `DB_TOKEN` — Turso auth token
- `MASTER_KEY_V1_HEX` — the 64-hex-char master key
- `DB_CA_CERT` — optional, path to a private CA root certificate

Each is resolved in this order: **environment variable** → **`.env` in the
current directory** (so running ttssh from the Key-Upload-TUI folder just
works) → **values saved by `ttssh vault setup`**.

`ttssh vault setup` prompts for the values, tests the connection, and stores
them in ttssh's `config.json`. Note the token and master key are stored there
in plain text — protect that file, or stick to environment variables.

### Commands

```
ttssh vault list                          list stored keys (unit, fingerprint, status)
ttssh vault pull                          interactive: multi-select keys, browse to a folder
ttssh vault pull -out D:\keys A6-001-ODU  non-interactive download of specific units
ttssh vault pull -force …                 overwrite existing files without asking
ttssh vault setup                         save credentials to config.json
```

`vault pull` writes each key as `<unit>.key` with owner-only permissions, so
downloaded keys immediately show up in ttssh's normal key scanning. The same
download flow is available inside the key picker (press `p`).

## Configuring the key directory

Keys must end in `.key`. The default search directory is `~/.ssh`.

The easiest way to change it is from inside the app: press `f` in the key
picker. This opens a folder browser — `/` filters the subfolders of the current
folder, `enter` steps into one, `←`/`h`/`backspace` goes up, and `space` (or
`.`) uses the current folder (the details pane shows how many `*.key` files
each folder contains). `t` lets you type or paste a full path (`~` is
expanded), and going up from a drive root on Windows offers a drive list.
After picking, choose whether to save the folder as the default or use it
just for this session. If the configured folder is missing or has no keys,
the key picker says so and points you to `f`.

It can also be set from the command line:

```
ttssh set-key-dir ~/my-keys     # persist a new default
ttssh --key-dir ~/other-keys    # override for a single run
ttssh config                    # show the effective key directory
```

The persisted setting is stored in your OS config directory
(`%AppData%\ttssh\config.json` on Windows, `~/.config/ttssh/config.json` on
Linux, `~/Library/Application Support/ttssh/config.json` on macOS).

## Project layout

```
cmd/ttssh/         CLI entry point: command dispatch, the Bubble Tea
                   dashboard (tui.go), session flows, key picker /
                   folder browser, vault subcommands
internal/config/   persistent settings (config.json), recents, path helpers
internal/ui/       color palette, shared styles, huh theme, print helpers
internal/vault/    key-vault client: Turso/libSQL access and v1 decryption
                   (plus the crypto interop tests)
```

## Building

```
go build -o ttssh ./cmd/ttssh
```

Cross-compile for other platforms, e.g. from any OS:

```
GOOS=linux  GOARCH=amd64 go build -o ttssh ./cmd/ttssh
GOOS=darwin GOARCH=arm64 go build -o ttssh ./cmd/ttssh
GOOS=windows GOARCH=amd64 go build -o ttssh.exe ./cmd/ttssh
```

Prebuilt binaries for Linux (amd64), macOS (arm64/amd64), and Windows (amd64)
are attached to each [GitHub release](https://github.com/XiaoKChen/TTSSH/releases)
(built automatically when a `v*` tag is pushed; local builds land in `dist/`) —
everything, including the terminal UI and the vault client, is statically
compiled in; the only runtime requirement is `ssh`/`scp` on PATH.

The binary reports its build version in the dashboard header and in `ttssh config`; it is
injected at build time, so release and install-script builds are stamped with
the git tag automatically.

## Installing (run `ttssh` from anywhere)

The install scripts build the binary and put it on your PATH:

```
# Windows (PowerShell, no admin needed)
.\scripts\install.ps1

# Linux / macOS
sh scripts/install.sh
```

- **Windows** installs to `%LOCALAPPDATA%\Programs\ttssh` and adds that folder
  to your user PATH — open a new terminal after the first install.
- **Linux/macOS** installs to `/usr/local/bin` (via sudo if needed), falling
  back to `~/.local/bin` when sudo isn't available.

Pass `-NoBuild` / `--no-build` to install an already-built binary (`./ttssh`,
`ttssh.exe`, or the matching `dist/` binary) instead of compiling from source.
Alternatively, plain `go install ./cmd/ttssh` works too if `$GOPATH/bin` is
already on your PATH.

`go test ./...` includes interop tests that decrypt the exact reference
vectors shared with Key-Upload-TUI's test suite — if those pass, ttssh can
read what the uploader wrote.

## Setup

Requires the Go toolchain (version per `go.mod`) and `ssh`/`scp` on PATH.

```
go mod download
```

## Development

```
go build ./...       # compile everything
go test ./...        # run all tests (includes vault crypto interop vectors)
go test -race ./...  # race detector for concurrent code
go vet ./...         # static analysis
gofmt -l .           # list files needing formatting
```

## Dependencies

- `github.com/charmbracelet/bubbletea` — the full-screen dashboard
- `github.com/charmbracelet/bubbles` — list, help, and spinner components
- `github.com/charmbracelet/huh` — forms embedded in the dashboard and the `vault` prompts
- `github.com/charmbracelet/lipgloss` — terminal styling
