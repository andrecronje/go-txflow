package types

import (
	// it is ok to use math/rand here: we do not need a cryptographically secure random
	// number generator here and we can run the tests a bit faster
	"crypto/rand"
	"math"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tendermint/tendermint/crypto"
	"github.com/tendermint/tendermint/crypto/tmhash"
	cmn "github.com/tendermint/tendermint/libs/common"
	"github.com/tendermint/tendermint/types"
	"github.com/tendermint/tendermint/version"
)

// NOTE: privValidators are in order
func randVoteSet(height int64, round int, type_ types.SignedMsgType, numValidators int, votingPower int64) (*types.VoteSet, *types.ValidatorSet, []types.PrivValidator) {
	valSet, privValidators := types.RandValidatorSet(numValidators, votingPower)
	return types.NewVoteSet("test_chain_id", height, round, type_, valSet), valSet, privValidators
}

func TestMain(m *testing.M) {
	types.RegisterMockEvidences(cdc)

	code := m.Run()
	os.Exit(code)
}

func TestBlockAddEvidence(t *testing.T) {
	txs := []types.Tx{types.Tx("foo"), types.Tx("bar")}
	lastID := makeBlockIDRandom()
	h := int64(3)

	voteSet, valSet, vals := randVoteSet(h-1, 1, types.PrecommitType, 10, 1)
	commit, err := types.MakeCommit(lastID, h-1, 1, voteSet, vals)
	require.NoError(t, err)

	ev := types.NewMockGoodEvidence(h, 0, valSet.Validators[0].Address)
	evList := []types.Evidence{ev}

	block := MakeBlock(h, txs, nil, commit, evList)
	require.NotNil(t, block)
	require.Equal(t, 1, len(block.Evidence.Evidence))
	require.NotNil(t, block.EvidenceHash)
}

func TestBlockValidateBasic(t *testing.T) {
	require.Error(t, (*Block)(nil).ValidateBasic())

	txs := []types.Tx{types.Tx("foo"), types.Tx("bar")}
	lastID := makeBlockIDRandom()
	h := int64(3)

	voteSet, valSet, vals := randVoteSet(h-1, 1, types.PrecommitType, 10, 1)
	commit, err := types.MakeCommit(lastID, h-1, 1, voteSet, vals)
	require.NoError(t, err)

	ev := types.NewMockGoodEvidence(h, 0, valSet.Validators[0].Address)
	evList := []types.Evidence{ev}

	testCases := []struct {
		testName      string
		malleateBlock func(*Block)
		expErr        bool
	}{
		{"Make Block", func(blk *Block) {}, false},
		{"Make Block w/ proposer Addr", func(blk *Block) { blk.ProposerAddress = valSet.GetProposer().Address }, false},
		{"Negative Height", func(blk *Block) { blk.Height = -1 }, true},
		{"Increase NumTxs", func(blk *Block) { blk.NumTxs++ }, true},
		{"Remove 1/2 the commits", func(blk *Block) {
			blk.LastCommit.Precommits = commit.Precommits[:commit.Size()/2]
		}, true},
		{"Remove LastCommitHash", func(blk *Block) { blk.LastCommitHash = []byte("something else") }, true},
		{"Tampered Data", func(blk *Block) {
			blk.Data.Txs[0] = types.Tx("something else")
			blk.Data.hash = nil // clear hash or change wont be noticed
		}, true},
		{"Tampered DataHash", func(blk *Block) {
			blk.DataHash = cmn.RandBytes(len(blk.DataHash))
		}, true},
		{"Tampered EvidenceHash", func(blk *Block) {
			blk.EvidenceHash = []byte("something else")
		}, true},
	}
	for i, tc := range testCases {
		t.Run(tc.testName, func(t *testing.T) {
			block := MakeBlock(h, txs, nil, commit, evList)
			block.ProposerAddress = valSet.GetProposer().Address
			tc.malleateBlock(block)
			err = block.ValidateBasic()
			assert.Equal(t, tc.expErr, err != nil, "#%d: %v", i, err)
		})
	}
}

func TestBlockHash(t *testing.T) {
	assert.Nil(t, (*Block)(nil).Hash())
	assert.Nil(t, MakeBlock(int64(3), []types.Tx{types.Tx("Hello World")}, nil, nil, nil).Hash())
}

func TestBlockMakePartSet(t *testing.T) {
	assert.Nil(t, (*Block)(nil).MakePartSet(2))

	partSet := MakeBlock(int64(3), []types.Tx{types.Tx("Hello World")}, nil, nil, nil).MakePartSet(1024)
	assert.NotNil(t, partSet)
	assert.Equal(t, 1, partSet.Total())
}

func TestBlockMakePartSetWithEvidence(t *testing.T) {
	assert.Nil(t, (*Block)(nil).MakePartSet(2))

	lastID := makeBlockIDRandom()
	h := int64(3)

	voteSet, valSet, vals := randVoteSet(h-1, 1, types.PrecommitType, 10, 1)
	commit, err := types.MakeCommit(lastID, h-1, 1, voteSet, vals)
	require.NoError(t, err)

	ev := types.NewMockGoodEvidence(h, 0, valSet.Validators[0].Address)
	evList := []types.Evidence{ev}

	partSet := MakeBlock(h, []types.Tx{types.Tx("Hello World")}, nil, commit, evList).MakePartSet(1024)
	assert.NotNil(t, partSet)
	assert.Equal(t, 3, partSet.Total())
}

func TestBlockHashesTo(t *testing.T) {
	assert.False(t, (*Block)(nil).HashesTo(nil))

	lastID := makeBlockIDRandom()
	h := int64(3)
	voteSet, valSet, vals := randVoteSet(h-1, 1, types.PrecommitType, 10, 1)
	commit, err := types.MakeCommit(lastID, h-1, 1, voteSet, vals)
	require.NoError(t, err)

	ev := types.NewMockGoodEvidence(h, 0, valSet.Validators[0].Address)
	evList := []types.Evidence{ev}

	block := MakeBlock(h, []types.Tx{types.Tx("Hello World")}, nil, commit, evList)
	block.ValidatorsHash = valSet.Hash()
	assert.False(t, block.HashesTo([]byte{}))
	assert.False(t, block.HashesTo([]byte("something else")))
	assert.True(t, block.HashesTo(block.Hash()))
}

func TestBlockSize(t *testing.T) {
	size := MakeBlock(int64(3), []types.Tx{types.Tx("Hello World")}, nil, nil, nil).Size()
	if size <= 0 {
		t.Fatal("Size of the block is zero or negative")
	}
}

func TestBlockString(t *testing.T) {
	assert.Equal(t, "nil-Block", (*Block)(nil).String())
	assert.Equal(t, "nil-Block", (*Block)(nil).StringIndented(""))
	assert.Equal(t, "nil-Block", (*Block)(nil).StringShort())

	block := MakeBlock(int64(3), []types.Tx{types.Tx("Hello World")}, nil, nil, nil)
	assert.NotEqual(t, "nil-Block", block.String())
	assert.NotEqual(t, "nil-Block", block.StringIndented(""))
	assert.NotEqual(t, "nil-Block", block.StringShort())
}

func makeBlockIDRandom() types.BlockID {
	blockHash := make([]byte, tmhash.Size)
	partSetHash := make([]byte, tmhash.Size)
	rand.Read(blockHash)   //nolint: gosec
	rand.Read(partSetHash) //nolint: gosec
	blockPartsHeader := types.PartSetHeader{123, partSetHash}
	return types.BlockID{blockHash, blockPartsHeader}
}

func makeBlockID(hash []byte, partSetSize int, partSetHash []byte) types.BlockID {
	return types.BlockID{
		Hash: hash,
		PartsHeader: types.PartSetHeader{
			Total: partSetSize,
			Hash:  partSetHash,
		},
	}

}

var nilBytes []byte

func TestNilHeaderHashDoesntCrash(t *testing.T) {
	assert.Equal(t, []byte((*types.Header)(nil).Hash()), nilBytes)
	assert.Equal(t, []byte((new(types.Header)).Hash()), nilBytes)
}

func TestNilDataHashDoesntCrash(t *testing.T) {
	assert.Equal(t, []byte((*Data)(nil).Hash()), nilBytes)
	assert.Equal(t, []byte(new(Data).Hash()), nilBytes)
}

func TestCommit(t *testing.T) {
	lastID := makeBlockIDRandom()
	h := int64(3)
	voteSet, _, vals := randVoteSet(h-1, 1, types.PrecommitType, 10, 1)
	commit, err := types.MakeCommit(lastID, h-1, 1, voteSet, vals)
	require.NoError(t, err)

	assert.Equal(t, h-1, commit.Height())
	assert.Equal(t, 1, commit.Round())
	if commit.Size() <= 0 {
		t.Fatalf("commit %v has a zero or negative size: %d", commit, commit.Size())
	}

	require.NotNil(t, commit.BitArray())
	assert.Equal(t, cmn.NewBitArray(10).Size(), commit.BitArray().Size())

	assert.Equal(t, voteSet.GetByIndex(0), commit.GetByIndex(0))
	assert.True(t, commit.IsCommit())
}

func TestCommitValidateBasic(t *testing.T) {
	testCases := []struct {
		testName       string
		malleateCommit func(*types.Commit)
		expectErr      bool
	}{
		{"Random Commit", func(com *types.Commit) {}, false},
		{"Nil precommit", func(com *types.Commit) { com.Precommits[0] = nil }, false},
		{"Incorrect signature", func(com *types.Commit) { com.Precommits[0].Signature = []byte{0} }, false},
		{"Incorrect type", func(com *types.Commit) { com.Precommits[0].Type = types.PrevoteType }, true},
		{"Incorrect height", func(com *types.Commit) { com.Precommits[0].Height = int64(100) }, true},
		{"Incorrect round", func(com *types.Commit) { com.Precommits[0].Round = 100 }, true},
	}
	for _, tc := range testCases {
		t.Run(tc.testName, func(t *testing.T) {
			com := randCommit()
			tc.malleateCommit(com)
			assert.Equal(t, tc.expectErr, com.ValidateBasic() != nil, "Validate Basic had an unexpected result")
		})
	}
}

func TestMaxHeaderBytes(t *testing.T) {
	// Construct a UTF-8 string of MaxChainIDLen length using the supplementary
	// characters.
	// Each supplementary character takes 4 bytes.
	// http://www.i18nguy.com/unicode/supplementary-test.html
	maxChainID := ""
	for i := 0; i < types.MaxChainIDLen; i++ {
		maxChainID += "𠜎"
	}

	// time is varint encoded so need to pick the max.
	// year int, month Month, day, hour, min, sec, nsec int, loc *Location
	timestamp := time.Date(math.MaxInt64, 0, 0, 0, 0, 0, math.MaxInt64, time.UTC)

	h := types.Header{
		Version:            version.Consensus{Block: math.MaxInt64, App: math.MaxInt64},
		ChainID:            maxChainID,
		Height:             math.MaxInt64,
		Time:               timestamp,
		NumTxs:             math.MaxInt64,
		TotalTxs:           math.MaxInt64,
		LastBlockID:        makeBlockID(make([]byte, tmhash.Size), math.MaxInt64, make([]byte, tmhash.Size)),
		LastCommitHash:     tmhash.Sum([]byte("last_commit_hash")),
		DataHash:           tmhash.Sum([]byte("data_hash")),
		ValidatorsHash:     tmhash.Sum([]byte("validators_hash")),
		NextValidatorsHash: tmhash.Sum([]byte("next_validators_hash")),
		ConsensusHash:      tmhash.Sum([]byte("consensus_hash")),
		AppHash:            tmhash.Sum([]byte("app_hash")),
		LastResultsHash:    tmhash.Sum([]byte("last_results_hash")),
		EvidenceHash:       tmhash.Sum([]byte("evidence_hash")),
		ProposerAddress:    crypto.AddressHash([]byte("proposer_address")),
	}

	bz, err := cdc.MarshalBinaryLengthPrefixed(h)
	require.NoError(t, err)

	assert.EqualValues(t, types.MaxHeaderBytes, len(bz))
}

func randCommit() *types.Commit {
	lastID := makeBlockIDRandom()
	h := int64(3)
	voteSet, _, vals := randVoteSet(h-1, 1, types.PrecommitType, 10, 1)
	commit, err := types.MakeCommit(lastID, h-1, 1, voteSet, vals)
	if err != nil {
		panic(err)
	}
	return commit
}

func TestBlockMaxDataBytes(t *testing.T) {
	testCases := []struct {
		maxBytes      int64
		valsCount     int
		evidenceCount int
		panics        bool
		result        int64
	}{
		0: {-10, 1, 0, true, 0},
		1: {10, 1, 0, true, 0},
		2: {886, 1, 0, true, 0},
		3: {887, 1, 0, false, 0},
		4: {888, 1, 0, false, 1},
	}

	for i, tc := range testCases {
		if tc.panics {
			assert.Panics(t, func() {
				types.MaxDataBytes(tc.maxBytes, tc.valsCount, tc.evidenceCount)
			}, "#%v", i)
		} else {
			assert.Equal(t,
				tc.result,
				types.MaxDataBytes(tc.maxBytes, tc.valsCount, tc.evidenceCount),
				"#%v", i)
		}
	}
}

func TestBlockMaxDataBytesUnknownEvidence(t *testing.T) {
	testCases := []struct {
		maxBytes  int64
		valsCount int
		panics    bool
		result    int64
	}{
		0: {-10, 1, true, 0},
		1: {10, 1, true, 0},
		2: {984, 1, true, 0},
		3: {985, 1, false, 0},
		4: {986, 1, false, 1},
	}

	for i, tc := range testCases {
		if tc.panics {
			assert.Panics(t, func() {
				types.MaxDataBytesUnknownEvidence(tc.maxBytes, tc.valsCount)
			}, "#%v", i)
		} else {
			assert.Equal(t,
				tc.result,
				types.MaxDataBytesUnknownEvidence(tc.maxBytes, tc.valsCount),
				"#%v", i)
		}
	}
}

func TestCommitToVoteSet(t *testing.T) {
	lastID := makeBlockIDRandom()
	h := int64(3)

	voteSet, valSet, vals := randVoteSet(h-1, 1, types.PrecommitType, 10, 1)
	commit, err := types.MakeCommit(lastID, h-1, 1, voteSet, vals)
	assert.NoError(t, err)

	chainID := voteSet.ChainID()
	voteSet2 := types.CommitToVoteSet(chainID, commit, valSet)

	for i := 0; i < len(vals); i++ {
		vote1 := voteSet.GetByIndex(i)
		vote2 := voteSet2.GetByIndex(i)
		vote3 := commit.GetVote(i)

		vote1bz := cdc.MustMarshalBinaryBare(vote1)
		vote2bz := cdc.MustMarshalBinaryBare(vote2)
		vote3bz := cdc.MustMarshalBinaryBare(vote3)
		assert.Equal(t, vote1bz, vote2bz)
		assert.Equal(t, vote1bz, vote3bz)
	}
}
