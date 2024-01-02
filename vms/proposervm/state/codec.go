// Copyright (C) 2019-2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package state

import (
	"math"

	"github.com/ava-labs/avalanchego/codec"
	"github.com/ava-labs/avalanchego/codec/linearcodec"
)

const CodecVersion = 0

var Codec codec.Manager

func init() {
	lc := linearcodec.NewCustomMaxLength(math.MaxUint32)
	Codec = codec.NewManager(math.MaxInt32)

	err := Codec.RegisterCodec(CodecVersion, lc)
	if err != nil {
		panic(err)
	}
}
