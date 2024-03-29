package consensus

import (
	amino "github.com/tendermint/go-amino"
	"github.com/tendermint/tendermint/consensus"
	"github.com/tendermint/tendermint/types"
)

var cdc = amino.NewCodec()

func init() {
	RegisterConsensusMessages(cdc)
	consensus.RegisterWALMessages(cdc)
	types.RegisterBlockAmino(cdc)
}
