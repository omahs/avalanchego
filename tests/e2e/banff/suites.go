// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

// Implements tests for the banff network upgrade.
package banff

import (
	"github.com/stretchr/testify/require"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/tests"
	"github.com/ava-labs/avalanchego/tests/fixture/e2e"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/units"
	"github.com/ava-labs/avalanchego/vms/components/avax"
	"github.com/ava-labs/avalanchego/vms/components/verify"
	"github.com/ava-labs/avalanchego/vms/secp256k1fx"
	"github.com/ava-labs/avalanchego/wallet/subnet/primary"

	ginkgo "github.com/onsi/ginkgo/v2"
)

type TestContext interface {
	// Mirrors the signature of ginkgo.By
	By(text string, callback func())
	Require() *require.Assertions
}

type ginkgoContext struct {
	require *require.Assertions
}

func (c *ginkgoContext) By(text string, callback func()) {
	ginkgo.By(text, callback)
}

func (c *ginkgoContext) Require() *require.Assertions {
	if c.require == nil {
		c.require = require.New(ginkgo.GinkgoT())
	}
	return c.require
}

func SendCustomAssets(tc TestContext, wallet primary.Wallet, outputAddress ids.ShortID) {
	require := tc.Require()

	// Get the P-chain and the X-chain wallets
	pWallet := wallet.P()
	xWallet := wallet.X()
	xBuilder := xWallet.Builder()
	xContext := xBuilder.Context()

	// Pull out useful constants to use when issuing transactions.
	xChainID := xContext.BlockchainID
	owner := &secp256k1fx.OutputOwners{
		Threshold: 1,
		Addrs: []ids.ShortID{
			outputAddress,
		},
	}

	var assetID ids.ID
	tc.By("create new X-chain asset", func() {
		assetTx, err := xWallet.IssueCreateAssetTx(
			"RnM",
			"RNM",
			9,
			map[uint32][]verify.State{
				0: {
					&secp256k1fx.TransferOutput{
						Amt:          100 * units.Schmeckle,
						OutputOwners: *owner,
					},
				},
			},
			e2e.WithDefaultContext(),
		)
		require.NoError(err)
		assetID = assetTx.ID()

		tests.Outf("{{green}}created new X-chain asset{{/}}: %s\n", assetID)
	})

	tc.By("export new X-chain asset to P-chain", func() {
		tx, err := xWallet.IssueExportTx(
			constants.PlatformChainID,
			[]*avax.TransferableOutput{
				{
					Asset: avax.Asset{
						ID: assetID,
					},
					Out: &secp256k1fx.TransferOutput{
						Amt:          100 * units.Schmeckle,
						OutputOwners: *owner,
					},
				},
			},
			e2e.WithDefaultContext(),
		)
		require.NoError(err)

		tests.Outf("{{green}}issued X-chain export{{/}}: %s\n", tx.ID())
	})

	tc.By("import new asset from X-chain on the P-chain", func() {
		tx, err := pWallet.IssueImportTx(
			xChainID,
			owner,
			e2e.WithDefaultContext(),
		)
		require.NoError(err)

		tests.Outf("{{green}}issued P-chain import{{/}}: %s\n", tx.ID())
	})

	tc.By("export asset from P-chain to the X-chain", func() {
		tx, err := pWallet.IssueExportTx(
			xChainID,
			[]*avax.TransferableOutput{
				{
					Asset: avax.Asset{
						ID: assetID,
					},
					Out: &secp256k1fx.TransferOutput{
						Amt:          100 * units.Schmeckle,
						OutputOwners: *owner,
					},
				},
			},
			e2e.WithDefaultContext(),
		)
		require.NoError(err)

		tests.Outf("{{green}}issued P-chain export{{/}}: %s\n", tx.ID())
	})

	tc.By("import asset from P-chain on the X-chain", func() {
		tx, err := xWallet.IssueImportTx(
			constants.PlatformChainID,
			owner,
			e2e.WithDefaultContext(),
		)
		require.NoError(err)

		tests.Outf("{{green}}issued X-chain import{{/}}: %s\n", tx.ID())
	})

}

var _ = ginkgo.Describe("[Banff]", func() {
	ginkgo.It("can send custom assets X->P and P->X", func() {
		keychain := e2e.Env.NewKeychain(1)
		wallet := e2e.NewWallet(keychain, e2e.Env.GetRandomNodeURI())
		SendCustomAssets(&ginkgoContext{}, wallet, keychain.Keys[0].Address())
	})
})
