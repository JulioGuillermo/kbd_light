#!/usr/bin/env bash
# kbdlight installer: builds the daemon and the window here and installs everything system-wide: both
# programs to /usr/bin, the system service (kbdlight.service, as root), the menu entry and its icon
# (/usr/share/applications, /usr/share/icons) and, with Brasa, a "Keyboard light" button in the control
# panel's Custom menu. Asks for sudo. ./install.sh --uninstall removes it all.
set -euo pipefail
SUDO=${SUDO:-sudo}   # e.g. SUDO="sudo -A" with an askpass helper
HERE=$(cd "$(dirname "$0")" && pwd)
USER_GROUP=$(id -gn)
BIN=/usr/bin
DESKTOP=/usr/share/applications/kbdlight-ui.desktop        # named like the window's app id, so it gets the icon
ICON=/usr/share/icons/hicolor/scalable/apps/kbdlight.svg
PANEL=~/.config/brasa/panel.conf      # Brasa's custom panel buttons
PANEL_LINE="󰌌 | Keyboard light | setsid -f $BIN/kbdlight-ui"
say() { printf '\e[1;31m▶\e[0m %s\n' "$*"; }

refresh_menus() {  # icon and menu caches, where the system has them
  command -v gtk-update-icon-cache >/dev/null && $SUDO gtk-update-icon-cache -q -f /usr/share/icons/hicolor 2>/dev/null || true
  command -v update-desktop-database >/dev/null && $SUDO update-desktop-database -q /usr/share/applications 2>/dev/null || true
}

panel_reload() {  # rebuild Brasa's panel so the Custom menu shows the change (when Brasa is installed)
  [ -f ~/.config/brasa/docs/gen-waybar-swaync.py ] || return 0
  python3 ~/.config/brasa/docs/gen-waybar-swaync.py ~/.config/brasa/config >/dev/null 2>&1 || true
  command -v swaync-client >/dev/null && swaync-client -R >/dev/null 2>&1 || true
}

if [ "${1:-}" = --uninstall ]; then
  $SUDO systemctl disable --now kbdlight 2>/dev/null || true
  $SUDO rm -f $BIN/kbdlight $BIN/kbdlight-ui /usr/local/bin/kbdlight /usr/local/bin/kbdlight-ui \
             /etc/systemd/system/kbdlight.service /etc/modules-load.d/kbdlight.conf
  $SUDO rm -f "$DESKTOP" "$ICON"
  rm -f ~/.local/share/applications/kbdlight-ui.desktop
  refresh_menus
  [ -f "$PANEL" ] && sed -i '/kbdlight-ui/d' "$PANEL" && panel_reload
  $SUDO systemctl daemon-reload
  say "Removed (the saved setting stays in /var/lib/kbdlight)"; exit 0
fi

command -v go >/dev/null || { echo "Go is needed to build it (pacman -S go)"; exit 1; }
[ -d /sys/module/acpi_call ] || modinfo acpi_call >/dev/null 2>&1 ||
  { echo "The acpi_call kernel module is needed (Manjaro: pacman -S linux-acpi_call-meta)"; exit 1; }

say "Building"
cd "$HERE"
mkdir -p bin
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/kbdlight ./cmd/kbdlight
CGO_ENABLED=1 go build -tags desktop,production,webkit2_41 -trimpath -ldflags "-s -w" -o bin/kbdlight-ui ./ui

say "Installing (sudo)"
$SUDO install -m 755 bin/kbdlight bin/kbdlight-ui $BIN/
$SUDO rm -f /usr/local/bin/kbdlight /usr/local/bin/kbdlight-ui   # from earlier installs
echo acpi_call | $SUDO tee /etc/modules-load.d/kbdlight.conf >/dev/null
$SUDO tee /etc/systemd/system/kbdlight.service >/dev/null <<UNIT
[Unit]
Description=Keyboard backlight: 256 levels, breathing, reactive (kbdlight)
After=systemd-modules-load.service

[Service]
ExecStartPre=-/usr/bin/modprobe acpi_call
# Only root and the group $USER_GROUP may control it (its socket is /run/kbdlight.sock)
ExecStart=$BIN/kbdlight daemon --group $USER_GROUP
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
UNIT
$SUDO systemctl daemon-reload
$SUDO systemctl enable --now kbdlight
$SUDO systemctl restart kbdlight

# Menu entry and icon, for every user
$SUDO install -Dm644 assets/kbdlight.svg "$ICON"
$SUDO tee "$DESKTOP" >/dev/null <<DESK
[Desktop Entry]
Name=Keyboard light
GenericName=Keyboard backlight
Comment=Keyboard backlight: any intensity, breathing, light up while typing
Exec=$BIN/kbdlight-ui
Icon=kbdlight
Terminal=false
Type=Application
Categories=Settings;HardwareSettings;
Keywords=keyboard;backlight;light;breathing;kbd;
StartupWMClass=kbdlight-ui
DESK
rm -f ~/.local/share/applications/kbdlight-ui.desktop   # the per-user entry of earlier installs
refresh_menus

# Brasa: a button in the control panel's Custom menu that opens the window
if [ -d ~/.config/brasa ]; then
  touch "$PANEL"
  sed -i '/kbdlight-ui/d' "$PANEL"
  echo "$PANEL_LINE" >> "$PANEL"
  panel_reload
  say "Brasa panel: Custom › Keyboard light"
fi
sleep 1
say "Ready: $($BIN/kbdlight status 2>&1)"
