package light

import (
	"math"
	"testing"
	"time"
)

func TestDefaultCurve(t *testing.T) {
	c := clampCurve(DefaultCurve())
	for _, tc := range []struct{ x, want float64 }{{0, 0}, {0.5, 1}, {1, 0}} {
		if got := CurveAt(c, tc.x); math.Abs(got-tc.want) > 0.01 {
			t.Errorf("CurveAt(%v) = %.3f, want %.3f", tc.x, got, tc.want)
		}
	}
	// Rises on the way up, falls on the way down
	if !(CurveAt(c, 0.2) < CurveAt(c, 0.4) && CurveAt(c, 0.6) > CurveAt(c, 0.8)) {
		t.Error("the default curve should rise to the middle and fall after it")
	}
}

func TestClampCurveKeepsAFunction(t *testing.T) {
	// Out-of-order nodes and handles crossing into the next segment
	c := clampCurve([]Node{{X: 0.7, Y: 2, InX: 0.1, OutX: 2}, {X: 0.3, Y: -1, InX: -1, OutX: 0.9}})
	if c[0].X != 0 || c[len(c)-1].X != 1 {
		t.Fatalf("ends not pinned: %+v", c)
	}
	for i, n := range c {
		if n.Y < 0 || n.Y > 1 || n.InX > n.X || n.OutX < n.X {
			t.Errorf("node %d out of range: %+v", i, n)
		}
		if i+1 < len(c) && n.OutX > c[i+1].X {
			t.Errorf("node %d's handle crosses the next node", i)
		}
	}
}

func TestModesStayInRange(t *testing.T) {
	base := DefaultConfig()
	base.Min, base.Max = 10, 240
	now := time.Unix(1_700_000_000, 0)
	for _, mode := range Modes {
		c := base
		c.Mode, c.FPS = mode, 5+len(mode)*5 // different rates
		c, _ = c.Clamp()
		for i := 0; i < 400; i++ {
			at := now.Add(time.Duration(i) * 37 * time.Millisecond)
			v, next := c.At(at, State{LastKey: now, CPU: float64(i%100) / 100})
			if v < 0 || v > 255 || next <= 0 {
				t.Fatalf("%s at step %d: value %d, next %v", mode, i, v, next)
			}
		}
	}
}

func TestOldStateKeepsNewDefaults(t *testing.T) {
	c := DefaultConfig()
	c.Mode, c.BPM, c.BatteryLevel = "heartbeat", 0, 0 // as if read from an older state file
	c, _ = c.Clamp()
	if c.BPM < 20 || c.BatteryLevel < 1 {
		t.Errorf("clamp should bring missing values into range: %+v", c)
	}
}
