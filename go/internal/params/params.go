// Package params holds the network identity and consensus constants.
//
// These values must match the Python reference implementation exactly
// (genesis_config.py, protocol.py, consensus.py, blockchain.py). Changing
// any of them on a running network forks the chain.
package params

// ---- Network identity (protocol.py / genesis_config.py) ----

const (
	ProtocolVersion = 1
	NetworkID       = "entropy-chain-testnet-4"

	GenesisMessage    = "EntropyChain testnet-4 — beacon v2 — 133.7M supply · Oct 2026"
	GenesisTimestamp  = 1791590400.0 // 2026-10-10 00:00:00 UTC
	GenesisNetworkTag = "testnet-4"
	GenesisOperator   = "ZZBolt / @UnionOfPlanets"
)

// ---- Consensus (consensus.py) ----

// Argon2id cost parameters. ArgonMemoryKB is a var so --testnet-fast can
// lower it for local development (doing so breaks consensus with real peers).
var (
	ArgonTimeCost    uint32 = 1
	ArgonMemoryKB    uint32 = 16 * 1024
	ArgonParallelism uint8  = 1
)

const (
	ArgonHashLen     = 32
	TargetBlockTime  = 30 // seconds
	DifficultyWindow = 20 // blocks between retargets
)

// GenesisDifficulty is a var only so tests can mine instantly. Changing it
// changes the genesis hash.
var GenesisDifficulty uint64 = 1000

// ---- Monetary policy (blockchain.py) ----

const (
	NativeToken     = "ENTRO"
	InitialReward   = 50 * 100_000_000
	HalvingInterval = 1_337_000
)

// ---- Rules added by the Go node (stricter than Python; see README) ----

const (
	// MedianTimeSpan is how many past blocks feed the median-time-past rule.
	MedianTimeSpan = 11
	// MaxFutureBlockTime is how far ahead of local time a new block may be.
	MaxFutureBlockTime = 2 * 60 * 60 // seconds
	// MaxSymbolLen bounds token symbols.
	MaxSymbolLen = 32
)

// EnableTestnetFast relaxes PoW cost for local development, mirroring
// node.py --testnet-fast. (node.py also sets GENESIS_DIFFICULTY = 200, but
// blockchain.py has already imported the old value by then, so Python's fast
// mode only changes Argon2 memory. We match that.)
func EnableTestnetFast() {
	ArgonMemoryKB = 1024
}
