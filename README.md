# Tailtray

A menu bar app for Tailscale that looks and works the same on macOS and Linux. It talks to tailscaled's local API, so it runs alongside the `tailscale` command line rather than replacing it.

The icon shows whether Tailscale is connected, connecting, or using an exit node, and marks an exit node that has gone offline. The menu covers:

- Connecting, disconnecting and logging in, with health warnings and key expiry
- Exit nodes, with the one Tailscale suggests marked, and local network access
- Devices grouped into yours, tagged and shared, each with Ping, SSH, its connection path and copyable addresses
- Serve and Funnel: what this device serves, local ports it could serve, and Funnel on or off
- Taildrop: sending a file to a device, and saving received files to Downloads
- Accept Routes, Tailscale DNS, and switching, adding or removing accounts

A notification says when Tailscale connects or disconnects, or the exit node changes or goes offline, unless the change came from the menu.

## Install

From a clone, with Go:

```
go build -o ~/.local/bin/tailtray .
```

macOS builds need the Xcode Command Line Tools for cgo. Linux builds need nothing beyond Go.

Start it at login: on macOS from System Settings, General, Login Items; on Linux from your desktop's autostart, or an `exec` line in an i3 config.

## Requirements

tailscaled must be running. Changing settings needs permission to edit its preferences; without it the menu is read-only and offers to copy the fix:

```
sudo tailscale set --operator=$USER
```

The open-source tailscaled on macOS cannot use exit nodes ([tailscale/tailscale#1650](https://github.com/tailscale/tailscale/issues/1650)), so the Exit Nodes menu needs a tailscaled that can.

On Linux the icon appears in any tray that hosts StatusNotifierItem icons. i3bar and other XEmbed-only trays need a bridge such as [snixembed](https://git.sr.ht/~steef/snixembed), started before tailtray. Dialogs use `zenity`, notifications `notify-send`, the clipboard `wl-copy`, `xclip` or `xsel`, and listening ports come from `ss`. SSH opens in Ghostty on both systems.

## Flags

| Flag | Default | What it does |
|---|---|---|
| `-theme` | `dark` | Linux icon colors for a `dark` or `light` bar. macOS uses a template icon that follows the menu bar. |
| `-socket` | tailscaled's default | The local API socket, for a tailscaled started with a different `--socket`. |

## License

MIT
