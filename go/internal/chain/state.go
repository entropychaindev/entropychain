package chain

import (
	"errors"
	"math"

	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/params"
)

// Token is a fungible token's registry entry.
type Token struct {
	Name        string `json:"name"`
	TotalSupply int64  `json:"total_supply"`
	Decimals    int64  `json:"decimals"`
	Creator     string `json:"creator"`
}

// Collection is an NFT collection.
type Collection struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	Creator         string `json:"creator"`
	CreatedAtHeight int64  `json:"created_at_height"`
}

// NFT is a minted token.
type NFT struct {
	Owner               string `json:"owner"`
	MetadataURI         string `json:"metadata_uri"`
	Traits              any    `json:"traits"`
	MintedAtHeight      int64  `json:"minted_at_height"`
	Creator             string `json:"creator"`
	EntropySourceHeight *int64 `json:"entropy_source_height,omitempty"`
	// TraitsPending is set on read for a random mint whose source block
	// has not been mined yet (traits are then null).
	TraitsPending bool `json:"traits_pending,omitempty"`
}

// Lottery is a lottery's state.
type Lottery struct {
	Organizer       string   `json:"organizer"`
	PrizeAmount     int64    `json:"prize_amount"`
	TicketPrice     int64    `json:"ticket_price"`
	DrawHeight      int64    `json:"draw_height"`
	DrawBlocks      int64    `json:"draw_blocks"`
	Token           string   `json:"token"`
	Entrants        []string `json:"entrants"`
	EntryPool       int64    `json:"entry_pool"`
	Drawn           bool     `json:"drawn"`
	Winner          *string  `json:"winner"`
	EntropyUsed     *string  `json:"entropy_used"`
	CreatedAtHeight int64    `json:"created_at_height"`
	DrawnAtHeight   *int64   `json:"drawn_at_height,omitempty"`
}

type nftKey struct{ collection, token string }

// HistoryEntry locates a transaction in the chain.
type HistoryEntry struct {
	Height int64
	Hash   string
}

type txLoc struct {
	height int64
	tx     *core.Transaction
}

// State is the account/token/NFT/lottery state derived from the main chain.
//
// Every mutation goes through a method that pushes an undo closure onto the
// journal. Rolling the journal back restores the exact previous state; this
// gives atomic block application, dry-run validation for the miner, and
// fast reorgs without replaying from genesis. Registry values (tokens,
// NFTs, lotteries...) are copy-on-write: they are never mutated in place,
// so a pointer held by an undo closure or an RPC reader stays valid.
type State struct {
	balances    map[string]map[string]int64
	tokens      map[string]*Token
	tokenOrder  []string
	collections map[string]*Collection
	nfts        map[nftKey]*NFT
	lotteries   map[string]*Lottery
	txSeen      map[string]struct{}
	txIndex     map[string][]HistoryEntry
	txByHash    map[string]txLoc

	journal []func()
}

func newState() *State {
	return &State{
		balances:    map[string]map[string]int64{},
		tokens:      map[string]*Token{},
		collections: map[string]*Collection{},
		nfts:        map[nftKey]*NFT{},
		lotteries:   map[string]*Lottery{},
		txSeen:      map[string]struct{}{},
		txIndex:     map[string][]HistoryEntry{},
		txByHash:    map[string]txLoc{},
	}
}

// mark returns a journal position for rollback.
func (s *State) mark() int { return len(s.journal) }

// rollback undoes every mutation after mark.
func (s *State) rollback(mark int) {
	for i := len(s.journal) - 1; i >= mark; i-- {
		s.journal[i]()
	}
	s.journal = s.journal[:mark]
}

// take removes and returns the journal entries after mark, so the caller
// can keep them as a block's undo record.
func (s *State) take(mark int) []func() {
	j := append([]func(){}, s.journal[mark:]...)
	s.journal = s.journal[:mark]
	return j
}

// undo runs a block's undo record.
func undo(j []func()) {
	for i := len(j) - 1; i >= 0; i-- {
		j[i]()
	}
}

// ---- Reads ----

func (s *State) balance(addr, token string) int64 { return s.balances[addr][token] }

func (s *State) seen(hash string) bool {
	_, ok := s.txSeen[hash]
	return ok
}

// ---- Journaled writes ----

var errOverflow = errors.New("amount overflow")

func addChecked(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, errOverflow
	}
	return a + b, nil
}

func (s *State) addBalance(addr, token string, delta int64) error {
	if delta == 0 {
		return nil
	}
	m := s.balances[addr]
	created := m == nil
	if created {
		m = map[string]int64{}
		s.balances[addr] = m
	}
	old, had := m[token]
	nv, err := addChecked(old, delta)
	if err != nil {
		if created {
			delete(s.balances, addr)
		}
		return err
	}
	if nv < 0 {
		if created {
			delete(s.balances, addr)
		}
		return errors.New("balance would go negative")
	}
	m[token] = nv
	s.journal = append(s.journal, func() {
		if had {
			m[token] = old
		} else {
			delete(m, token)
		}
		if created {
			delete(s.balances, addr)
		}
	})
	return nil
}

func (s *State) setToken(sym string, t *Token) {
	old, had := s.tokens[sym]
	s.tokens[sym] = t
	if !had {
		s.tokenOrder = append(s.tokenOrder, sym)
	}
	s.journal = append(s.journal, func() {
		if had {
			s.tokens[sym] = old
		} else {
			delete(s.tokens, sym)
			s.tokenOrder = s.tokenOrder[:len(s.tokenOrder)-1]
		}
	})
}

func (s *State) setCollection(id string, c *Collection) {
	old, had := s.collections[id]
	s.collections[id] = c
	s.journal = append(s.journal, func() {
		if had {
			s.collections[id] = old
		} else {
			delete(s.collections, id)
		}
	})
}

func (s *State) setNFT(k nftKey, n *NFT) {
	old, had := s.nfts[k]
	s.nfts[k] = n
	s.journal = append(s.journal, func() {
		if had {
			s.nfts[k] = old
		} else {
			delete(s.nfts, k)
		}
	})
}

func (s *State) setLottery(id string, l *Lottery) {
	old, had := s.lotteries[id]
	s.lotteries[id] = l
	s.journal = append(s.journal, func() {
		if had {
			s.lotteries[id] = old
		} else {
			delete(s.lotteries, id)
		}
	})
}

func (s *State) markSeen(hash string) {
	s.txSeen[hash] = struct{}{}
	s.journal = append(s.journal, func() { delete(s.txSeen, hash) })
}

func (s *State) recordHistory(tx *core.Transaction, height int64, addrs []string) {
	h := tx.Hash()
	old, had := s.txByHash[h]
	s.txByHash[h] = txLoc{height, tx}
	added := make([]string, 0, len(addrs))
	seen := map[string]bool{}
	for _, a := range addrs {
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		s.txIndex[a] = append(s.txIndex[a], HistoryEntry{height, h})
		added = append(added, a)
	}
	s.journal = append(s.journal, func() {
		for _, a := range added {
			l := s.txIndex[a][:len(s.txIndex[a])-1]
			if len(l) == 0 {
				delete(s.txIndex, a)
			} else {
				s.txIndex[a] = l
			}
		}
		if had {
			s.txByHash[h] = old
		} else {
			delete(s.txByHash, h)
		}
	})
}

// ---- Copy helpers for copy-on-write ----

func (l *Lottery) clone() *Lottery {
	c := *l
	// Never nil: an empty lottery must serialize as [] (clients read .length).
	c.Entrants = append(make([]string, 0, len(l.Entrants)), l.Entrants...)
	return &c
}

func isNative(token string) bool { return token == params.NativeToken }
