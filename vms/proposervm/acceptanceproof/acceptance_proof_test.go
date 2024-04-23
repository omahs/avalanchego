// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package acceptanceproof

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow/validators"
	"github.com/ava-labs/avalanchego/vms/platformvm/warp"
	"github.com/ava-labs/avalanchego/vms/platformvm/warp/payload"
)

var (
	_      warp.Signature = (*testSignature)(nil)
	errFoo                = errors.New("foo")
)

func TestVerify(t *testing.T) {
	tests := []struct {
		name     string
		proof    AcceptanceProof
		expected error
	}{
		{
			name: "passes verification",
			proof: func() AcceptanceProof {
				hash, err := payload.NewHash(ids.Empty)
				require.NoError(t, err)

				signature := &testSignature{}
				return New(0, ids.Empty, hash, signature)
			}(),
		},
		{
			name: "fails verification",
			proof: func() AcceptanceProof {
				hash, err := payload.NewHash(ids.Empty)
				require.NoError(t, err)

				signature := &testSignature{
					VerifyF: func(
						context.Context,
						*warp.UnsignedMessage,
						uint32,
						validators.State,
						uint64,
						uint64,
						uint64,
					) error {
						return errFoo
					},
				}
				return New(0, ids.Empty, hash, signature)
			}(),
			expected: errFoo,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			err := Verify(
				context.Background(),
				tt.proof,
				0,
				nil,
				0,
				0,
				0,
			)
			require.ErrorIs(err, tt.expected)
		})
	}
}

type testSignature struct {
	warp.Signature
	VerifyF func(
		ctx context.Context,
		msg *warp.UnsignedMessage,
		networkID uint32,
		pChainState validators.State,
		pChainHeight uint64,
		quorumNum uint64,
		quorumDen uint64,
	) error
}

func (t testSignature) Verify(
	ctx context.Context,
	msg *warp.UnsignedMessage,
	networkID uint32,
	pChainState validators.State,
	pChainHeight uint64,
	quorumNum uint64,
	quorumDen uint64,
) error {
	if t.VerifyF != nil {
		return t.VerifyF(
			ctx,
			msg,
			networkID,
			pChainState,
			pChainHeight,
			quorumNum,
			quorumDen,
		)
	}

	return nil
}
