// kbdlight: keyboard backlight daemon and command line for ASUS laptops whose embedded controller takes a 0-255 intensity
// (Zenbook S 14 UX5406 and similar), written through the firmware's own ACPI method with acpi_call.
// The kernel's driver only offers four levels (0x00, 0x55, 0xAA, 0xFF); this gives every value in
// between, breathing and a light that wakes up when you type.
//
// Usage:
//
//	kbdlight daemon [--socket PATH] [--state PATH] [--method ACPI] [--group NAME]   (root, systemd)
//	kbdlight status [--json]
//	kbdlight off
//	kbdlight fixed LEVEL                                0-255
//	kbdlight breath [--min N] [--max N] [--period 3s]
//	kbdlight reactive [--max N] [--hold 1s] [--fade 4s]   lights up while you type
//	kbdlight heartbeat [--min N] [--max N] [--bpm 60]
//	kbdlight candle [--level N] [--flicker 0.35]
//	kbdlight cpu [--min N] [--max N]                    follows the processor's load
//	kbdlight custom [--min N] [--max N] [--period 3s]   your own curve (drawn in kbdlight-ui)
//	kbdlight notify on|off                              flash on desktop notifications
//	kbdlight battery-alert on|off [--level 20] [--every 10s]
//	kbdlight charger-blink on|off                       three blinks on plugging or unplugging the charger
//	kbdlight flash                                      one flash, to try it
//	kbdlight fps N                                      animation updates per second, 5-60 (15)
//	kbdlight-ui                                         the window
//	kbdlight daemon [--group NAME] ...                  as root (kbdlight.service)
//	kbdlight notify-watch                               as your user (kbdlight-notify.service)
//
// The window (kbdlight-ui) is a separate program, so this one stays small.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"kbdlight/light"
)

const (
	defaultState = "/var/lib/kbdlight/state.json"
	// Zenbook S 14 (UX5406): ST9E(0x1F, 0xFF, intensity) is what the firmware itself uses for the light
	defaultMethod = `\_SB.PC00.LPCB.EC0.ST9E 0x1F 0xFF`
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "daemon":
		fs := flag.NewFlagSet("daemon", flag.ExitOnError)
		sock := fs.String("socket", light.DefaultSocket, "control socket")
		state := fs.String("state", defaultState, "where the chosen mode is kept")
		method := fs.String("method", defaultMethod, "ACPI method and leading arguments; the intensity is appended")
		group := fs.String("group", "", "group allowed to control it (e.g. your user's)")
		fs.Parse(args)
		err = runDaemon(*sock, *state, *method, *group)
	case "status":
		fs := flag.NewFlagSet("status", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "print JSON")
		fs.Parse(args)
		var r light.Response
		if r, err = light.Status(); err == nil {
			if *asJSON {
				json.NewEncoder(os.Stdout).Encode(r)
			} else {
				fmt.Printf("%s (now %d)\n", r.Config.Describe(), r.Value)
			}
		}
	case "off":
		err = change(func(c *light.Config) { c.Mode = "off" })
	case "fixed":
		if len(args) != 1 {
			usage()
		}
		level, e := strconv.Atoi(args[0])
		if e != nil {
			fail(fmt.Errorf("level: a number 0-255"))
		}
		err = change(func(c *light.Config) { c.Mode, c.Level = "fixed", level })
	case "breath":
		err = change(func(c *light.Config) {
			fs := flag.NewFlagSet("breath", flag.ExitOnError)
			fs.IntVar(&c.Min, "min", c.Min, "lowest intensity 0-255")
			fs.IntVar(&c.Max, "max", c.Max, "highest intensity 0-255")
			period := fs.Duration("period", seconds(c.Period), "one full breath")
			fs.Parse(args)
			c.Mode, c.Period = "breath", period.Seconds()
		})
	case "reactive":
		err = change(func(c *light.Config) {
			fs := flag.NewFlagSet("reactive", flag.ExitOnError)
			fs.IntVar(&c.Max, "max", c.Max, "intensity on a key press 0-255")
			hold := fs.Duration("hold", seconds(c.Hold), "time at full after the last key")
			fade := fs.Duration("fade", seconds(c.Fade), "time to fade out")
			fs.Parse(args)
			c.Mode, c.Hold, c.Fade = "reactive", hold.Seconds(), fade.Seconds()
		})
	case "heartbeat":
		err = change(func(c *light.Config) {
			fs := flag.NewFlagSet("heartbeat", flag.ExitOnError)
			fs.IntVar(&c.Min, "min", c.Min, "between beats 0-255")
			fs.IntVar(&c.Max, "max", c.Max, "the beat 0-255")
			fs.Float64Var(&c.BPM, "bpm", c.BPM, "beats per minute")
			fs.Parse(args)
			c.Mode = "heartbeat"
		})
	case "candle":
		err = change(func(c *light.Config) {
			fs := flag.NewFlagSet("candle", flag.ExitOnError)
			fs.IntVar(&c.Level, "level", c.Level, "the flame's brightness 0-255")
			fs.Float64Var(&c.Flick, "flicker", c.Flick, "how much it moves, 0-1")
			fs.Parse(args)
			c.Mode = "candle"
		})
	case "cpu", "custom":
		err = change(func(c *light.Config) {
			fs := flag.NewFlagSet(cmd, flag.ExitOnError)
			fs.IntVar(&c.Min, "min", c.Min, "lowest 0-255")
			fs.IntVar(&c.Max, "max", c.Max, "highest 0-255")
			period := fs.Duration("period", seconds(c.Period), "custom: one full cycle")
			fs.Parse(args)
			c.Mode, c.Period = cmd, period.Seconds()
		})
	case "notify":
		if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
			usage()
		}
		err = change(func(c *light.Config) { c.Notify = args[0] == "on" })
	case "battery-alert":
		if len(args) < 1 || (args[0] != "on" && args[0] != "off") {
			usage()
		}
		err = change(func(c *light.Config) {
			fs := flag.NewFlagSet("battery-alert", flag.ExitOnError)
			fs.IntVar(&c.BatteryLevel, "level", c.BatteryLevel, "percent at or below which it alerts")
			every := fs.Duration("every", seconds(c.BatteryEvery), "time between pulses")
			fs.Parse(args[1:])
			c.BatteryAlert, c.BatteryEvery = args[0] == "on", every.Seconds()
		})
	case "charger-blink":
		if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
			usage()
		}
		err = change(func(c *light.Config) { c.ChargerBlink = args[0] == "on" })
	case "fps":
		if len(args) != 1 {
			usage()
		}
		n, e := strconv.Atoi(args[0])
		if e != nil {
			fail(fmt.Errorf("fps: a number 5-60"))
		}
		err = change(func(c *light.Config) { c.FPS = n })
	case "flash":
		_, err = light.Call(light.Request{Cmd: "flash"})
	case "notify-watch":
		err = notifyWatch()
	default:
		usage()
	}
	if err != nil {
		fail(err)
	}
}

// change applies an edit to the daemon's current configuration (unset options keep their values).
func change(edit func(*light.Config)) error {
	r, err := light.Status()
	if err != nil {
		return err
	}
	c := r.Config
	edit(&c)
	if r, err = light.Set(c); err == nil {
		fmt.Println(r.Config.Describe())
	}
	return err
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

func usage() {
	fmt.Fprint(os.Stderr, "kbdlight: keyboard backlight with 256 levels and effects\n"+`  kbdlight status [--json]
  kbdlight off
  kbdlight fixed LEVEL                                0-255
  kbdlight breath [--min N] [--max N] [--period 3s]
  kbdlight reactive [--max N] [--hold 1s] [--fade 4s]   lights up while you type
  kbdlight heartbeat [--min N] [--max N] [--bpm 60]
  kbdlight candle [--level N] [--flicker 0.35]
  kbdlight cpu [--min N] [--max N]                    follows the processor's load
  kbdlight custom [--min N] [--max N] [--period 3s]   your own curve (drawn in kbdlight-ui)
  kbdlight notify on|off                              flash on desktop notifications
  kbdlight battery-alert on|off [--level 20] [--every 10s]
  kbdlight charger-blink on|off                       three blinks on plugging or unplugging the charger
  kbdlight flash                                      one flash, to try it
  kbdlight fps N                                      animation updates per second, 5-60 (15)
  kbdlight-ui                                         the window
  kbdlight daemon [--group NAME] ...                  as root (kbdlight.service)
  kbdlight notify-watch                               as your user (kbdlight-notify.service)
`)
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "kbdlight:", err)
	os.Exit(1)
}
