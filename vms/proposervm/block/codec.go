// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package block

import (
	"errors"
	"math"

	"github.com/ava-labs/avalanchego/codec"
	"github.com/ava-labs/avalanchego/codec/linearcodec"
)

const CodecVersion = 0

var Codec codec.Manager

func init() {
	lc := linearcodec.NewDefault()
	// The maximum block size is enforced by the p2p message size limit.
	// See: [constants.DefaultMaxMessageSize]
	Codec = codec.NewManager(math.MaxInt)

	err := errors.Join(
		lc.RegisterType(&statelessBlock[statelessUnsignedBlockV0]{}),
		lc.RegisterType(&option{}),
		lc.RegisterType(&statelessBlock[statelessUnsignedBlock]{}),
		Codec.RegisterCodec(CodecVersion, lc),
	)
	if err != nil {
		panic(err)
	}
}
