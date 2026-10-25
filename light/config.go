// Package light is what the kbdlight daemon, its command line and its window share: the configuration,
// the light it gives at each moment, and the socket protocol.
package light

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Config is what the daemon does with the keyboard light. Intensities are the embedded controller's
// own scale, 0 (off) to 255 (full); durations are in seconds.
type Config struct {
	Mode   string  `json:"mode"`    // one of Modes
	Level  int     `json:"level"`   // fixed: the intensity; candle: the flame's middle
	Min    int     `json:"min"`     // breath, heartbeat, cpu, custom: the lowest point
	Max    int     `json:"max"`     // breath, heartbeat, cpu, custom: the highest; reactive: on a key press
	Period float64 `json:"period"`  // breath, custom: one full cycle
	Hold   float64 `json:"hold"`    // reactive: time at Max after the last key press
	Fade   float64 `json:"fade"`    // reactive: time to fade from Max to off
	BPM    float64 `json:"bpm"`     // heartbeat: beats per minute
	Flick  float64 `json:"flicker"` // candle: how much the flame moves, 0-1
	Curve  []Node  `json:"curve"`   // custom: the shape over one period, x and y in 0-1
	FPS    int     `json:"fps"`     // animation updates per second (each one runs the firmware method)

	// Alerts, on top of any mode
	Notify       bool    `json:"notify"`        // flash on desktop notifications (kbdlight notify-watch)
	BatteryAlert bool    `json:"battery_alert"` // low battery: light off, a strong double pulse every BatteryEvery
	BatteryLevel int     `json:"battery_level"` // percent at or below which the alert starts
	BatteryEvery float64 `json:"battery_every"`
}

// Node is a point of a custom curve with its two Bézier handles (absolute coordinates, 0-1).
type Node struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	InX  float64 `json:"in_x"`
	InY  float64 `json:"in_y"`
	OutX float64 `json:"out_x"`
	OutY float64 `json:"out_y"`
}

var Modes = []string{"off", "fixed", "breath", "reactive", "heartbeat", "candle", "cpu", "custom"}

func DefaultCurve() []Node {
	return []Node{
		{X: 0, Y: 0, InX: 0, InY: 0, OutX: 0.25, OutY: 0},
		{X: 0.5, Y: 1, InX: 0.3, InY: 1, OutX: 0.7, OutY: 1},
		{X: 1, Y: 0, InX: 0.75, InY: 0, OutX: 1, OutY: 0},
	}
}

func DefaultConfig() Config {
	return Config{Mode: "off", Level: 128, Min: 0, Max: 200, Period: 3, Hold: 1, Fade: 4, BPM: 60, Flick: 0.35,
		Curve: DefaultCurve(), FPS: 15, BatteryLevel: 20, BatteryEvery: 10}
}

// Clamp keeps every field in range, so the daemon never writes an odd value to the controller.
func (c Config) Clamp() (Config, error) {
	known := false
	for _, m := range Modes {
		known = known || c.Mode == m
	}
	if !known {
		return c, fmt.Errorf("unknown mode %q", c.Mode)
	}
	c.Level, c.Min, c.Max = clampInt(c.Level, 0, 255), clampInt(c.Min, 0, 255), clampInt(c.Max, 0, 255)
	if c.Min > c.Max {
		c.Min, c.Max = c.Max, c.Min
	}
	c.Period = ClampFloat(c.Period, 0.5, 60)
	c.Hold = ClampFloat(c.Hold, 0, 60)
	c.Fade = ClampFloat(c.Fade, 0.2, 60)
	c.BPM = ClampFloat(c.BPM, 20, 200)
	c.Flick = ClampFloat(c.Flick, 0, 1)
	c.BatteryLevel = clampInt(c.BatteryLevel, 1, 99)
	c.BatteryEvery = ClampFloat(c.BatteryEvery, 2, 300)
	c.Curve = clampCurve(c.Curve)
	if c.FPS == 0 {
		c.FPS = 15 // saved by a version without the setting
	}
	c.FPS = clampInt(c.FPS, 5, 60)
	return c, nil
}

// clampCurve sorts the nodes, pins the ends to x=0 and x=1 and keeps each handle within its segment,
// so the curve stays a function of time (one height for each moment).
func clampCurve(nodes []Node) []Node {
	if len(nodes) < 2 {
		return DefaultCurve()
	}
	if len(nodes) > 32 {
		nodes = nodes[:32]
	}
	n := append([]Node(nil), nodes...)
	sort.Slice(n, func(i, j int) bool { return n[i].X < n[j].X })
	n[0].X, n[len(n)-1].X = 0, 1
	for i := range n {
		n[i].X, n[i].Y = ClampFloat(n[i].X, 0, 1), ClampFloat(n[i].Y, 0, 1)
		n[i].InY, n[i].OutY = ClampFloat(n[i].InY, 0, 1), ClampFloat(n[i].OutY, 0, 1)
		lo, hi := n[i].X, n[i].X
		if i > 0 {
			lo = n[i-1].X
		}
		if i < len(n)-1 {
			hi = n[i+1].X
		}
		n[i].InX = ClampFloat(n[i].InX, lo, n[i].X)
		n[i].OutX = ClampFloat(n[i].OutX, n[i].X, hi)
	}
	return n
}

// CurveAt is the custom curve's height (0-1) at time x (0-1).
func CurveAt(n []Node, x float64) float64 {
	for i := 0; i+1 < len(n); i++ {
		a, b := n[i], n[i+1]
		if x > b.X && i+2 < len(n) {
			continue
		}
		// Cubic Bézier P0 (a), P1 (a's out handle), P2 (b's in handle), P3 (b): find s with Bx(s) = x
		bez := func(p0, p1, p2, p3, s float64) float64 {
			u := 1 - s
			return u*u*u*p0 + 3*u*u*s*p1 + 3*u*s*s*p2 + s*s*s*p3
		}
		lo, hi := 0.0, 1.0
		for k := 0; k < 30; k++ {
			mid := (lo + hi) / 2
			if bez(a.X, a.OutX, b.InX, b.X, mid) < x {
				lo = mid
			} else {
				hi = mid
			}
		}
		return ClampFloat(bez(a.Y, a.OutY, b.InY, b.Y, (lo+hi)/2), 0, 1)
	}
	return 0
}

// State is what, besides the time, decides the light: the last key press and the processor's load.
type State struct {
	LastKey time.Time
	CPU     float64 // 0-1
}

// Frame is the time between two animation updates.
func (c Config) Frame() time.Duration {
	if c.FPS <= 0 {
		return time.Second / 15
	}
	return time.Second / time.Duration(c.FPS)
}

// At returns the intensity for this moment and how long it stays valid: animations ask again FPS
// times a second, steady states only to re-assert it now and then.
func (c Config) At(now time.Time, s State) (int, time.Duration) {
	const steady = 30 * time.Second // re-assert a steady light now and then (after a resume, the Fn key)
	frame := c.Frame()
	span := float64(c.Max - c.Min)
	secs := float64(now.UnixNano()) / 1e9
	switch c.Mode {
	case "fixed":
		return c.Level, steady
	case "breath":
		x := 0.5 - 0.5*math.Cos(2*math.Pi*math.Mod(secs, c.Period)/c.Period)
		// Squared: the eye sees brightness roughly logarithmically, so a linear ramp looks like it
		// rushes through the dark part and lingers at the top
		return c.Min + round(span*x*x), frame
	case "reactive":
		if s.LastKey.IsZero() {
			return 0, steady
		}
		since := now.Sub(s.LastKey).Seconds()
		switch {
		case since < c.Hold:
			return c.Max, time.Duration((c.Hold - since) * 1e9)
		case since < c.Hold+c.Fade:
			left := 1 - (since-c.Hold)/c.Fade
			return round(float64(c.Max) * left * left), frame
		}
		return 0, steady
	case "heartbeat":
		// "lub-dub": a strong beat, a softer one right after, then rest until the next beat
		t := math.Mod(secs, 60/c.BPM)
		x := math.Max(pulse(t, 0.10, 0.05), 0.65*pulse(t, 0.32, 0.06))
		return c.Min + round(span*x), frame
	case "candle":
		// Several slow waves at unrelated speeds: a flame that moves without ever repeating visibly
		w := (math.Sin(secs*2.3) + 0.6*math.Sin(secs*5.7+1) + 0.35*math.Sin(secs*11.3+2)) / 1.95
		return clampInt(round(float64(c.Level)*(1-c.Flick*(0.5-0.5*w))), 0, 255), frame
	case "cpu":
		return c.Min + round(span*s.CPU), max(frame, 100*time.Millisecond) // the load is read twice a second
	case "custom":
		return c.Min + round(span*CurveAt(c.Curve, math.Mod(secs, c.Period)/c.Period)), frame
	}
	return 0, steady
}

// pulse is a smooth bump centred at c with half-width w.
func pulse(t, c, w float64) float64 { d := (t - c) / w; return math.Exp(-d * d) }

func round(v float64) int { return int(math.Round(v)) }

func (c Config) Describe() string {
	var d string
	switch c.Mode {
	case "fixed":
		d = fmt.Sprintf("fixed at %d", c.Level)
	case "breath":
		d = fmt.Sprintf("breathing %d-%d every %.1fs", c.Min, c.Max, c.Period)
	case "reactive":
		d = fmt.Sprintf("reactive: %d on a key press, %.1fs, then fades in %.1fs", c.Max, c.Hold, c.Fade)
	case "heartbeat":
		d = fmt.Sprintf("heartbeat %d-%d at %.0f bpm", c.Min, c.Max, c.BPM)
	case "candle":
		d = fmt.Sprintf("candle around %d, flicker %.0f%%", c.Level, c.Flick*100)
	case "cpu":
		d = fmt.Sprintf("processor load, %d-%d", c.Min, c.Max)
	case "custom":
		d = fmt.Sprintf("custom curve (%d points) %d-%d every %.1fs", len(c.Curve), c.Min, c.Max, c.Period)
	default:
		d = "off"
	}
	if c.Notify {
		d += "; flashes on notifications"
	}
	if c.BatteryAlert {
		d += fmt.Sprintf("; low battery alert at %d%% every %.0fs", c.BatteryLevel, c.BatteryEvery)
	}
	return d
}

func clampInt(v, lo, hi int) int { return max(lo, min(hi, v)) }

func ClampFloat(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
