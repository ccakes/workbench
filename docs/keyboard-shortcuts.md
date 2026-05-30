# Keyboard Shortcuts

## Navigation

| Key | Action |
|-----|--------|
| `j` / `Down` | Move selection down (list pane) or scroll logs down toward newest |
| `k` / `Up` | Move selection up (list pane) or scroll logs up toward oldest |
| `h` / `Left` | Scroll the log pane left (for lines wider than the pane) |
| `l` / `Right` | Scroll the log pane right |
| `Ctrl+D` / `PageDown` | Scroll logs down one page |
| `Ctrl+U` / `PageUp` | Scroll logs up one page |
| `Tab` | Switch between service list and log pane |
| `g` | Scroll to top of logs |
| `G` | Scroll to bottom of logs (re-enables follow) |

Vertical scrolling clamps at the top and bottom; reaching the bottom re-enables
follow mode. Horizontal scrolling lets you read log lines that are wider than the
pane instead of having them truncated, and clamps so the longest visible line's
right edge stops at the pane edge.

## Service Control

| Key | Action |
|-----|--------|
| `r` | Restart selected service |
| `s` | Stop selected service |
| `S` | Start selected service |
| `w` | Toggle file watch for selected service |

## Logs

| Key | Action |
|-----|--------|
| `f` | Toggle follow mode (auto-scroll to new output) |
| `c` | Clear log buffer for selected service |
| `a` | Toggle all-services interleaved log view |
| `/` | Enter search/filter mode |

### Search mode

| Key | Action |
|-----|--------|
| Type | Add characters to search query |
| `Backspace` | Remove last character |
| `Enter` | Confirm search filter |
| `Esc` | Cancel search |

## General

| Key | Action |
|-----|--------|
| `?` | Toggle help screen |
| `q` | Quit workbench (asks for confirmation) |
| `Ctrl+C` | Quit workbench (asks for confirmation) |

### Quit confirmation

Pressing `q` or `Ctrl+C` opens a confirmation dialog before exiting, so an
accidental keypress won't tear down all your running services. Press `y`
(or `Enter`) to confirm and quit, or any other key (e.g. `n` / `Esc`) to cancel
and return to the TUI. The prompt works from any view — the service list, the
trace browser, the waterfall, and the service map.
