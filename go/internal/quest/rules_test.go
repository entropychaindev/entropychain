package quest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"testing"
)

func fakeHash(parts ...any) string {
	s := sha256.Sum256([]byte(fmt.Sprint(parts...)))
	return hex.EncodeToString(s[:])
}

func TestRNGUniformAndDeterministic(t *testing.T) {
	a, b := NewRNG(fakeHash(1), fakeHash(2), "x"), NewRNG(fakeHash(1), fakeHash(2), "x")
	counts := make([]int, 6)
	for range 60000 {
		v := a.Intn(6)
		if v != b.Intn(6) {
			t.Fatal("same seed gave different rolls")
		}
		counts[v]++
	}
	for f, c := range counts {
		if c < 9500 || c > 10500 {
			t.Errorf("face %d came up %d times out of 60000", f+1, c)
		}
	}
	if NewRNG(fakeHash(1), fakeHash(3), "x").Intn(1<<30) == NewRNG(fakeHash(1), fakeHash(2), "x").Intn(1<<30) {
		t.Error("different transactions should not share rolls")
	}
}

func TestLevelCurve(t *testing.T) {
	for _, c := range []struct {
		xp int64
		l  int
	}{{0, 1}, {39, 1}, {40, 2}, {159, 2}, {160, 3}, {XPForLevel(13), 13}, {1 << 40, MaxLevel}} {
		if got := Level(c.xp); got != c.l {
			t.Errorf("Level(%d) = %d, want %d", c.xp, got, c.l)
		}
	}
	for l := 1; l <= MaxLevel; l++ {
		if Level(XPForLevel(l)) != l || (l > 1 && Level(XPForLevel(l)-1) != l-1) {
			t.Errorf("XPForLevel(%d) is not the threshold", l)
		}
	}
}

func TestSummonAndTraitsRoundTrip(t *testing.T) {
	h := Summon(fakeHash("b"), fakeHash("tx"))
	if h.Class == "" || h.Name == "" || h.Str < 3 {
		t.Fatalf("bad hero %+v", h)
	}
	back, ok := HeroFromTraits(h.ID, h.Traits())
	if !ok || !reflect.DeepEqual(back, h) {
		t.Fatalf("hero traits round trip: %+v vs %+v", back, h)
	}
	it := rollItem(NewRNG(fakeHash(1), fakeHash(2), "i"), 3, 0, "item-x")
	got, ok := ItemFromTraits(it.ID, it.Traits())
	if !ok || got != it {
		t.Fatalf("item traits round trip: %+v vs %+v", got, it)
	}
}

func TestRunDeterministicAndCapped(t *testing.T) {
	h := Summon(fakeHash("b"), fakeHash("tx"))
	d, _ := DungeonByID(2)
	r1 := Run(h, Progress{XP: 500}, nil, d, fakeHash("beacon"), fakeHash("run"))
	r2 := Run(h, Progress{XP: 500}, nil, d, fakeHash("beacon"), fakeHash("run"))
	if !reflect.DeepEqual(r1, r2) {
		t.Fatal("same inputs gave different runs")
	}
	for i := range 2000 {
		r := Run(h, Progress{XP: 1 << 30}, nil, d, fakeHash("b", i), fakeHash("r", i))
		if r.Payout > d.Fee*maxPayoutMul || r.Payout%payoutStep != 0 || r.Payout < 0 {
			t.Fatalf("payout %d breaks the cap or rounding", r.Payout)
		}
		if len(r.Events) == 0 || len(r.Events) > 5 {
			t.Fatalf("%d rooms", len(r.Events))
		}
	}
}

// gearFor makes a typical set: one item per slot of the given tier and rarity.
func gearFor(tier, rarity int) []Item {
	var g []Item
	for _, slot := range []string{"weapon", "armor", "charm"} {
		g = append(g, Item{Slot: slot, Tier: tier, Rarity: rarity, Power: tier*3 + rarity*3 + 1})
	}
	return g
}

// TestBalance prints the economy table; run with -run Balance -v. It fails
// only if a geared hero on level stops being worth playing (under 75%) or
// costs the treasury money (over 100%).
func TestBalance(t *testing.T) {
	const n = 3000
	for _, d := range Dungeons {
		for _, c := range []struct {
			name   string
			level  int
			gear   []Item
			strict bool
		}{
			{"under-leveled, no gear", max(1, d.Level-3), nil, false},
			{"on level, no gear", d.Level, nil, false},
			{"on level, tier gear", d.Level, gearFor(max(1, d.ID-1), 1), true},
			{"over-leveled, good gear", d.Level + 5, gearFor(d.ID, 2), false},
		} {
			var paid, xp int64
			clears, deaths, items := 0, 0, 0
			for i := range n {
				h := Summon(fakeHash("hb", i), fakeHash("ht", i))
				r := Run(h, Progress{XP: XPForLevel(c.level)}, c.gear, d, fakeHash("b", d.ID, i), fakeHash("r", d.ID, i))
				paid += r.Payout
				xp += r.XP
				items += len(r.Items)
				if r.Cleared {
					clears++
				}
				if !r.Survived {
					deaths++
				}
			}
			ret := float64(paid) / float64(int64(n)*d.Fee)
			t.Logf("T%d %-13s %-26s return %5.1f%%  clear %3d%%  death %3d%%  xp %5.1f  items/run %.2f",
				d.ID, d.Name, c.name, ret*100, clears*100/n, deaths*100/n, float64(xp)/n, float64(items)/n)
			if c.strict && (ret > 1.0 || ret < 0.75) {
				t.Errorf("T%d %s: return %.0f%% is outside 75–100%%", d.ID, c.name, ret*100)
			}
		}
	}
}
