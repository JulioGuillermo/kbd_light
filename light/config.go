// Package light is what the kbdlight daemon, its command line and its window share: the configuration,
// the light it gives at each moment, and the socket protocol.
package light

import (
	"fmt"
	"math"
	"time"
)

// Config is what the daemon does with the keyboard light. Intensities are the embedded controller's
// own scale, 0 (off) to 255 (full); durations are in seconds.
type Config struct {
	Mode   string  `json:"mode"`   // off, fixed, breath or reactive
	Level  int     `json:"level"`  // fixed: the intensity
	Min    int     `json:"min"`    // breath: the lowest point
	Max    int     `json:"max"`    // breath: the highest point; reactive: where a key press starts
	Period float64 `json:"period"` // breath: one full in-and-out
	Hold   float64 `json:"hold"`   // reactive: time at Max after the last key press
	Fade   float64 `json:"fade"`   // reactive: time to fade from Max to off
}

var Modes = []string{"off", "fixed", "breath", "reactive"}

func DefaultConfig() Config {
	return Config{Mode: "off", Level: 128, Min: 0, Max: 200, Period: 3, Hold: 1, Fade: 4}
}

// Clamp keeps every field in range, so the daemon never writes an odd value to the controller.
func (c Config) Clamp() (Config, error) {
	known := false
	for _, m := range Modes {
		known = known || c.Mode == m
	}
	if !known {
		return c, fmt.Errorf("unknown mode %q (off, fixed, breath, reactive)", c.Mode)
	}
	c.Level, c.Min, c.Max = clampInt(c.Level, 0, 255), clampInt(c.Min, 0, 255), clampInt(c.Max, 0, 255)
	if c.Min > c.Max {
		c.Min, c.Max = c.Max, c.Min
	}
	c.Period = ClampFloat(c.Period, 0.5, 60)
	c.Hold = ClampFloat(c.Hold, 0, 60)
	c.Fade = ClampFloat(c.Fade, 0.2, 60)
	return c, nil
}

// At returns the intensity for this moment and how long it stays valid: animations ask again
// ~30 times a second, steady states only to re-assert it now and then.
func (c Config) At(now, lastKey time.Time) (int, time.Duration) {
	const frame, steady = 33 * time.Millisecond, 5 * time.Second
	switch c.Mode {
	case "fixed":
		return c.Level, steady
	case "breath":
		t := float64(now.UnixNano()%int64(c.Period*1e9)) / 1e9
		x := 0.5 - 0.5*math.Cos(2*math.Pi*t/c.Period)
		// Squared: the eye sees brightness roughly logarithmically, so a linear ramp looks like it
		// rushes through the dark part and lingers at the top
		return c.Min + int(math.Round(float64(c.Max-c.Min)*x*x)), frame
	case "reactive":
		if lastKey.IsZero() {
			return 0, steady
		}
		since := now.Sub(lastKey).Seconds()
		switch {
		case since < c.Hold:
			return c.Max, time.Duration((c.Hold - since) * 1e9)
		case since < c.Hold+c.Fade:
			left := 1 - (since-c.Hold)/c.Fade
			return int(math.Round(float64(c.Max) * left * left)), frame
		}
		return 0, steady
	}
	return 0, steady
}

func (c Config) Describe() string {
	switch c.Mode {
	case "fixed":
		return fmt.Sprintf("fixed at %d", c.Level)
	case "breath":
		return fmt.Sprintf("breathing %d-%d every %.1fs", c.Min, c.Max, c.Period)
	case "reactive":
		return fmt.Sprintf("reactive: %d on a key press, %.1fs, then fades in %.1fs", c.Max, c.Hold, c.Fade)
	}
	return "off"
}

func clampInt(v, lo, hi int) int { return max(lo, min(hi, v)) }

func ClampFloat(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
