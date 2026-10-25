package main

import (
	"bufio"
	"log"
	"os/exec"
	"strings"
	"time"

	"kbdlight/light"
)

// notifyWatch runs in your session (kbdlight-notify.service): it watches the session bus for desktop
// notifications (the standard org.freedesktop.Notifications Notify call, whatever shows them) and
// asks the daemon for a flash, which it gives only when the notification alert is on. dbus-monitor
// keeps this program free of D-Bus libraries.
func notifyWatch() error {
	for {
		cmd := exec.Command("dbus-monitor", "--session",
			"type='method_call',interface='org.freedesktop.Notifications',member='Notify'")
		out, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		if err := cmd.Start(); err != nil {
			return err
		}
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "method call") && strings.Contains(sc.Text(), "member=Notify") {
				if _, err := light.Call(light.Request{Cmd: "notification"}); err != nil {
					log.Print(err)
				}
			}
		}
		cmd.Wait()
		log.Print("dbus-monitor stopped; again in 5 s")
		time.Sleep(5 * time.Second)
	}
}
