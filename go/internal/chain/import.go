package chain

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/entropychaindev/entropychain/go/internal/core"
)

// ImportPythonChain loads a Python node's chaindata/chain.json and adds its
// main chain block by block with full validation (PoW included). It stops
// at the first block the Go rules reject and reports it.
func (c *Chain) ImportPythonChain(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var data struct {
		Blocks    []json.RawMessage `json:"blocks"`
		MainChain []string          `json:"main_chain"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	byHash := make(map[string]*core.Block, len(data.Blocks))
	for i, rb := range data.Blocks {
		var b core.Block
		if err := json.Unmarshal(rb, &b); err != nil {
			return 0, fmt.Errorf("block #%d in file: %w", i, err)
		}
		byHash[b.Hash()] = &b
	}
	if len(data.MainChain) == 0 || data.MainChain[0] != c.GenesisHash() {
		return 0, errors.New("chain.json is from a different network (genesis mismatch)")
	}
	imported := 0
	for height, h := range data.MainChain[1:] {
		b, ok := byHash[h]
		if !ok {
			return imported, fmt.Errorf("main_chain references missing block %s", h)
		}
		if _, err := c.addBlock(b, false); err != nil && !errors.Is(err, ErrKnown) {
			return imported, fmt.Errorf("block %d (%s…) rejected by Go rules: %w", height+1, h[:12], err)
		}
		imported++
		if imported%500 == 0 {
			log.Printf("  imported %d / %d blocks", imported, len(data.MainChain)-1)
		}
	}
	return imported, nil
}
