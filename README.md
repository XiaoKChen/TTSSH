# ttssh

An interactive SSH manager for the terminal. A keyboard-driven dashboard lists
your saved connections (in nested folders) and recent connections; pick an SSH key — from a local folder or straight out
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
- **Connections** (left) — a tree of your saved connections in nested folders
  (`▾`/`▸` with an item count), then a `── Recent ──` section with your 10 most
  recent connections, most recent first. Rows show `user@host` and the key name
  (`☁ unit` for vault keys, `password` for connections without a key).
- **Details** (right) — for a connection: target, key, folder, and the commands
  available; for a folder: its path, contents, and commands. Hidden when the
  terminal is narrower than 80 columns.
- **Status line** — results and warnings (`✓` success, `!` warning, `✗` error),
  cleared after a few seconds, and a spinner while ttssh waits on the vault or
  the remote host.
- **Footer** — a minimal hint row for the current screen; `/` then `?` shows
  everything.

### Typing and commands

On every list screen, **typing filters the list** — there are no single-letter
action keys. Any printable character except `/` is added to the filter shown in
the list title, `backspace` edits it, and `esc` clears it (or, with no filter,
goes back one screen). While you filter the dashboard, the tree turns into a
flat list of matching connections (with their folder path) and recents.

Press **`/`** to open the command popup: a small box listing the commands that
are valid for the current screen and selection. The next key runs a command and
closes the popup; `esc` or a second `/` just closes it, and an unknown key
closes it with a `no shortcut` message. For example `/` then `x` removes the
selected connection.

Dashboard commands (after `/`):

- **n** — new connection:
  - **Pick a key** — `No key — log in with a password` at the top, then vault
    keys (when the vault is configured) and the `*.key` files in your key folder
    (default `~/.ssh`); the details pane shows each key's path, size, and
    modified time, or its vault fingerprint and status. In the key picker,
    `/` `f` switches to another folder (and optionally saves it as the
    default) and `/` `p` downloads vault keys into a folder.
  - **Enter the target** — username and IP/hostname, plus an optional **Save to
    folder** choice (default: the selected folder). The connection is always
    added to your recents; saving it also files it in the chosen folder.
- **u** / **d** — copy a file TO / FROM the host. Upload: enter a local folder
  (default `.`), filter its files, enter the remote destination (default `~/`),
  and ttssh runs `scp`. Download: enter a remote folder (default `~`); ttssh
  lists its files over SSH, you filter and pick one, then enter the local
  destination (default `.`). If the remote can't be listed (key hosts that need
  a passphrase prompt, or any password login), you type the remote path instead.
- **a** — save the selected recent connection into a folder.
- **m** — move the selected connection or folder to another folder (or the top
  level).
- **f** — new folder, inside the selected folder (or the selected connection's
  folder, or at the top level).
- **r** — rename the selected folder.
- **x** — remove the selected connection or recent, or delete a folder with
  everything in it (asks y/n first, stating how many connections it holds).
- **X** — clear every recent connection (asks y/n first).
- **?** — full help, **q** — quit.

`enter` on a connection or recent runs SSH (`ssh -i <key> user@host`); the
dashboard steps aside while the session runs and comes back when it ends.
`enter` on a folder folds or unfolds it; `→`/`←` expand/collapse, and `←` on a
connection jumps to its folder. Folders start expanded each run.

### Saved folders

Saved connections live in `config.json` under `saved`, as nested folders that
you create and rearrange with the commands above. Connecting to a saved entry
also bumps it into your recents. Recents whose key file was deleted are pruned
automatically; saved entries are never removed automatically — a missing key
file or vault unit only shows a warning. `ttssh clear-recents` forgets the
recents from the command line.

### Connections without a key

Choose `No key — log in with a password` in the key picker. ttssh runs
`ssh`/`scp` without `-i`, and they ask for the password themselves while the
dashboard is suspended. **Passwords are never stored.** Listing remote files for
downloads uses a non-interactive ssh, so on password hosts you type the remote
path instead.

### Keybindings

| Where | Key | Action |
|---|---|---|
| Every list | `↑`/`↓`, `pgup`/`pgdn` | move |
| | any character except `/` | filter the list |
| | `backspace` | edit the filter |
| | `esc` | clear the filter, or go back one screen |
| | `/` | open the command popup |
| | `ctrl+c` | quit (works everywhere) |
| Dashboard | `enter` | SSH into the connection, or fold/unfold a folder |
| | `→` / `←` | expand / collapse a folder; `←` on a connection selects its folder |
| | `/` `n` | new connection |
| | `/` `u` / `/` `d` | upload / download a file |
| | `/` `a` | save a recent into a folder |
| | `/` `m` | move a connection or folder |
| | `/` `f` / `/` `r` | new folder / rename folder |
| | `/` `x` | remove connection or recent, delete folder (`y` confirms) |
| | `/` `X` | clear all recents (`y` confirms) |
| | `/` `?` / `/` `q` | full help / quit |
| Key picker | `enter` | use the selected key (or password login) |
| | `/` `f` | change the key folder |
| | `/` `p` | download vault keys to a folder |
| Folder browser | `enter` | open the selected folder |
| | `←` or `backspace` on an empty filter | parent folder (a drive list at a Windows drive root) |
| | `/` `.` | use the current folder |
| | `/` `t` | type a path (`~` is expanded) |
| y/n questions | `y` / `n` or `esc` | confirm / cancel |

Forms and text fields are unaffected: there `/` and every letter are typed
literally (paths need `/`), and `esc` backs out.

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
download flow is available inside the key picker (`/` then `p`).

## Configuring the key directory

Keys must end in `.key`. The default search directory is `~/.ssh`.

The easiest way to change it is from inside the app: press `/` then `f` in the
key picker. This opens a folder browser — typing filters the subfolders of the
current folder, `enter` steps into one, `←`/`backspace` goes up, and `/` then
`.` uses the current folder (the details pane shows how many `*.key` files
each folder contains). `/` then `t` lets you type or paste a full path (`~` is
expanded), and going up from a drive root on Windows offers a drive list.
After picking, choose whether to save the folder as the default or use it
just for this session. If the configured folder is missing or has no keys,
the key picker says so and points you to `/` `f`.

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
                   dashboard (tui.go, folders.go), session flows, key picker /
                   folder browser, vault subcommands
internal/config/   persistent settings (config.json), recents, saved folder tree, path helpers
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
