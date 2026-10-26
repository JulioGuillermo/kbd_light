package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"kbdlight/light"
	"log"
	"math"
	"net"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const acpiCall = "/proc/acpi/call"

const blinkPeriod = 0.5 // seconds, one charger blink on and off

type daemon struct {
	method    string // the firmware method taking the intensity as its last argument
	statePath string

	mu         sync.Mutex
	cfg        light.Config
	value      int // last intensity written, -1 before the first write
	lastKey    time.Time
	cpu        float64    // processor load, 0-1, smoothed
	batteryLow bool       // at or below the alert level and not charging
	lowSince   time.Time  // when the battery went low (the alert's pulses count from here)
	flashAt    time.Time  // last notification flash
	blinkAt    time.Time  // last charger plug or unplug
	changed    *sync.Cond // signalled (on mu) when the configuration changes, for the watchers

	wake chan struct{} // configuration changed
	keys chan struct{} // a key was pressed
}

// write sends one intensity to the controller through the firmware's own method (acpi_call),
// skipping repeats unless forced (forced re-asserts it after resume or an Fn key change).
func (d *daemon) write(v int, force bool) error {
	d.mu.Lock()
	same := v == d.value
	d.mu.Unlock()
	if same && !force {
		return nil
	}
	f, err := os.OpenFile(acpiCall, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%s 0x%02x", d.method, v)
	f.Close()
	if err == nil {
		d.mu.Lock()
		d.value = v
		d.mu.Unlock()
	}
	return err
}

// probe checks once, at start, that acpi_call is there and the method answers without an error.
func (d *daemon) probe() error {
	if _, err := os.Stat(acpiCall); err != nil {
		return fmt.Errorf("%s is missing: load the acpi_call module (modprobe acpi_call)", acpiCall)
	}
	if err := os.WriteFile(acpiCall, []byte(d.method+" 0x00"), 0); err != nil {
		return err
	}
	out, _ := os.ReadFile(acpiCall)
	if strings.HasPrefix(string(out), "Error") {
		return fmt.Errorf("the firmware method %q failed: %s", d.method, strings.TrimSpace(string(out)))
	}
	d.value = 0
	return nil
}

// run drives the light: work out the intensity, write it, then sleep until the next frame, a new
// configuration, a key press or a flash, whichever comes first.
func (d *daemon) run() {
	timer := time.NewTimer(0)
	force := true
	for {
		now := time.Now()
		d.mu.Lock()
		cfg, st := d.cfg, light.State{LastKey: d.lastKey, CPU: d.cpu}
		low, lowSince, flashAt, blinkAt := d.batteryLow, d.lowSince, d.flashAt, d.blinkAt
		d.mu.Unlock()
		v, next := cfg.At(now, st)

		// Low battery, on top of any mode: the light stays off and every few seconds gives a strong,
		// quick double pulse
		if cfg.BatteryAlert && low {
			t := math.Mod(now.Sub(lowSince).Seconds(), cfg.BatteryEvery)
			x := math.Max(bump(t, 0.12, 0.05), bump(t, 0.36, 0.05))
			v = int(math.Round(255 * x))
			if t < 0.6 {
				next = cfg.Frame()
			} else {
				next = time.Duration((cfg.BatteryEvery - t) * 1e9)
			}
		}
		// Charger plugged in or out: three blinks, full and off, over a second and a half
		if since := now.Sub(blinkAt).Seconds(); since < 3*blinkPeriod {
			v = 0
			if math.Mod(since, blinkPeriod) < blinkPeriod/2 {
				v = 255
			}
			untilEdge := blinkPeriod/2 - math.Mod(since, blinkPeriod/2)
			next = time.Duration(untilEdge * 1e9)
		}
		// A notification: a quick double pulse to full, then back to whatever the mode shows
		if since := now.Sub(flashAt).Seconds(); since < 0.6 {
			x := math.Max(bump(since, 0.08, 0.06), bump(since, 0.34, 0.06))
			v = max(v, int(math.Round(255*x)))
			next = cfg.Frame()
		}

		if err := d.write(v, force); err != nil {
			log.Printf("write %d: %v", v, err)
		}
		timer.Reset(next)
		select {
		case <-timer.C:
			force = next >= time.Second // steady state: re-assert it
		case <-d.wake:
			force = true
		case <-d.keys:
			d.mu.Lock()
			d.lastKey = time.Now()
			d.mu.Unlock()
			force = false
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

func bump(t, c, w float64) float64 { x := (t - c) / w; return math.Exp(-x * x) }

// waitFor blocks, without waking up, until the configuration makes need() true.
func (d *daemon) waitFor(need func(light.Config) bool) {
	d.mu.Lock()
	for !need(d.cfg) {
		d.changed.Wait()
	}
	d.mu.Unlock()
}

// watchCPU keeps a smoothed processor load for the cpu mode (from /proc/stat, twice a second), and
// sleeps while another mode is on.
func (d *daemon) watchCPU() {
	var lastIdle, lastTotal uint64
	for {
		if d.mu.Lock(); d.cfg.Mode != "cpu" {
			d.mu.Unlock()
			d.waitFor(func(c light.Config) bool { return c.Mode == "cpu" })
			lastTotal = 0 // a fresh start: the old sample is stale
		} else {
			d.mu.Unlock()
		}
		if data, err := os.ReadFile("/proc/stat"); err == nil {
			f := strings.Fields(strings.SplitN(string(data), "\n", 2)[0]) // "cpu user nice system idle iowait ..."
			var total, idle uint64
			for i, v := range f[1:] {
				n, _ := strconv.ParseUint(v, 10, 64)
				total += n
				if i == 3 || i == 4 {
					idle += n
				}
			}
			if lastTotal > 0 && total > lastTotal {
				load := 1 - float64(idle-lastIdle)/float64(total-lastTotal)
				d.mu.Lock()
				d.cpu = 0.6*d.cpu + 0.4*load
				d.mu.Unlock()
			}
			lastIdle, lastTotal = idle, total
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// watchPower checks every 20 s whether the battery is at or below the alert level and discharging,
// and at once when the charger is plugged in or out. It sleeps while both power alerts are off.
func (d *daemon) watchPower() {
	powerChanged := make(chan struct{}, 1)
	go watchPowerEvents(powerChanged)
	for {
		d.waitFor(watchesPower)
		charger := chargerOnline()
		d.checkBattery(charger)
		for watchesPower(d.config()) {
			select {
			case <-time.After(20 * time.Second):
			case <-powerChanged:
			}
			chargerNow := chargerOnline()
			if chargerNow != charger && d.config().ChargerBlink {
				d.blink()
			}
			charger = chargerNow
			d.checkBattery(charger)
		}
	}
}

func watchesPower(c light.Config) bool { return c.BatteryAlert || c.ChargerBlink }

func (d *daemon) config() light.Config {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg
}

func (d *daemon) blink() {
	d.mu.Lock()
	d.blinkAt = time.Now()
	d.mu.Unlock()
	d.poke()
}

func (d *daemon) checkBattery(charger bool) {
	cfg := d.config()
	low := cfg.BatteryAlert && !charger && batteryBelow(cfg.BatteryLevel)
	d.mu.Lock()
	if low && !d.batteryLow {
		d.lowSince = time.Now()
	}
	changed := low != d.batteryLow
	d.batteryLow = low
	d.mu.Unlock()
	if changed {
		d.poke()
	}
}

func batteryBelow(level int) bool {
	batteries, _ := filepath.Glob("/sys/class/power_supply/BAT*")
	for _, bat := range batteries {
		capacity, err := strconv.Atoi(readSysfs(bat + "/capacity"))
		if err == nil && capacity <= level && readSysfs(bat+"/status") == "Discharging" {
			return true
		}
	}
	return false
}

func chargerOnline() bool {
	supplies, _ := filepath.Glob("/sys/class/power_supply/*")
	for _, supply := range supplies {
		if readSysfs(supply+"/type") == "Mains" && readSysfs(supply+"/online") == "1" {
			return true
		}
	}
	return false
}

func readSysfs(path string) string {
	data, _ := os.ReadFile(path)
	return strings.TrimSpace(string(data))
}

// watchPowerEvents signals every kernel event of a power supply (charger plugged or unplugged,
// battery status changed). The battery may still read "Discharging" for a moment after the
// charger event, so it signals again a second later.
func watchPowerEvents(changed chan<- struct{}) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_DGRAM, syscall.NETLINK_KOBJECT_UEVENT)
	if err != nil {
		log.Printf("power events: %v", err)
		return
	}
	defer syscall.Close(fd)
	const kernelEvents = 1
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK, Groups: kernelEvents}); err != nil {
		log.Printf("power events: %v", err)
		return
	}
	notify := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	buf := make([]byte, 8192)
	for {
		n, err := syscall.Read(fd, buf)
		if err != nil {
			log.Printf("power events: %v", err)
			return
		}
		if strings.Contains(string(buf[:n]), "\x00SUBSYSTEM=power_supply\x00") {
			notify()
			time.AfterFunc(time.Second, notify)
		}
	}
}

func (d *daemon) poke() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// watchKeyboards reads every keyboard's key presses (only that a key went down, never which one)
// for the reactive mode, picking up keyboards plugged in later.
func (d *daemon) watchKeyboards() {
	open := map[string]bool{}
	var mu sync.Mutex
	for {
		for _, path := range keyboards() {
			mu.Lock()
			seen := open[path]
			open[path] = true
			mu.Unlock()
			if seen {
				continue
			}
			go func(path string) {
				defer func() { mu.Lock(); delete(open, path); mu.Unlock() }()
				f, err := os.Open(path)
				if err != nil {
					return
				}
				defer f.Close()
				ev := make([]byte, 24) // struct input_event on 64-bit: timeval, type, code, value
				for {
					if _, err := f.Read(ev); err != nil {
						return
					}
					typ, value := binary.LittleEndian.Uint16(ev[16:]), int32(binary.LittleEndian.Uint32(ev[20:]))
					if typ == 1 && value == 1 { // EV_KEY, pressed
						select {
						case d.keys <- struct{}{}:
						default:
						}
					}
				}
			}(path)
		}
		time.Sleep(10 * time.Second)
	}
}

// keyboards lists the event devices of real keyboards: key events with autorepeat and LEDs,
// which leaves out power buttons, lid switches and hotkey-only devices.
func keyboards() []string {
	data, err := os.ReadFile("/proc/bus/input/devices")
	if err != nil {
		return nil
	}
	var out []string
	for _, block := range strings.Split(string(data), "\n\n") {
		var event string
		var ev uint64
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "H: Handlers=") {
				for _, h := range strings.Fields(strings.TrimPrefix(line, "H: Handlers=")) {
					if strings.HasPrefix(h, "event") {
						event = h
					}
				}
			}
			if strings.HasPrefix(line, "B: EV=") {
				ev, _ = strconv.ParseUint(strings.TrimPrefix(line, "B: EV="), 16, 64)
			}
		}
		const evKey, evLED, evRep = 1 << 1, 1 << 17, 1 << 20
		if event != "" && ev&(evKey|evLED|evRep) == evKey|evLED|evRep {
			out = append(out, "/dev/input/"+event)
		}
	}
	return out
}

func (d *daemon) load() {
	d.cfg = light.DefaultConfig()
	if data, err := os.ReadFile(d.statePath); err == nil {
		c := light.DefaultConfig() // settings added in later versions keep their defaults
		if json.Unmarshal(data, &c) == nil {
			if c, err := c.Clamp(); err == nil {
				d.cfg = c
			}
		}
	}
}

func (d *daemon) save(c light.Config) {
	if err := os.MkdirAll(filepath.Dir(d.statePath), 0o755); err != nil {
		log.Print(err)
		return
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	tmp := d.statePath + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err == nil {
		os.Rename(tmp, d.statePath)
	}
}

func (d *daemon) handle(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	var req light.Request
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
		json.NewEncoder(conn).Encode(light.Response{Error: "bad request: " + err.Error()})
		return
	}
	resp := light.Response{OK: true}
	if req.Cmd == "set" && req.Config != nil {
		c, err := req.Config.Clamp()
		if err != nil {
			resp = light.Response{Error: err.Error()}
		} else {
			d.mu.Lock()
			d.cfg = c
			d.mu.Unlock()
			d.save(c)
			d.poke()
			d.changed.Broadcast()
		}
	} else if req.Cmd == "flash" || req.Cmd == "notification" {
		// "notification" comes from notify-watch and only counts when the alert is on; "flash" always
		d.mu.Lock()
		if req.Cmd == "flash" || d.cfg.Notify {
			d.flashAt = time.Now()
		}
		d.mu.Unlock()
		d.poke()
	} else if req.Cmd != "status" {
		resp = light.Response{Error: "unknown command " + strconv.Quote(req.Cmd)}
	}
	d.mu.Lock()
	resp.Config, resp.Value = d.cfg, d.value
	d.mu.Unlock()
	json.NewEncoder(conn).Encode(resp)
}

func runDaemon(socket, statePath, method, group string) error {
	if os.Geteuid() != 0 {
		return errors.New("the daemon needs root (it writes to the firmware through acpi_call)")
	}
	d := &daemon{method: method, statePath: statePath, value: -1, wake: make(chan struct{}, 1), keys: make(chan struct{}, 1)}
	d.changed = sync.NewCond(&d.mu)
	if err := d.probe(); err != nil {
		return err
	}
	d.load()

	os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	// Only root and the chosen group (your user's) may talk to it
	gid := 0
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			return err
		}
		gid, _ = strconv.Atoi(g.Gid)
	}
	os.Chown(socket, 0, gid)
	os.Chmod(socket, 0o660)

	go d.watchKeyboards()
	go d.watchCPU()
	go d.watchPower()
	go d.run()
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		ln.Close()
		os.Remove(socket)
		os.Exit(0)
	}()
	log.Printf("kbdlight: %s, socket %s", d.cfg.Describe(), socket)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go d.handle(conn)
	}
}
