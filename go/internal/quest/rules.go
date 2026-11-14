// Package quest holds the rules of Entropy Quest, an expedition RPG played on
// EntropyChain.
//
// Every result is a pure function of chain data: the beacon of the block after
// a player's transaction, that transaction's hash, and the hero's earlier
// runs. Nobody knows the beacon when the transaction is signed, so nobody can
// choose an outcome, and anyone can recompute one. The game master
// (cmd/questd) and the browser (cmd/questwasm, compiled to WebAssembly) run
// this same code.
package quest

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

const (
	// GameTag marks player transactions: data["game"] == GameTag.
	GameTag = "entropy-quest"
	// RulesVersion changes whenever a rule change would alter a result.
	RulesVersion = 1

	Coin      = int64(100_000_000) // base units per ENTRO
	SummonFee = 5 * Coin
	MaxLevel  = 30
	MaxGear   = 3 // one weapon, one armor, one charm

	// Payouts are rounded down to 0.0001 ENTRO and capped at a multiple of
	// the entry fee, so one lucky run cannot drain the treasury.
	payoutStep   = int64(10_000)
	maxPayoutMul = 3
)

// ---------------------------------------------------------------------------
// Randomness

// RNG is a deterministic stream: SHA-256(seed ‖ counter) blocks, read four
// bytes at a time. The seed binds the beacon, the player's transaction and a
// purpose label, so two runs in the same block never share rolls.
type RNG struct {
	seed [32]byte
	ctr  uint64
	buf  []byte
}

func NewRNG(beaconHex, txHash, purpose string) *RNG {
	b, _ := hex.DecodeString(beaconHex)
	t, _ := hex.DecodeString(txHash)
	h := sha256.New()
	h.Write(b)
	h.Write(t)
	h.Write([]byte(purpose))
	r := &RNG{}
	copy(r.seed[:], h.Sum(nil))
	return r
}

func (r *RNG) u32() uint32 {
	if len(r.buf) < 4 {
		var c [8]byte
		binary.BigEndian.PutUint64(c[:], r.ctr)
		r.ctr++
		s := sha256.Sum256(append(r.seed[:], c[:]...))
		r.buf = s[:]
	}
	v := binary.BigEndian.Uint32(r.buf)
	r.buf = r.buf[4:]
	return v
}

// Intn returns a uniform integer in [0, n), without modulo bias.
func (r *RNG) Intn(n int) int {
	if n <= 1 {
		return 0
	}
	limit := (uint64(1) << 32) / uint64(n) * uint64(n)
	for {
		if v := uint64(r.u32()); v < limit {
			return int(v % uint64(n))
		}
	}
}

// Range returns a uniform integer in [lo, hi].
func (r *RNG) Range(lo, hi int) int { return lo + r.Intn(hi-lo+1) }

// Chance is true pct percent of the time.
func (r *RNG) Chance(pct int) bool { return r.Intn(100) < pct }

func pick[T any](r *RNG, xs []T) T { return xs[r.Intn(len(xs))] }

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

// ---------------------------------------------------------------------------
// Heroes

var Classes = []string{"Warrior", "Rogue", "Mage", "Ranger"}

var (
	firstNames = []string{"Kael", "Mira", "Tobin", "Sera", "Dorn", "Lyra", "Brann", "Isolde", "Fenn",
		"Rook", "Vesna", "Orrin", "Talia", "Garrick", "Nyx", "Edda", "Corvin", "Wren", "Hale", "Ysolt"}
	epithets = []string{"the Bold", "the Quiet", "Ashborn", "of the Marsh", "Ironhand", "the Wanderer",
		"Stormcaller", "the Lucky", "Grimshaw", "of the Pale Road", "Emberheart", "the Unbroken"}
)

// Hero is the permanent part of a character, fixed when it is summoned and
// stored as the hero NFT's traits.
type Hero struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Class    string `json:"class"`
	Str      int    `json:"str"`
	Dex      int    `json:"dex"`
	Int      int    `json:"int"`
	Vit      int    `json:"vit"`
	Luck     int    `json:"luck"`
	Starborn bool   `json:"starborn"` // 3% of heroes: +2 to every stat
	Seed     string `json:"seed"`     // the beacon that summoned it (portrait art)
	SummonTx string `json:"summon_tx"`
}

// HeroID is the hero NFT's token_id: derived from the summon transaction.
func HeroID(summonTx string) string { return "hero-" + summonTx[:12] }

// Summon creates the hero for a summon transaction.
func Summon(beacon, summonTx string) Hero {
	r := NewRNG(beacon, summonTx, "summon")
	stat := func() int { return 3 + r.Intn(4) + r.Intn(4) } // 3–9, middle-heavy
	h := Hero{ID: HeroID(summonTx), Seed: beacon, SummonTx: summonTx}
	h.Class = pick(r, Classes)
	h.Str, h.Dex, h.Int, h.Vit, h.Luck = stat(), stat(), stat(), stat(), stat()
	switch h.Class {
	case "Warrior":
		h.Str += 3
		h.Vit += 2
	case "Rogue":
		h.Dex += 3
		h.Luck += 2
	case "Mage":
		h.Int += 4
		h.Luck++
	case "Ranger":
		h.Dex += 2
		h.Str += 2
		h.Vit++
	}
	if r.Chance(3) {
		h.Starborn = true
		h.Str, h.Dex, h.Int, h.Vit, h.Luck = h.Str+2, h.Dex+2, h.Int+2, h.Vit+2, h.Luck+2
	}
	h.Name = pick(r, firstNames) + " " + pick(r, epithets)
	return h
}

// Traits is the hero as NFT traits (ints, strings and bools only).
func (h Hero) Traits() map[string]any {
	return map[string]any{
		"game": GameTag, "kind": "hero", "name": h.Name, "class": h.Class,
		"str": h.Str, "dex": h.Dex, "int": h.Int, "vit": h.Vit, "luck": h.Luck,
		"starborn": h.Starborn, "seed": h.Seed, "summon_tx": h.SummonTx,
	}
}

// Progress is what a hero has earned from its runs. It is not stored on
// chain: anyone can rebuild it by replaying the hero's runs in chain order.
type Progress struct {
	XP     int64 `json:"xp"`
	Runs   int   `json:"runs"`
	Clears int   `json:"clears"`
	Deaths int   `json:"deaths"`
	Gold   int64 `json:"gold"` // ENTRO base units won
}

// Level is 1 + floor(sqrt(xp / 40)), capped at MaxLevel.
func Level(xp int64) int {
	l := 1 + int(isqrt(xp/40))
	return min(l, MaxLevel)
}

// XPForLevel is the XP needed to reach level l.
func XPForLevel(l int) int64 { return int64(l-1) * int64(l-1) * 40 }

func isqrt(n int64) int64 {
	if n < 2 {
		return max(n, 0)
	}
	x := int64(1) << ((bitLen(uint64(n)) + 1) / 2)
	for {
		y := (x + n/x) / 2
		if y >= x {
			return x
		}
		x = y
	}
}

func bitLen(v uint64) int {
	n := 0
	for v > 0 {
		v >>= 1
		n++
	}
	return n
}

// Stats are the numbers combat uses.
type Stats struct {
	Level int `json:"level"`
	MaxHP int `json:"max_hp"`
	Atk   int `json:"atk"`
	Def   int `json:"def"`
	Hit   int `json:"hit"`   // % before the enemy's evasion
	Crit  int `json:"crit"`  // %
	Dodge int `json:"dodge"` // %
	Spell int `json:"spell"` // % chance per turn to cast (Mage)
	Find  int `json:"find"`  // % added to item drop chances
}

// Effective combines a hero, its level and the gear it carries.
func Effective(h Hero, level int, gear []Item) Stats {
	str, dex, in, vit, luck := h.Str, h.Dex, h.Int, h.Vit, h.Luck
	grow := level - 1
	switch h.Class {
	case "Warrior":
		str += grow
	case "Rogue", "Ranger":
		dex += grow
	case "Mage":
		in += grow
	}
	vit += grow / 2
	luck += grow / 4
	main := map[string]int{"Warrior": str, "Rogue": dex, "Mage": in, "Ranger": dex}[h.Class]
	s := Stats{
		Level: level,
		MaxHP: 30 + 6*vit + 4*level,
		Atk:   4 + main + level/2,
		Def:   vit / 3,
		Hit:   72 + dex*2,
		Crit:  4 + luck,
		Dodge: dex,
		Find:  luck / 2,
	}
	switch h.Class {
	case "Rogue":
		s.Dodge += 8
	case "Mage":
		s.Spell = 25 + in/3
	case "Ranger":
		s.Hit += 8
	}
	for _, it := range gear {
		switch it.Slot {
		case "weapon":
			s.Atk += it.Power/2 + 1
		case "armor":
			s.Def += it.Power / 4
			s.MaxHP += it.Power * 2
		case "charm":
			s.Crit += it.Power / 3
			s.Dodge += it.Power / 4
			s.Find += it.Power / 3
		}
	}
	return s
}

// ---------------------------------------------------------------------------
// Items

var Rarities = []string{"Common", "Uncommon", "Rare", "Epic", "Legendary"}

var (
	itemBases = map[string][]string{
		"weapon": {"Sword", "Axe", "Dagger", "Staff", "Bow", "Mace", "Spear", "Wand"},
		"armor":  {"Mail", "Robe", "Cuirass", "Cloak", "Plate", "Jerkin"},
		"charm":  {"Amulet", "Ring", "Talisman", "Idol", "Locket"},
	}
	rarityWords = [][]string{
		{"Worn", "Plain", "Iron", "Rusty"},
		{"Fine", "Keen", "Sturdy", "Polished"},
		{"Runed", "Gleaming", "Storm", "Moonlit"},
		{"Dread", "Celestial", "Abyssal", "Wyrmbone"},
		{"Entropic", "Eternal", "Starforged", "Beaconlit"},
	}
	itemSuffixes = []string{"of Embers", "of the Crypt", "of Chance", "of the Void", "of Echoes",
		"of the Deep", "of Frost", "of the Beacon"}
)

// Item is a piece of loot, minted as an NFT whose traits hold these fields.
type Item struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Slot   string `json:"slot"`
	Rarity int    `json:"rarity"` // index into Rarities
	Tier   int    `json:"tier"`
	Power  int    `json:"power"`
}

// ItemID is the item NFT's token_id: derived from the run that dropped it.
func ItemID(runTx string, n int) string {
	return "item-" + runTx[:10] + "-" + string(rune('a'+n))
}

func rollItem(r *RNG, tier, find int, id string) Item {
	// Rarity: 55/27/12/5/1 before luck; luck moves weight up the table.
	roll := r.Intn(100) + find
	rar := 0
	switch {
	case roll >= 99:
		rar = 4
	case roll >= 94:
		rar = 3
	case roll >= 82:
		rar = 2
	case roll >= 55:
		rar = 1
	}
	slot := pick(r, []string{"weapon", "armor", "charm"})
	name := pick(r, rarityWords[rar]) + " " + pick(r, itemBases[slot])
	if rar >= 2 {
		name += " " + pick(r, itemSuffixes)
	}
	return Item{ID: id, Name: name, Slot: slot, Rarity: rar, Tier: tier,
		Power: tier*2 + rar*2 + r.Intn(3)}
}

// Traits is the item as NFT traits.
func (it Item) Traits() map[string]any {
	return map[string]any{
		"game": GameTag, "kind": "item", "name": it.Name, "slot": it.Slot,
		"rarity": Rarities[it.Rarity], "rarity_rank": it.Rarity, "tier": it.Tier, "power": it.Power,
	}
}

// ItemFromTraits reads an item NFT back.
func ItemFromTraits(id string, t map[string]any) (Item, bool) {
	if t["game"] != GameTag || t["kind"] != "item" {
		return Item{}, false
	}
	it := Item{ID: id, Name: str(t["name"]), Slot: str(t["slot"]),
		Rarity: num(t["rarity_rank"]), Tier: num(t["tier"]), Power: num(t["power"])}
	if itemBases[it.Slot] == nil || it.Rarity < 0 || it.Rarity >= len(Rarities) {
		return Item{}, false
	}
	return it, true
}

// HeroFromTraits reads a hero NFT back.
func HeroFromTraits(id string, t map[string]any) (Hero, bool) {
	if t["game"] != GameTag || t["kind"] != "hero" {
		return Hero{}, false
	}
	h := Hero{ID: id, Name: str(t["name"]), Class: str(t["class"]),
		Str: num(t["str"]), Dex: num(t["dex"]), Int: num(t["int"]), Vit: num(t["vit"]), Luck: num(t["luck"]),
		Seed: str(t["seed"]), SummonTx: str(t["summon_tx"])}
	h.Starborn, _ = t["starborn"].(bool)
	return h, h.Class != ""
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// num accepts the number types a JSON decoder or this package may produce.
func num(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case interface{ Int64() (int64, error) }:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

// ---------------------------------------------------------------------------
// Dungeons

type Dungeon struct {
	ID       int      `json:"id"`
	Name     string   `json:"name"`
	Blurb    string   `json:"blurb"`
	Fee      int64    `json:"fee"`   // entry fee, base units
	Level    int      `json:"level"` // recommended hero level
	Monsters []string `json:"monsters"`
	Boss     string   `json:"boss"`
}

var Dungeons = []Dungeon{
	{1, "Goblin Warren", "Tunnels full of things that bite. A fine place to start.", 1 * Coin, 1,
		[]string{"Goblin", "Giant Rat", "Kobold"}, "Goblin King"},
	{2, "Sunken Crypt", "Flooded tombs where the dead keep their gold.", 2 * Coin, 4,
		[]string{"Skeleton", "Ghoul", "Drowned Knight"}, "Lich of the Tide"},
	{3, "Ember Mines", "Abandoned when the dwarves dug too deep and too hot.", 4 * Coin, 8,
		[]string{"Fire Imp", "Magma Golem", "Ash Wraith"}, "Cinder Wyrm"},
	{4, "Frost Spire", "A tower of ice that grows a floor every winter.", 7 * Coin, 13,
		[]string{"Ice Troll", "Frost Wolf", "Rime Witch"}, "Frost Giant Jarl"},
	{5, "Void Citadel", "Where randomness itself is born. Nothing here plays fair.", 12 * Coin, 19,
		[]string{"Void Stalker", "Mind Flayer", "Null Knight"}, "The Entropy"},
}

// DungeonByID returns the dungeon with that id.
func DungeonByID(id int) (Dungeon, bool) {
	for _, d := range Dungeons {
		if d.ID == id {
			return d, true
		}
	}
	return Dungeon{}, false
}

type monster struct {
	name                string
	hp, atk, def, evade int
}

// Monsters scale with the dungeon's recommended level, so each dungeon is
// about as hard for a hero of that level as the first is for a new hero.
func makeMonster(r *RNG, d Dungeon, boss bool) monster {
	l := d.Level
	m := monster{name: pick(r, d.Monsters), hp: 40 + 7*l/2, atk: 6 + l*130/100, def: 1 + l/2, evade: 5 + l/2}
	m.hp = m.hp * r.Range(85, 115) / 100
	if boss {
		m.name = d.Boss
		m.hp = m.hp * 25 / 10
		m.atk = m.atk*125/100 + 1
	}
	return m
}

// ---------------------------------------------------------------------------
// Expeditions

// Swing is one attack in a fight, for the replay.
type Swing struct {
	Hero   bool   `json:"hero"` // true: the hero attacked
	Kind   string `json:"kind"` // hit, miss, crit, spell, volley, ambush, block
	Dmg    int    `json:"dmg"`
	HeroHP int    `json:"hero_hp"`          // after the swing
	FoeHP  int    `json:"foe_hp,omitempty"` // after the swing
}

// Event is one room of an expedition.
type Event struct {
	Room   int     `json:"room"` // 1–4, 5 = boss
	Kind   string  `json:"kind"` // monster, boss, trap, chest, shrine
	Name   string  `json:"name"`
	FoeHP  int     `json:"foe_hp,omitempty"`
	Swings []Swing `json:"swings,omitempty"`
	Roll   int     `json:"roll,omitempty"` // trap: d20 + DEX against the target
	Target int     `json:"target,omitempty"`
	Dmg    int     `json:"dmg,omitempty"`
	Heal   int     `json:"heal,omitempty"`
	Gold   int64   `json:"gold,omitempty"`
	XP     int64   `json:"xp,omitempty"`
	Item   *Item   `json:"item,omitempty"`
	Won    bool    `json:"won"`
	HP     int     `json:"hp"` // hero HP after the room
}

// Result is a whole expedition.
type Result struct {
	Rules    int     `json:"rules"`
	Dungeon  int     `json:"dungeon"`
	Stats    Stats   `json:"stats"`
	Events   []Event `json:"events"`
	Survived bool    `json:"survived"`
	Cleared  bool    `json:"cleared"` // beat the boss
	Found    int64   `json:"found"`   // gold found before any death penalty
	Payout   int64   `json:"payout"`  // ENTRO base units the game master sends
	XP       int64   `json:"xp"`
	Items    []Item  `json:"items"`
}

// Run plays one expedition. gear must already be filtered to items the hero's
// owner held when the run was sent (at most one per slot).
func Run(h Hero, prog Progress, gear []Item, d Dungeon, beacon, runTx string) Result {
	r := NewRNG(beacon, runTx, "run")
	s := Effective(h, Level(prog.XP), gear)
	res := Result{Rules: RulesVersion, Dungeon: d.ID, Stats: s, Survived: true, Items: []Item{}}
	hp := s.MaxHP
	pct := func(lo, hi int) int64 { return d.Fee * int64(r.Range(lo, hi)) / 100 }
	drop := func(base int) *Item {
		if !r.Chance(base + s.Find) {
			return nil
		}
		it := rollItem(r, d.ID, s.Find, ItemID(runTx, len(res.Items)))
		res.Items = append(res.Items, it)
		return &it
	}

	for room := 1; room <= 5 && hp > 0; room++ {
		ev := Event{Room: room}
		kind := "boss"
		if room < 5 {
			switch roll := r.Intn(100); {
			case roll < 55:
				kind = "monster"
			case roll < 70:
				kind = "trap"
			case roll < 85:
				kind = "chest"
			default:
				kind = "shrine"
			}
		}
		ev.Kind = kind
		switch kind {
		case "monster", "boss":
			m := makeMonster(r, d, kind == "boss")
			ev.Name, ev.FoeHP = m.name, m.hp
			hp, ev.Won, ev.Swings = fight(r, h.Class, s, hp, m)
			if ev.Won {
				if kind == "boss" {
					ev.Gold, ev.XP = pct(42, 72), int64(30*d.ID)
					ev.Item = drop(45)
					res.Cleared = true
				} else {
					ev.Gold, ev.XP = pct(8, 16), int64(8*d.ID)
					ev.Item = drop(6)
				}
			}
		case "trap":
			ev.Name = pick(r, []string{"Pit trap", "Swinging blade", "Poison darts", "Collapsing floor"})
			ev.Target = 10 + 3*d.ID
			ev.Roll = r.Range(1, 20) + h.Dex + s.Level/3
			if ev.Roll >= ev.Target {
				ev.Won, ev.XP = true, int64(2*d.ID)
			} else {
				ev.Dmg = max(1, 4+4*d.ID-s.Def/2)
				hp -= ev.Dmg
			}
		case "chest":
			ev.Name = pick(r, []string{"Old chest", "Bone pile", "Merchant's pack", "Gilded urn"})
			ev.Won, ev.Gold = true, pct(15, 35)
			ev.Item = drop(12)
		case "shrine":
			ev.Name = pick(r, []string{"Healing spring", "Forgotten altar", "Glowing runes"})
			ev.Heal = min(s.MaxHP-hp, s.MaxHP*3/10)
			ev.Won = true
			hp += ev.Heal
		}
		hp = max(hp, 0)
		ev.HP = hp
		res.Found += ev.Gold
		res.XP += ev.XP
		res.Events = append(res.Events, ev)
	}

	gold := res.Found
	if hp <= 0 {
		res.Survived = false
		gold /= 2 // fleeing the dungeon costs half the gold carried
	} else if res.Cleared {
		res.XP += int64(10 * d.ID)
	}
	gold = min(gold, d.Fee*maxPayoutMul)
	res.Payout = gold / payoutStep * payoutStep
	return res
}

// fight runs one battle; it returns the hero's HP, whether the hero won and
// the swings. Battles stop after 30 rounds: the foe slinks away and the hero
// gets nothing for it.
func fight(r *RNG, class string, s Stats, hp int, m monster) (int, bool, []Swing) {
	var sw []Swing
	foe := m.hp
	heroHits := func(kind string, mult10 int) {
		if class == "Mage" && kind == "" && r.Chance(s.Spell) {
			d := s.Atk*18/10 + r.Range(1, 6)
			foe -= d
			sw = append(sw, Swing{Hero: true, Kind: "spell", Dmg: d, HeroHP: hp, FoeHP: max(foe, 0)})
			return
		}
		if !r.Chance(clamp(s.Hit-m.evade, 35, 95)) {
			sw = append(sw, Swing{Hero: true, Kind: "miss", HeroHP: hp, FoeHP: max(foe, 0)})
			return
		}
		d := max(1, s.Atk+r.Range(1, 6)-3-m.def) * mult10 / 10
		k := kind
		if k == "" {
			k = "hit"
		}
		if r.Chance(s.Crit) {
			d, k = d*2, "crit"
		}
		foe -= d
		sw = append(sw, Swing{Hero: true, Kind: k, Dmg: d, HeroHP: hp, FoeHP: max(foe, 0)})
	}
	for round := 1; round <= 30; round++ {
		switch {
		case round == 1 && class == "Ranger":
			heroHits("volley", 10)
			if foe > 0 {
				heroHits("", 10)
			}
		case round == 1 && class == "Rogue":
			heroHits("ambush", 15)
		default:
			heroHits("", 10)
		}
		if foe <= 0 {
			return hp, true, sw
		}
		if !r.Chance(clamp(80-s.Dodge, 30, 95)) {
			sw = append(sw, Swing{Kind: "miss", HeroHP: hp, FoeHP: foe})
			continue
		}
		d := max(1, m.atk+r.Range(1, 6)-3-s.Def)
		k := "hit"
		if class == "Warrior" && r.Chance(15) {
			d, k = (d+1)/2, "block"
		}
		hp -= d
		sw = append(sw, Swing{Kind: k, Dmg: d, HeroHP: max(hp, 0), FoeHP: foe})
		if hp <= 0 {
			return 0, false, sw
		}
	}
	return hp, false, sw
}
