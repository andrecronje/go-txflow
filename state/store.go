package state

import (
	"fmt"

	"github.com/Fantom-foundation/go-txflow/types"
	abci "github.com/tendermint/tendermint/abci/types"
	cmn "github.com/tendermint/tendermint/libs/common"
	sm "github.com/tendermint/tendermint/state"
	ttypes "github.com/tendermint/tendermint/types"
	dbm "github.com/tendermint/tm-cmn/db"
)

const (
	// persist validators every valSetCheckpointInterval blocks to avoid
	// LoadValidators taking too much time.
	// https://github.com/tendermint/tendermint/pull/3438
	// 100000 results in ~ 100ms to get 100 validators (see BenchmarkLoadValidators)
	valSetCheckpointInterval = 100000
)

//------------------------------------------------------------------------

func calcValidatorsKey(height int64) []byte {
	return []byte(fmt.Sprintf("validatorsKey:%v", height))
}

func calcConsensusParamsKey(height int64) []byte {
	return []byte(fmt.Sprintf("consensusParamsKey:%v", height))
}

func calcABCIResponsesKey(height int64) []byte {
	return []byte(fmt.Sprintf("abciResponsesKey:%v", height))
}

// LoadStateFromDBOrGenesisFile loads the most recent state from the database,
// or creates a new one from the given genesisFilePath and persists the result
// to the database.
func LoadStateFromDBOrGenesisFile(stateDB dbm.DB, genesisFilePath string) (State, error) {
	state := LoadState(stateDB)
	if state.IsEmpty() {
		var err error
		state, err = MakeGenesisStateFromFile(genesisFilePath)
		if err != nil {
			return state, err
		}
		SaveState(stateDB, state)
	}

	return state, nil
}

// LoadStateFromDBOrGenesisDoc loads the most recent state from the database,
// or creates a new one from the given genesisDoc and persists the result
// to the database.
func LoadStateFromDBOrGenesisDoc(stateDB dbm.DB, genesisDoc *ttypes.GenesisDoc) (State, error) {
	state := LoadState(stateDB)
	if state.IsEmpty() {
		var err error
		state, err = MakeGenesisState(genesisDoc)
		if err != nil {
			return state, err
		}
		SaveState(stateDB, state)
	}

	return state, nil
}

// LoadState loads the State from the database.
func LoadState(db dbm.DB) State {
	return loadState(db, stateKey)
}

func loadState(db dbm.DB, key []byte) (state State) {
	buf := db.Get(key)
	if len(buf) == 0 {
		return state
	}

	err := cdc.UnmarshalBinaryBare(buf, &state)
	if err != nil {
		// DATA HAS BEEN CORRUPTED OR THE SPEC HAS CHANGED
		cmn.Exit(fmt.Sprintf(`LoadState: Data has been corrupted or its spec has changed:
                %v\n`, err))
	}
	// TODO: ensure that buf is completely read.

	return state
}

// SaveState persists the State, the ValidatorsInfo, and the ConsensusParamsInfo to the database.
// This flushes the writes (e.g. calls SetSync).
func SaveState(db dbm.DB, state State) {
	saveState(db, state, stateKey)
}

func saveState(db dbm.DB, state State, key []byte) {
	nextHeight := state.LastBlockHeight + 1
	// If first block, save validators for block 1.
	if nextHeight == 1 {
		// This extra logic due to Tendermint validator set changes being delayed 1 block.
		// It may get overwritten due to InitChain validator updates.
		lastHeightVoteChanged := int64(1)
		saveValidatorsInfo(db, nextHeight, lastHeightVoteChanged, state.Validators)
	}
	// Save next validators.
	saveValidatorsInfo(db, nextHeight+1, state.LastHeightValidatorsChanged, state.NextValidators)
	// Save next consensus params.
	saveConsensusParamsInfo(db, nextHeight, state.LastHeightConsensusParamsChanged, state.ConsensusParams)
	db.SetSync(key, state.Bytes())
}

// NewABCIResponses returns a new ABCIResponses
func NewABCIResponses(block *types.Block) *sm.ABCIResponses {
	resDeliverTxs := make([]*abci.ResponseDeliverTx, block.NumTxs)
	if block.NumTxs == 0 {
		// This makes Amino encoding/decoding consistent.
		resDeliverTxs = nil
	}
	return &sm.ABCIResponses{
		DeliverTx: resDeliverTxs,
	}
}

// SaveABCIResponses persists the ABCIResponses to the database.
// This is useful in case we crash after app.Commit and before s.Save().
// Responses are indexed by height so they can also be loaded later to produce Merkle proofs.
func saveABCIResponses(db dbm.DB, height int64, abciResponses *sm.ABCIResponses) {
	db.SetSync(calcABCIResponsesKey(height), abciResponses.Bytes())
}

// saveValidatorsInfo persists the validator set.
//
// `height` is the effective height for which the validator is responsible for
// signing. It should be called from s.Save(), right before the state itself is
// persisted.
func saveValidatorsInfo(db dbm.DB, height, lastHeightChanged int64, valSet *ttypes.ValidatorSet) {
	if lastHeightChanged > height {
		panic("LastHeightChanged cannot be greater than ValidatorsInfo height")
	}
	valInfo := &sm.ValidatorsInfo{
		LastHeightChanged: lastHeightChanged,
	}
	// Only persist validator set if it was updated or checkpoint height (see
	// valSetCheckpointInterval) is reached.
	if height == lastHeightChanged || height%valSetCheckpointInterval == 0 {
		valInfo.ValidatorSet = valSet
	}
	db.Set(calcValidatorsKey(height), valInfo.Bytes())
}

// saveConsensusParamsInfo persists the consensus params for the next block to disk.
// It should be called from s.Save(), right before the state itself is persisted.
// If the consensus params did not change after processing the latest block,
// only the last height for which they changed is persisted.
func saveConsensusParamsInfo(db dbm.DB, nextHeight, changeHeight int64, params ttypes.ConsensusParams) {
	paramsInfo := &sm.ConsensusParamsInfo{
		LastHeightChanged: changeHeight,
	}
	if changeHeight == nextHeight {
		paramsInfo.ConsensusParams = params
	}
	db.Set(calcConsensusParamsKey(nextHeight), paramsInfo.Bytes())
}
