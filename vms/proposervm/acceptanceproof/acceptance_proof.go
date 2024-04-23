// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package acceptanceproof

import (
	"context"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow/validators"
	"github.com/ava-labs/avalanchego/vms/platformvm/warp"
	"github.com/ava-labs/avalanchego/vms/platformvm/warp/payload"
)

// TODO codec

// New returns an AcceptanceProof of blockID
func New(
	networkID uint32,
	chainID ids.ID,
	blockID *payload.Hash,
	signature warp.Signature,
) AcceptanceProof {
	return AcceptanceProof{
		signature: signature,
		message: &warp.UnsignedMessage{
			NetworkID:     networkID,
			SourceChainID: chainID,
			Payload:       blockID.Bytes(),
		},
	}
}

// AcceptanceProof is a proof of block acceptance in consensus
type AcceptanceProof struct {
	signature warp.Signature
	message   *warp.UnsignedMessage
}

// Verify returns an error if less than quorumNum/quorumDen have signed the
// warp signature.
func Verify(
	ctx context.Context,
	proof AcceptanceProof,
	networkID uint32,
	pChainState validators.State,
	pChainHeight uint64,
	quorumNum uint64,
	quorumDen uint64,
) error {
	return proof.signature.Verify(
		ctx,
		proof.message,
		networkID,
		pChainState,
		pChainHeight,
		quorumNum,
		quorumDen,
	)
}
