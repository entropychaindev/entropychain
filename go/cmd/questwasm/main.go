//go:build js && wasm

// Command questwasm exposes the Entropy Quest rules to the browser, so the
// game page can recompute any run with exactly the code the game master runs.
//
//	GOOS=js GOARCH=wasm go build -o quest.wasm ./cmd/questwasm
//
// It defines two globals that take and return JSON strings:
//
//	questRun({hero, progress, gear, dungeon, beacon, tx}) -> Result
//	questSummon({beacon, tx}) -> Hero
//	questStats({hero, xp, gear}) -> Stats
package main

import (
	"encoding/json"
	"syscall/js"

	"github.com/entropychaindev/entropychain/go/internal/quest"
)

type runInput struct {
	Hero     quest.Hero     `json:"hero"`
	Progress quest.Progress `json:"progress"`
	Gear     []quest.Item   `json:"gear"`
	Dungeon  int            `json:"dungeon"`
	Beacon   string         `json:"beacon"`
	Tx       string         `json:"tx"`
}

func reply(v any, err error) any {
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(b)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func main() {
	js.Global().Set("questRun", js.FuncOf(func(_ js.Value, args []js.Value) any {
		var in runInput
		if err := json.Unmarshal([]byte(args[0].String()), &in); err != nil {
			return reply(nil, err)
		}
		d, _ := quest.DungeonByID(in.Dungeon)
		return reply(quest.Run(in.Hero, in.Progress, in.Gear, d, in.Beacon, in.Tx), nil)
	}))
	js.Global().Set("questSummon", js.FuncOf(func(_ js.Value, args []js.Value) any {
		var in struct{ Beacon, Tx string }
		if err := json.Unmarshal([]byte(args[0].String()), &in); err != nil {
			return reply(nil, err)
		}
		return reply(quest.Summon(in.Beacon, in.Tx), nil)
	}))
	js.Global().Set("questStats", js.FuncOf(func(_ js.Value, args []js.Value) any {
		var in struct {
			Hero quest.Hero   `json:"hero"`
			XP   int64        `json:"xp"`
			Gear []quest.Item `json:"gear"`
		}
		if err := json.Unmarshal([]byte(args[0].String()), &in); err != nil {
			return reply(nil, err)
		}
		return reply(quest.Effective(in.Hero, quest.Level(in.XP), in.Gear), nil)
	}))
	js.Global().Set("questRulesVersion", quest.RulesVersion)
	select {}
}
