# kbdlight

Keyboard backlight for ASUS laptops whose embedded controller takes any intensity from 0 to 255
(Zenbook S 14 UX5406 and similar). The kernel's `asus-wmi` driver only offers four levels; the
hardware takes 256, through the same firmware method Windows uses. kbdlight adds:

- **Fixed**: any of the 256 intensities.
- **Breathing**: fades between a lowest and a highest intensity, with the period you choose.
- **Reactive**: lights up when you press a key, stays on for a while, then fades out.

## Pieces

| | |
|---|---|
| `/usr/bin/kbdlight` | the daemon (root, `kbdlight.service`) and the command line; Go standard library only, ~3 MB |
| `/usr/bin/kbdlight-ui` | the window (Wails + WebKitGTK); only runs while open; menu entry "Keyboard light" with its own icon (`assets/kbdlight.svg`) |

```
kbdlight status
kbdlight off
kbdlight fixed 120
kbdlight breath --min 0 --max 200 --period 3s
kbdlight reactive --max 255 --hold 1s --fade 4s
kbdlight-ui
```

The setting is kept in `/var/lib/kbdlight/state.json` and comes back after a reboot. The daemon
re-applies it every few seconds when steady, so a resume or the Fn light key does not undo it.

## How it works

The firmware's DSDT turns the four levels into a duty cycle (`PWKB = 00 55 AA FF`) and sends it to the
embedded controller with `\_SB.PC00.LPCB.EC0.ST9E(0x1F, 0xFF, value)`. kbdlight calls that method
directly through `/proc/acpi/call` (the `acpi_call` module), ~30 times a second while animating and
only on changes otherwise. Another model may use another method: `kbdlight daemon --method '...'`.

The reactive mode reads key presses from the keyboards' event devices (only that a key went down,
never which one). Only the daemon runs as root; the socket `/run/kbdlight.sock` is limited to root
and your user's group, and the daemon only accepts a configuration, never an arbitrary ACPI call.

## Install

Needs Go, gcc, WebKitGTK 4.1 / GTK 3 (for the window) and the `acpi_call` module
(Manjaro: `pacman -S go linux-acpi_call-meta`).

```
./install.sh              # build both, install to /usr/bin, enable kbdlight.service,
                          # and with Brasa add Custom › Keyboard light to its panel
./install.sh --uninstall
```
