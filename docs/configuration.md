# Configuration

cli-capture reads JSON config for its **theme** and **keybindings**.

```
~/.config/cli-capture/config.json
```

or `$XDG_CONFIG_HOME/cli-capture/config.json` when that's set. Nothing is
created for you — no file means built-in defaults.

> Config and data are separate. `-dir` (default `~/.cli-capture`) still holds
> the CA, sessions, exports, and the log; only settings moved to the config
> directory. If you already have a `config.json` in the data directory it is
> still read, but only while `~/.config/cli-capture/config.json` doesn't exist.

## The file

Every key is optional — a file may set only what it changes.

```json
{
  "theme": {
    "base": "dark",
    "colors": {
      "focused": "#ff8800",
      "status5xx": "196"
    }
  },
  "keys": {
    "leader": "ctrl+b",
    "bindings": {
      "traffic": {
        "p": "flow.prev",
        "n": "flow.next",
        "x": "none"
      }
    }
  }
}
```

A malformed file, an unknown key, an unknown color, or an unknown action stops
startup with an error naming the file. A config that half-applied would be
harder to debug than one that refuses.

## Several files at once

`-config` takes files or preset names, may be repeated, and accepts
comma-separated lists. They merge left to right, on top of the default file:

```bash
cli-capture -config work -- gh pr list          # ~/.config/cli-capture/work.json
cli-capture -config base,dark -- some-cli       # two presets, dark wins conflicts
cli-capture -config ./ci.json -- some-cli       # an explicit path
cli-capture -config work -config light -- ...   # repeated flag, same thing
```

A value with a `/`, a leading `.`, or a `.json` suffix is a path. Anything else
is a **preset**: a bare `work` means `~/.config/cli-capture/work.json`. So keep
a handful of presets side by side —

```
~/.config/cli-capture/
├── config.json     always loaded
├── work.json       the corporate proxy's hosts, muted colors
├── light.json      just: {"theme": {"base": "light"}}
└── demo.json       high-contrast, for screen sharing
```

— and combine them per run: `-config work,light`.

Merging is per field, not per file: a later file setting one color keeps every
other color from the file before it. `bindings` merges per context, so a preset
can rebind one key without restating the rest.

A missing `-config` file is an error (you asked for it by name); a missing
default file is not.

## Themes

Pick a built-in with `"base"` or `-theme`:

| Theme | For |
|---|---|
| `dark` | the default — 256-color, dark terminals |
| `light` | darker foregrounds for a light background |
| `high-contrast` | the 16 basic colors, maximally separated |
| `none` | no color at all; bold, reverse, and borders still apply |

`-theme` beats the config file, so you can override a preset for one run:

```bash
cli-capture -theme light -- some-cli
```

Setting `NO_COLOR` to anything strips color regardless of theme.

Override individual colors under `"colors"`. Values are an ANSI 256 index
(`"196"`) or a hex triplet (`"#ff8800"`); an empty string means "render this
one plain".

| Key | What it colors |
|---|---|
| `focused` / `unfocused` | pane borders, active and inactive |
| `title` | pane titles |
| `pending` | a PAUSED flow |
| `flag` | flagged rows |
| `keycap` | key names in the help overlay |
| `dim` | secondary text |
| `section` | section headings |
| `status2xx` `status3xx` `status4xx` `status5xx` | the status column, by class |
| `json.key` `json.string` `json.number` `json.literal` `json.punct` | JSON body highlighting |
| `statusbar.mode` | the `monitor` / `req:on` chip |
| `statusbar.text` | the message beside it |

### Glyphs and border

Terminal font coverage varies, so the marker glyphs and the pane border are
themeable too — swap the default `⚑`/`▶`/`▸` for characters your font actually
draws (an empty string removes the glyph entirely):

```json
{
  "theme": {
    "glyphs": { "flag": "*", "pointer": ">", "arrow": "-" },
    "border": "normal"
  }
}
```

| `glyphs` key | Default | Where it's drawn |
|---|---|---|
| `flag` | `⚑` | the marker on flagged rows |
| `pointer` | `▶` | the PAUSED-flow / editor pointer |
| `arrow` | `▸` | the detail and Repeater title arrow |

`border` selects the pane and overlay border style: `rounded` (default),
`normal`, `thick`, `double`, or `hidden`. Glyphs and the border survive
`NO_COLOR` — it strips colors, not your chosen characters.

## Keybindings

`"leader"` sets the prefix key. It must be a ctrl key — `ctrl+a` through
`ctrl+z`, excluding `ctrl+i` and `ctrl+m` (terminals send those as Tab and
Enter). The default `ctrl+a` collides with tmux's own leader and with
readline's start-of-line, so `ctrl+b` or `ctrl+g` are worth considering:

```json
{"keys": {"leader": "ctrl+g"}}
```

`"bindings"` maps **context → key → action**. Contexts are `leader`,
`traffic`, `detail`, `editor`, and `repeater` — the same key can do different
things in the list and in the editor. Bind an action to `"none"` to unbind it.

Key names are the ones the help overlay shows: single characters, `enter`,
`esc`, `tab`, `up`/`down`, `ctrl+s`, and `" "` for space.

### Actions

| Context | Actions |
|---|---|
| `leader` | `pane.switch` `split.shrink` `split.grow` `intercept.requests` `intercept.responses` `session.save` `export.har` `export.flagged` `help.toggle` `app.quit` |
| `traffic` | `flow.prev` `flow.next` `flow.host-next` `flow.host-prev` `flow.flag` `flow.flagged-only` `flow.sort` `flow.filter` `flow.command` `flow.detail` `flow.resend` `repeater.open` `export.curl` `paused.edit` `paused.forward` `paused.drop` `ws.inject.client` `ws.inject.server` `intercept.requests` `intercept.responses` `help.toggle` |
| `detail` | `detail.save` `detail.close` |
| `editor` | `editor.send` `editor.fix-length` `editor.cancel` |
| `repeater` | `repeater.cycle-focus` `repeater.cycle-mode` `repeater.send` `repeater.close` |

The `?` overlay is generated from your live keymap, so it always shows what's
actually bound — rebind a key and the help follows. The vim motions and the `:`
commands are the exception: motions are prefixes and sequences rather than keys,
and `:` commands stay reachable even when you unbind the matching key, so both
are listed in the overlay unconditionally. See [keybindings](keybindings.md).

Two things are rejected rather than silently ignored: an action bound in a
context that doesn't have it, and binding the leader key inside another context
(the leader is consumed first, so that binding could never fire).

### Example: rebinding list movement

```json
{
  "keys": {
    "bindings": {
      "traffic": {
        "p": "flow.prev",
        "n": "flow.next",
        "/": "flow.filter"
      }
    }
  }
}
```

The built-in motion layer only sees keys the traffic keymap has not claimed.
Binding or disabling `g` removes `gg`; binding or disabling `G` removes `G`
and `{count}G`. Claiming any digit disables counted-motion help because that
prefix is no longer fully available. The live help overlay reflects these
changes. See [keybindings](keybindings.md#vim-style-motions).

## Where a setting came from

The startup log in `~/.cli-capture/cli-capture.log` records the files that were
merged, the resolved theme, and the leader:

```
config: /home/you/.config/cli-capture/config.json + /home/you/.config/cli-capture/work.json
theme: light
keys: leader ctrl+b
```
