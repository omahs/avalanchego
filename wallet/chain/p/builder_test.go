// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package p

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ava-labs/avalanchego/database"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/crypto/bls"
	"github.com/ava-labs/avalanchego/utils/crypto/secp256k1"
	"github.com/ava-labs/avalanchego/utils/set"
	"github.com/ava-labs/avalanchego/utils/units"
	"github.com/ava-labs/avalanchego/vms/components/avax"
	"github.com/ava-labs/avalanchego/vms/platformvm/fx"
	"github.com/ava-labs/avalanchego/vms/platformvm/reward"
	"github.com/ava-labs/avalanchego/vms/platformvm/stakeable"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs/fee"
	"github.com/ava-labs/avalanchego/vms/platformvm/upgrade"
	"github.com/ava-labs/avalanchego/vms/secp256k1fx"
	"github.com/ava-labs/avalanchego/wallet/chain/p/builder"
	"github.com/ava-labs/avalanchego/wallet/chain/p/signer"
	"github.com/ava-labs/avalanchego/wallet/subnet/primary/common"

	stdcontext "context"
	commonfee "github.com/ava-labs/avalanchego/vms/components/fee"
	blssigner "github.com/ava-labs/avalanchego/vms/platformvm/signer"
)

var (
	testKeys = secp256k1.TestKeys()

	// We hard-code [avaxAssetID] and [subnetAssetID] to make
	// ordering of UTXOs generated by [testUTXOsList] is reproducible
	avaxAssetID   = ids.Empty.Prefix(1789)
	subnetAssetID = ids.Empty.Prefix(2024)

	testContext = &builder.Context{
		NetworkID:                     constants.UnitTestID,
		AVAXAssetID:                   avaxAssetID,
		BaseTxFee:                     units.MicroAvax,
		CreateSubnetTxFee:             19 * units.MicroAvax,
		TransformSubnetTxFee:          789 * units.MicroAvax,
		CreateBlockchainTxFee:         1234 * units.MicroAvax,
		AddPrimaryNetworkValidatorFee: 19 * units.MilliAvax,
		AddPrimaryNetworkDelegatorFee: 765 * units.MilliAvax,
		AddSubnetValidatorFee:         1010 * units.MilliAvax,
		AddSubnetDelegatorFee:         9 * units.Avax,
	}
	testStaticConfig = staticFeesConfigFromContext(testContext)

	testGasPrice    = commonfee.GasPrice(10 * units.MicroAvax)
	testBlockMaxGas = commonfee.Gas(100_000)
)

// These tests create a tx, then verify that utxos included in the tx are
// exactly necessary to pay fees for it.

func TestBaseTx(t *testing.T) {
	var (
		require = require.New(t)

		// backend
		utxosKey   = testKeys[1]
		utxos      = makeTestUTXOs(utxosKey)
		chainUTXOs = common.NewDeterministicChainUTXOs(require, map[ids.ID][]*avax.UTXO{
			constants.PlatformChainID: utxos,
		})
		backend = NewBackend(testContext, chainUTXOs, nil)

		// builder and signer
		utxoAddr = utxosKey.Address()
		builder  = builder.New(set.Of(utxoAddr), testContext, backend)
		kc       = secp256k1fx.NewKeychain(utxosKey)
		s        = signer.New(kc, backend)

		// data to build the transaction
		outputsToMove = []*avax.TransferableOutput{{
			Asset: avax.Asset{ID: avaxAssetID},
			Out: &secp256k1fx.TransferOutput{
				Amt: 7 * units.Avax,
				OutputOwners: secp256k1fx.OutputOwners{
					Threshold: 1,
					Addrs:     []ids.ShortID{utxoAddr},
				},
			},
		}}
	)

	{ // Post E-Upgrade
		feeCalc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		utx, err := builder.NewBaseTx(
			outputsToMove,
			feeCalc,
		)
		require.NoError(err)

		tx, err := signer.SignUnsigned(stdcontext.Background(), s, utx)
		require.NoError(err)

		fc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		fee, err := fc.ComputeFee(utx, tx.Creds)
		require.NoError(err)
		require.Equal(30_620*units.MicroAvax, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		require.Len(ins, 2)
		require.Len(outs, 2)

		expectedConsumed := fee + outputsToMove[0].Out.Amount()
		consumed := ins[0].In.Amount() + ins[1].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
		require.Equal(outputsToMove[0], outs[1])
	}

	{ // Pre E-Upgrade
		feeCalc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		utx, err := builder.NewBaseTx(
			outputsToMove,
			feeCalc,
		)
		require.NoError(err)

		fc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		fee, err := fc.ComputeFee(utx, nil)
		require.NoError(err)
		require.Equal(testContext.BaseTxFee, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		require.Len(ins, 2)
		require.Len(outs, 2)

		expectedConsumed := fee + outputsToMove[0].Out.Amount()
		consumed := ins[0].In.Amount() + ins[1].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
		require.Equal(outputsToMove[0], outs[1])
	}
}

func TestAddSubnetValidatorTx(t *testing.T) {
	var (
		require = require.New(t)

		// backend
		utxosKey       = testKeys[1]
		utxos          = makeTestUTXOs(utxosKey)
		subnetID       = ids.GenerateTestID()
		subnetAuthKey  = testKeys[0]
		subnetAuthAddr = subnetAuthKey.Address()
		subnetOwner    = &secp256k1fx.OutputOwners{
			Threshold: 1,
			Addrs:     []ids.ShortID{subnetAuthAddr},
		}

		chainUTXOs = newDeterministicGenericBackend(
			require,
			map[ids.ID][]*avax.UTXO{
				constants.PlatformChainID: utxos,
			},
			map[ids.ID]fx.Owner{
				subnetID: subnetOwner,
			},
		)

		subnets = map[ids.ID]*txs.Tx{
			subnetID: {
				Unsigned: &txs.CreateSubnetTx{
					Owner: subnetOwner,
				},
			},
		}
		backend = NewBackend(testContext, chainUTXOs, subnets)

		// builder and signer
		utxoAddr = utxosKey.Address()
		builder  = builder.New(set.Of(utxoAddr, subnetAuthAddr), testContext, backend)
		kc       = secp256k1fx.NewKeychain(utxosKey)
		s        = signer.New(kc, chainUTXOs)

		// data to build the transaction
		subnetValidator = &txs.SubnetValidator{
			Validator: txs.Validator{
				NodeID: ids.GenerateTestNodeID(),
				End:    uint64(time.Now().Add(time.Hour).Unix()),
			},
			Subnet: subnetID,
		}
	)

	{ // Post E-Upgrade
		feeCalc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		utx, err := builder.NewAddSubnetValidatorTx(subnetValidator, feeCalc)
		require.NoError(err)

		tx, err := signer.SignUnsigned(stdcontext.Background(), s, utx)
		require.NoError(err)

		fc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		fee, err := fc.ComputeFee(utx, tx.Creds)
		require.NoError(err)
		require.Equal(30_610*units.MicroAvax, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		require.Len(ins, 2)
		require.Len(outs, 1)

		expectedConsumed := fee
		consumed := ins[0].In.Amount() + ins[1].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}

	{ // Pre E-Upgrade
		feeCalc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		utx, err := builder.NewAddSubnetValidatorTx(subnetValidator, feeCalc)
		require.NoError(err)

		fc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		fee, err := fc.ComputeFee(utx, nil)
		require.NoError(err)
		require.Equal(testContext.AddSubnetValidatorFee, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		require.Len(ins, 2)
		require.Len(outs, 1)

		expectedConsumed := fee
		consumed := ins[0].In.Amount() + ins[1].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}
}

func TestRemoveSubnetValidatorTx(t *testing.T) {
	var (
		require = require.New(t)

		// backend
		utxosKey       = testKeys[1]
		utxos          = makeTestUTXOs(utxosKey)
		subnetID       = ids.GenerateTestID()
		subnetAuthKey  = testKeys[0]
		subnetAuthAddr = subnetAuthKey.Address()
		subnetOwner    = &secp256k1fx.OutputOwners{
			Threshold: 1,
			Addrs:     []ids.ShortID{subnetAuthAddr},
		}

		chainUTXOs = newDeterministicGenericBackend(
			require,
			map[ids.ID][]*avax.UTXO{
				constants.PlatformChainID: utxos,
			},
			map[ids.ID]fx.Owner{
				subnetID: subnetOwner,
			},
		)

		subnets = map[ids.ID]*txs.Tx{
			subnetID: {
				Unsigned: &txs.CreateSubnetTx{
					Owner: subnetOwner,
				},
			},
		}

		backend = NewBackend(testContext, chainUTXOs, subnets)

		// builder and signer
		utxoAddr = utxosKey.Address()
		builder  = builder.New(set.Of(utxoAddr, subnetAuthAddr), testContext, backend)
		kc       = secp256k1fx.NewKeychain(utxosKey)
		s        = signer.New(kc, chainUTXOs)
	)

	{ // Post E-Upgrade
		feeCalc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		utx, err := builder.NewRemoveSubnetValidatorTx(
			ids.GenerateTestNodeID(),
			subnetID,
			feeCalc,
		)
		require.NoError(err)

		tx, err := signer.SignUnsigned(stdcontext.Background(), s, utx)
		require.NoError(err)

		fc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		fee, err := fc.ComputeFee(utx, tx.Creds)
		require.NoError(err)
		require.Equal(30_370*units.MicroAvax, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		require.Len(ins, 2)
		require.Len(outs, 1)

		expectedConsumed := fee
		consumed := ins[0].In.Amount() + ins[1].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}

	{ // Pre E-Upgrade
		feeCalc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		utx, err := builder.NewRemoveSubnetValidatorTx(
			ids.GenerateTestNodeID(),
			subnetID,
			feeCalc,
		)
		require.NoError(err)

		fc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		fee, err := fc.ComputeFee(utx, nil)
		require.NoError(err)
		require.Equal(testContext.BaseTxFee, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		require.Len(ins, 1)
		require.Len(outs, 1)

		expectedConsumed := fee
		consumed := ins[0].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}
}

func TestCreateChainTx(t *testing.T) {
	var (
		require = require.New(t)

		// backend
		utxosKey       = testKeys[1]
		utxos          = makeTestUTXOs(utxosKey)
		subnetID       = ids.GenerateTestID()
		subnetAuthKey  = testKeys[0]
		subnetAuthAddr = subnetAuthKey.Address()
		subnetOwner    = &secp256k1fx.OutputOwners{
			Threshold: 1,
			Addrs:     []ids.ShortID{subnetAuthAddr},
		}

		chainUTXOs = newDeterministicGenericBackend(
			require,
			map[ids.ID][]*avax.UTXO{
				constants.PlatformChainID: utxos,
			},
			map[ids.ID]fx.Owner{
				subnetID: subnetOwner,
			},
		)

		subnets = map[ids.ID]*txs.Tx{
			subnetID: {
				Unsigned: &txs.CreateSubnetTx{
					Owner: subnetOwner,
				},
			},
		}

		backend = NewBackend(testContext, chainUTXOs, subnets)

		// builder and signer
		utxoAddr = utxosKey.Address()
		builder  = builder.New(set.Of(utxoAddr, subnetAuthAddr), testContext, backend)
		kc       = secp256k1fx.NewKeychain(utxosKey)
		s        = signer.New(kc, chainUTXOs)

		// data to build the transaction
		genesisBytes = []byte{'a', 'b', 'c'}
		vmID         = ids.GenerateTestID()
		fxIDs        = []ids.ID{ids.GenerateTestID()}
		chainName    = "dummyChain"
	)

	{ // Post E-Upgrade
		feeCalc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		utx, err := builder.NewCreateChainTx(
			subnetID,
			genesisBytes,
			vmID,
			fxIDs,
			chainName,
			feeCalc,
		)
		require.NoError(err)

		tx, err := signer.SignUnsigned(stdcontext.Background(), s, utx)
		require.NoError(err)

		fc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		fee, err := fc.ComputeFee(utx, tx.Creds)
		require.NoError(err)
		require.Equal(31_040*units.MicroAvax, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		require.Len(ins, 2)
		require.Len(outs, 1)

		expectedConsumed := fee
		consumed := ins[0].In.Amount() + ins[1].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}

	{ // Pre E-Upgrade
		feeCalc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		utx, err := builder.NewCreateChainTx(
			subnetID,
			genesisBytes,
			vmID,
			fxIDs,
			chainName,
			feeCalc,
		)
		require.NoError(err)

		fc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		fee, err := fc.ComputeFee(utx, nil)
		require.NoError(err)
		require.Equal(testContext.CreateBlockchainTxFee, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		require.Len(ins, 1)
		require.Len(outs, 1)

		expectedConsumed := fee
		consumed := ins[0].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}
}

func TestCreateSubnetTx(t *testing.T) {
	var (
		require = require.New(t)

		// backend
		utxosKey       = testKeys[1]
		utxos          = makeTestUTXOs(utxosKey)
		subnetID       = ids.GenerateTestID()
		subnetAuthKey  = testKeys[0]
		subnetAuthAddr = subnetAuthKey.Address()
		subnetOwner    = &secp256k1fx.OutputOwners{
			Threshold: 1,
			Addrs:     []ids.ShortID{subnetAuthAddr},
		}

		chainUTXOs = newDeterministicGenericBackend(
			require,
			map[ids.ID][]*avax.UTXO{
				constants.PlatformChainID: utxos,
			},
			map[ids.ID]fx.Owner{
				subnetID: subnetOwner,
			},
		)

		subnets = map[ids.ID]*txs.Tx{
			subnetID: {
				Unsigned: &txs.CreateSubnetTx{
					Owner: subnetOwner,
				},
			},
		}

		backend = NewBackend(testContext, chainUTXOs, subnets)

		// builder and signer
		utxoAddr = utxosKey.Address()
		builder  = builder.New(set.Of(utxoAddr, subnetAuthAddr), testContext, backend)
		kc       = secp256k1fx.NewKeychain(utxosKey)
		s        = signer.New(kc, chainUTXOs)
	)

	{ // Post E-Upgrade
		feeCalc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		utx, err := builder.NewCreateSubnetTx(
			subnetOwner,
			feeCalc,
		)
		require.NoError(err)

		tx, err := signer.SignUnsigned(stdcontext.Background(), s, utx)
		require.NoError(err)

		fc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		fee, err := fc.ComputeFee(utx, tx.Creds)
		require.NoError(err)
		require.Equal(29_400*units.MicroAvax, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		require.Len(ins, 2)
		require.Len(outs, 1)

		expectedConsumed := fee
		consumed := ins[0].In.Amount() + ins[1].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}

	{ // Pre E-Upgrade
		feeCalc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		utx, err := builder.NewCreateSubnetTx(
			subnetOwner,
			feeCalc,
		)
		require.NoError(err)

		fc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		fee, err := fc.ComputeFee(utx, nil)
		require.NoError(err)
		require.Equal(testContext.CreateSubnetTxFee, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		require.Len(ins, 1)
		require.Len(outs, 1)

		expectedConsumed := fee
		consumed := ins[0].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}
}

func TestTransferSubnetOwnershipTx(t *testing.T) {
	var (
		require = require.New(t)

		// backend
		utxosKey       = testKeys[1]
		utxos          = makeTestUTXOs(utxosKey)
		subnetID       = ids.GenerateTestID()
		subnetAuthKey  = testKeys[0]
		subnetAuthAddr = subnetAuthKey.Address()
		subnetOwner    = &secp256k1fx.OutputOwners{
			Threshold: 1,
			Addrs:     []ids.ShortID{subnetAuthAddr},
		}

		chainUTXOs = newDeterministicGenericBackend(
			require,
			map[ids.ID][]*avax.UTXO{
				constants.PlatformChainID: utxos,
			},
			map[ids.ID]fx.Owner{
				subnetID: subnetOwner,
			},
		)

		subnets = map[ids.ID]*txs.Tx{
			subnetID: {
				Unsigned: &txs.CreateSubnetTx{
					Owner: subnetOwner,
				},
			},
		}

		backend = NewBackend(testContext, chainUTXOs, subnets)

		// builder
		utxoAddr = utxosKey.Address()
		builder  = builder.New(set.Of(utxoAddr, subnetAuthAddr), testContext, backend)
		kc       = secp256k1fx.NewKeychain(utxosKey)
		s        = signer.New(kc, chainUTXOs)
	)

	{ // Post E-Upgrade
		feeCalc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		utx, err := builder.NewTransferSubnetOwnershipTx(
			subnetID,
			subnetOwner,
			feeCalc,
		)
		require.NoError(err)

		tx, err := signer.SignUnsigned(stdcontext.Background(), s, utx)
		require.NoError(err)

		fc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		fee, err := fc.ComputeFee(utx, tx.Creds)
		require.NoError(err)
		require.Equal(30_570*units.MicroAvax, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		require.Len(ins, 2)
		require.Len(outs, 1)

		expectedConsumed := fee
		consumed := ins[0].In.Amount() + ins[1].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}

	{ // Pre E-Upgrade
		feeCalc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		utx, err := builder.NewTransferSubnetOwnershipTx(
			subnetID,
			subnetOwner,
			feeCalc,
		)
		require.NoError(err)

		fc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		fee, err := fc.ComputeFee(utx, nil)
		require.NoError(err)
		require.Equal(testContext.BaseTxFee, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		require.Len(ins, 1)
		require.Len(outs, 1)

		expectedConsumed := fee
		consumed := ins[0].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}
}

func TestImportTx(t *testing.T) {
	var (
		require = require.New(t)

		// backend
		utxosKey      = testKeys[1]
		utxos         = makeTestUTXOs(utxosKey)
		sourceChainID = ids.GenerateTestID()
		importedUTXOs = utxos[:1]
		chainUTXOs    = newDeterministicGenericBackend(
			require,
			map[ids.ID][]*avax.UTXO{
				constants.PlatformChainID: utxos,
				sourceChainID:             importedUTXOs,
			},
			nil,
		)

		backend = NewBackend(testContext, chainUTXOs, nil)

		// builder and signer
		utxoAddr = utxosKey.Address()
		builder  = builder.New(set.Of(utxoAddr), testContext, backend)
		kc       = secp256k1fx.NewKeychain(utxosKey)
		s        = signer.New(kc, chainUTXOs)

		// data to build the transaction
		importKey = testKeys[0]
		importTo  = &secp256k1fx.OutputOwners{
			Threshold: 1,
			Addrs: []ids.ShortID{
				importKey.Address(),
			},
		}
	)

	{ // Post E-Upgrade
		feeCalc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		utx, err := builder.NewImportTx(
			sourceChainID,
			importTo,
			feeCalc,
		)
		require.NoError(err)

		tx, err := signer.SignUnsigned(stdcontext.Background(), s, utx)
		require.NoError(err)

		fc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		fee, err := fc.ComputeFee(utx, tx.Creds)
		require.NoError(err)
		require.Equal(42_770*units.MicroAvax, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		importedIns := utx.ImportedInputs
		require.Len(ins, 2)
		require.Len(importedIns, 1)
		require.Len(outs, 1)

		expectedConsumed := fee
		consumed := importedIns[0].In.Amount() + ins[0].In.Amount() + ins[1].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}

	{ // Pre E-Upgrade
		feeCalc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		utx, err := builder.NewImportTx(
			sourceChainID,
			importTo,
			feeCalc,
		)
		require.NoError(err)

		fc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		fee, err := fc.ComputeFee(utx, nil)
		require.NoError(err)
		require.Equal(testContext.BaseTxFee, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		importedIns := utx.ImportedInputs
		require.Empty(ins)
		require.Len(importedIns, 1)
		require.Len(outs, 1)

		expectedConsumed := fee
		consumed := importedIns[0].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}
}

func TestExportTx(t *testing.T) {
	var (
		require = require.New(t)

		// backend
		utxosKey   = testKeys[1]
		utxos      = makeTestUTXOs(utxosKey)
		chainUTXOs = common.NewDeterministicChainUTXOs(require, map[ids.ID][]*avax.UTXO{
			constants.PlatformChainID: utxos,
		})
		backend = NewBackend(testContext, chainUTXOs, nil)

		// builder and signer
		utxoAddr = utxosKey.Address()
		builder  = builder.New(set.Of(utxoAddr), testContext, backend)
		kc       = secp256k1fx.NewKeychain(utxosKey)
		s        = signer.New(kc, backend)

		// data to build the transaction
		subnetID        = ids.GenerateTestID()
		exportedOutputs = []*avax.TransferableOutput{{
			Asset: avax.Asset{ID: avaxAssetID},
			Out: &secp256k1fx.TransferOutput{
				Amt: 7 * units.Avax,
				OutputOwners: secp256k1fx.OutputOwners{
					Threshold: 1,
					Addrs:     []ids.ShortID{utxoAddr},
				},
			},
		}}
	)

	{ // Post E-Upgrade
		feeCalc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		utx, err := builder.NewExportTx(
			subnetID,
			exportedOutputs,
			feeCalc,
		)
		require.NoError(err)

		tx, err := signer.SignUnsigned(stdcontext.Background(), s, utx)
		require.NoError(err)

		fc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		fee, err := fc.ComputeFee(utx, tx.Creds)
		require.NoError(err)
		require.Equal(30_980*units.MicroAvax, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		require.Len(ins, 2)
		require.Len(outs, 1)
		require.Equal(utx.ExportedOutputs, exportedOutputs)

		expectedConsumed := fee + exportedOutputs[0].Out.Amount()
		consumed := ins[0].In.Amount() + ins[1].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}

	{ // Pre E-Upgrade
		feeCalc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		utx, err := builder.NewExportTx(
			subnetID,
			exportedOutputs,
			feeCalc,
		)
		require.NoError(err)

		fc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		fee, err := fc.ComputeFee(utx, nil)
		require.NoError(err)
		require.Equal(testContext.BaseTxFee, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		require.Len(ins, 2)
		require.Len(outs, 1)

		expectedConsumed := fee + exportedOutputs[0].Out.Amount()
		consumed := ins[0].In.Amount() + ins[1].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
		require.Equal(utx.ExportedOutputs, exportedOutputs)
	}
}

func TestTransformSubnetTx(t *testing.T) {
	var (
		require = require.New(t)

		// backend
		utxosKey       = testKeys[1]
		utxos          = makeTestUTXOs(utxosKey)
		subnetID       = ids.GenerateTestID()
		subnetAuthKey  = testKeys[0]
		subnetAuthAddr = subnetAuthKey.Address()
		subnetOwner    = &secp256k1fx.OutputOwners{
			Threshold: 1,
			Addrs:     []ids.ShortID{subnetAuthAddr},
		}

		chainUTXOs = newDeterministicGenericBackend(
			require,
			map[ids.ID][]*avax.UTXO{
				constants.PlatformChainID: utxos,
			},
			map[ids.ID]fx.Owner{
				subnetID: subnetOwner,
			},
		)

		subnets = map[ids.ID]*txs.Tx{
			subnetID: {
				Unsigned: &txs.CreateSubnetTx{
					Owner: subnetOwner,
				},
			},
		}

		backend = NewBackend(testContext, chainUTXOs, subnets)

		// builder and signer
		utxoAddr = utxosKey.Address()
		builder  = builder.New(set.Of(utxoAddr, subnetAuthAddr), testContext, backend)
		kc       = secp256k1fx.NewKeychain(utxosKey)
		s        = signer.New(kc, chainUTXOs)

		// data to build the transaction
		initialSupply = 40 * units.MegaAvax
		maxSupply     = 100 * units.MegaAvax
	)

	{ // Post E-Upgrade
		feeCalc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		utx, err := builder.NewTransformSubnetTx(
			subnetID,
			subnetAssetID,
			initialSupply,                 // initial supply
			maxSupply,                     // max supply
			reward.PercentDenominator,     // min consumption rate
			reward.PercentDenominator,     // max consumption rate
			1,                             // min validator stake
			100*units.MegaAvax,            // max validator stake
			time.Second,                   // min stake duration
			365*24*time.Hour,              // max stake duration
			0,                             // min delegation fee
			1,                             // min delegator stake
			5,                             // max validator weight factor
			.80*reward.PercentDenominator, // uptime requirement
			feeCalc,
		)
		require.NoError(err)

		tx, err := signer.SignUnsigned(stdcontext.Background(), s, utx)
		require.NoError(err)

		fc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		fee, err := fc.ComputeFee(utx, tx.Creds)
		require.NoError(err)
		require.Equal(46_250*units.MicroAvax, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		require.Len(ins, 3)
		require.Len(outs, 2)

		expectedConsumedSubnetAsset := maxSupply - initialSupply
		consumedSubnetAsset := ins[0].In.Amount() - outs[1].Out.Amount()
		require.Equal(expectedConsumedSubnetAsset, consumedSubnetAsset)
		expectedConsumed := fee
		consumed := ins[1].In.Amount() + ins[2].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}

	{ // Pre E-Upgrade
		feeCalc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		utx, err := builder.NewTransformSubnetTx(
			subnetID,
			subnetAssetID,
			initialSupply,                 // initial supply
			maxSupply,                     // max supply
			reward.PercentDenominator,     // min consumption rate
			reward.PercentDenominator,     // max consumption rate
			1,                             // min validator stake
			100*units.MegaAvax,            // max validator stake
			time.Second,                   // min stake duration
			365*24*time.Hour,              // max stake duration
			0,                             // min delegation fee
			1,                             // min delegator stake
			5,                             // max validator weight factor
			.80*reward.PercentDenominator, // uptime requirement
			feeCalc,
		)
		require.NoError(err)

		fc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		fee, err := fc.ComputeFee(utx, nil)
		require.NoError(err)
		require.Equal(testContext.TransformSubnetTxFee, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		outs := utx.Outs
		require.Len(ins, 2)
		require.Len(outs, 2)

		expectedConsumedSubnetAsset := maxSupply - initialSupply
		consumedSubnetAsset := ins[0].In.Amount() - outs[1].Out.Amount()
		require.Equal(expectedConsumedSubnetAsset, consumedSubnetAsset)
		expectedConsumed := fee
		consumed := ins[1].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}
}

func TestAddPermissionlessValidatorTx(t *testing.T) {
	var (
		require = require.New(t)

		// backend
		utxosKey   = testKeys[1]
		utxos      = makeTestUTXOs(utxosKey)
		chainUTXOs = common.NewDeterministicChainUTXOs(require, map[ids.ID][]*avax.UTXO{
			constants.PlatformChainID: utxos,
		})
		backend = NewBackend(testContext, chainUTXOs, nil)

		// builder
		utxoAddr   = utxosKey.Address()
		rewardKey  = testKeys[0]
		rewardAddr = rewardKey.Address()
		builder    = builder.New(set.Of(utxoAddr, rewardAddr), testContext, backend)
		kc         = secp256k1fx.NewKeychain(utxosKey)
		s          = signer.New(kc, backend)

		// data to build the transaction
		validationRewardsOwner = &secp256k1fx.OutputOwners{
			Threshold: 1,
			Addrs: []ids.ShortID{
				rewardAddr,
			},
		}
		delegationRewardsOwner = &secp256k1fx.OutputOwners{
			Threshold: 1,
			Addrs: []ids.ShortID{
				rewardAddr,
			},
		}
	)

	sk, err := bls.NewSecretKey()
	require.NoError(err)

	{ // Post E-Upgrade
		feeCalc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		utx, err := builder.NewAddPermissionlessValidatorTx(
			&txs.SubnetValidator{
				Validator: txs.Validator{
					NodeID: ids.GenerateTestNodeID(),
					End:    uint64(time.Now().Add(time.Hour).Unix()),
					Wght:   2 * units.Avax,
				},
				Subnet: constants.PrimaryNetworkID,
			},
			blssigner.NewProofOfPossession(sk),
			avaxAssetID,
			validationRewardsOwner,
			delegationRewardsOwner,
			reward.PercentDenominator,
			feeCalc,
		)
		require.NoError(err)

		tx, err := signer.SignUnsigned(stdcontext.Background(), s, utx)
		require.NoError(err)

		fc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		fee, err := fc.ComputeFee(utx, tx.Creds)
		require.NoError(err)
		require.Equal(65_240*units.MicroAvax, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		staked := utx.StakeOuts
		outs := utx.Outs
		require.Len(ins, 4)
		require.Len(staked, 2)
		require.Len(outs, 2)

		expectedConsumedSubnetAsset := utx.Validator.Weight()
		consumedSubnetAsset := staked[0].Out.Amount() + staked[1].Out.Amount()
		require.Equal(ins[0].In.Amount()+ins[2].In.Amount()-outs[1].Out.Amount(), consumedSubnetAsset)
		require.Equal(expectedConsumedSubnetAsset, consumedSubnetAsset)

		expectedConsumed := fee
		consumed := ins[1].In.Amount() + ins[3].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}

	{ // Pre E-Upgrade
		feeCalc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		utx, err := builder.NewAddPermissionlessValidatorTx(
			&txs.SubnetValidator{
				Validator: txs.Validator{
					NodeID: ids.GenerateTestNodeID(),
					End:    uint64(time.Now().Add(time.Hour).Unix()),
					Wght:   2 * units.Avax,
				},
				Subnet: constants.PrimaryNetworkID,
			},
			blssigner.NewProofOfPossession(sk),
			avaxAssetID,
			validationRewardsOwner,
			delegationRewardsOwner,
			reward.PercentDenominator,
			feeCalc,
		)
		require.NoError(err)

		fc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		fee, err := fc.ComputeFee(utx, nil)
		require.NoError(err)
		require.Equal(testContext.AddPrimaryNetworkValidatorFee, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		staked := utx.StakeOuts
		outs := utx.Outs
		require.Len(ins, 4)
		require.Len(staked, 2)
		require.Len(outs, 2)

		expectedConsumedSubnetAsset := utx.Validator.Weight()
		consumedSubnetAsset := staked[0].Out.Amount() + staked[1].Out.Amount()
		require.Equal(ins[0].In.Amount()+ins[2].In.Amount()-outs[1].Out.Amount(), consumedSubnetAsset)
		require.Equal(expectedConsumedSubnetAsset, consumedSubnetAsset)

		expectedConsumed := fee
		consumed := ins[1].In.Amount() + ins[3].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}
}

func TestAddPermissionlessDelegatorTx(t *testing.T) {
	var (
		require = require.New(t)

		// backend
		utxosKey   = testKeys[1]
		utxos      = makeTestUTXOs(utxosKey)
		chainUTXOs = common.NewDeterministicChainUTXOs(require, map[ids.ID][]*avax.UTXO{
			constants.PlatformChainID: utxos,
		})
		backend = NewBackend(testContext, chainUTXOs, nil)

		// builder and signer
		utxoAddr   = utxosKey.Address()
		rewardKey  = testKeys[0]
		rewardAddr = rewardKey.Address()
		builder    = builder.New(set.Of(utxoAddr, rewardAddr), testContext, backend)
		kc         = secp256k1fx.NewKeychain(utxosKey)
		s          = signer.New(kc, backend)

		// data to build the transaction
		rewardsOwner = &secp256k1fx.OutputOwners{
			Threshold: 1,
			Addrs: []ids.ShortID{
				rewardAddr,
			},
		}
	)

	{ // Post E-Upgrade
		feeCalc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		utx, err := builder.NewAddPermissionlessDelegatorTx(
			&txs.SubnetValidator{
				Validator: txs.Validator{
					NodeID: ids.GenerateTestNodeID(),
					End:    uint64(time.Now().Add(time.Hour).Unix()),
					Wght:   2 * units.Avax,
				},
				Subnet: constants.PrimaryNetworkID,
			},
			avaxAssetID,
			rewardsOwner,
			feeCalc,
		)
		require.NoError(err)

		tx, err := signer.SignUnsigned(stdcontext.Background(), s, utx)
		require.NoError(err)

		fc := fee.NewDynamicCalculator(commonfee.NewCalculator(testGasPrice, testBlockMaxGas))
		fee, err := fc.ComputeFee(utx, tx.Creds)
		require.NoError(err)
		require.Equal(63_320*units.MicroAvax, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		staked := utx.StakeOuts
		outs := utx.Outs
		require.Len(ins, 4)
		require.Len(staked, 2)
		require.Len(outs, 2)

		expectedConsumedSubnetAsset := utx.Validator.Weight()
		consumedSubnetAsset := staked[0].Out.Amount() + staked[1].Out.Amount()
		require.Equal(ins[0].In.Amount()+ins[2].In.Amount()-outs[1].Out.Amount(), consumedSubnetAsset)
		require.Equal(expectedConsumedSubnetAsset, consumedSubnetAsset)

		expectedConsumed := fee
		consumed := ins[1].In.Amount() + ins[3].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}

	{ // Pre E-Upgrade
		feeCalc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		utx, err := builder.NewAddPermissionlessDelegatorTx(
			&txs.SubnetValidator{
				Validator: txs.Validator{
					NodeID: ids.GenerateTestNodeID(),
					End:    uint64(time.Now().Add(time.Hour).Unix()),
					Wght:   2 * units.Avax,
				},
				Subnet: constants.PrimaryNetworkID,
			},
			avaxAssetID,
			rewardsOwner,
			feeCalc,
		)
		require.NoError(err)

		fc := fee.NewStaticCalculator(testStaticConfig, upgrade.Config{}, time.Time{})
		fee, err := fc.ComputeFee(utx, nil)
		require.NoError(err)
		require.Equal(testContext.AddPrimaryNetworkDelegatorFee, fee)

		// check UTXOs selection and fee financing
		ins := utx.Ins
		staked := utx.StakeOuts
		outs := utx.Outs
		require.Len(ins, 4)
		require.Len(staked, 2)
		require.Len(outs, 2)

		expectedConsumedSubnetAsset := utx.Validator.Weight()
		consumedSubnetAsset := staked[0].Out.Amount() + staked[1].Out.Amount()
		require.Equal(ins[0].In.Amount()+ins[2].In.Amount()-outs[1].Out.Amount(), consumedSubnetAsset)
		require.Equal(expectedConsumedSubnetAsset, consumedSubnetAsset)

		expectedConsumed := fee
		consumed := ins[1].In.Amount() + ins[3].In.Amount() - outs[0].Out.Amount()
		require.Equal(expectedConsumed, consumed)
	}
}

func makeTestUTXOs(utxosKey *secp256k1.PrivateKey) []*avax.UTXO {
	// Note: we avoid ids.GenerateTestNodeID here to make sure that UTXO IDs won't change
	// run by run. This simplifies checking what utxos are included in the built txs.
	const utxosOffset uint64 = 2024

	utxosAddr := utxosKey.Address()
	return []*avax.UTXO{
		{ // a small UTXO first, which should not be enough to pay fees
			UTXOID: avax.UTXOID{
				TxID:        ids.Empty.Prefix(utxosOffset),
				OutputIndex: uint32(utxosOffset),
			},
			Asset: avax.Asset{ID: avaxAssetID},
			Out: &secp256k1fx.TransferOutput{
				Amt: 2 * units.MilliAvax,
				OutputOwners: secp256k1fx.OutputOwners{
					Locktime:  0,
					Addrs:     []ids.ShortID{utxosAddr},
					Threshold: 1,
				},
			},
		},
		{ // a locked, small UTXO
			UTXOID: avax.UTXOID{
				TxID:        ids.Empty.Prefix(utxosOffset + 1),
				OutputIndex: uint32(utxosOffset + 1),
			},
			Asset: avax.Asset{ID: avaxAssetID},
			Out: &stakeable.LockOut{
				Locktime: uint64(time.Now().Add(time.Hour).Unix()),
				TransferableOut: &secp256k1fx.TransferOutput{
					Amt: 3 * units.MilliAvax,
					OutputOwners: secp256k1fx.OutputOwners{
						Threshold: 1,
						Addrs:     []ids.ShortID{utxosAddr},
					},
				},
			},
		},
		{ // a subnetAssetID denominated UTXO
			UTXOID: avax.UTXOID{
				TxID:        ids.Empty.Prefix(utxosOffset + 2),
				OutputIndex: uint32(utxosOffset + 2),
			},
			Asset: avax.Asset{ID: subnetAssetID},
			Out: &secp256k1fx.TransferOutput{
				Amt: 99 * units.MegaAvax,
				OutputOwners: secp256k1fx.OutputOwners{
					Locktime:  0,
					Addrs:     []ids.ShortID{utxosAddr},
					Threshold: 1,
				},
			},
		},
		{ // a locked, large UTXO
			UTXOID: avax.UTXOID{
				TxID:        ids.Empty.Prefix(utxosOffset + 3),
				OutputIndex: uint32(utxosOffset + 3),
			},
			Asset: avax.Asset{ID: avaxAssetID},
			Out: &stakeable.LockOut{
				Locktime: uint64(time.Now().Add(time.Hour).Unix()),
				TransferableOut: &secp256k1fx.TransferOutput{
					Amt: 88 * units.Avax,
					OutputOwners: secp256k1fx.OutputOwners{
						Threshold: 1,
						Addrs:     []ids.ShortID{utxosAddr},
					},
				},
			},
		},
		{ // a large UTXO last, which should be enough to pay any fee by itself
			UTXOID: avax.UTXOID{
				TxID:        ids.Empty.Prefix(utxosOffset + 4),
				OutputIndex: uint32(utxosOffset + 4),
			},
			Asset: avax.Asset{ID: avaxAssetID},
			Out: &secp256k1fx.TransferOutput{
				Amt: 9 * units.Avax,
				OutputOwners: secp256k1fx.OutputOwners{
					Locktime:  0,
					Addrs:     []ids.ShortID{utxosAddr},
					Threshold: 1,
				},
			},
		},
	}
}

func newDeterministicGenericBackend(
	require *require.Assertions,
	utxoSets map[ids.ID][]*avax.UTXO,
	subnetOwners map[ids.ID]fx.Owner,
) *testBackend {
	globalUTXOs := common.NewUTXOs()
	for subnetID, utxos := range utxoSets {
		for _, utxo := range utxos {
			require.NoError(
				globalUTXOs.AddUTXO(stdcontext.Background(), subnetID, constants.PlatformChainID, utxo),
			)
		}
	}
	return &testBackend{
		DeterministicChainUTXOs: common.NewDeterministicChainUTXOs(require, utxoSets),
		subnetOwners:            subnetOwners,
	}
}

type testBackend struct {
	*common.DeterministicChainUTXOs
	subnetOwners map[ids.ID]fx.Owner // subnetID --> fx.Owner
}

func (c *testBackend) UTXOs(ctx stdcontext.Context, sourceChainID ids.ID) ([]*avax.UTXO, error) {
	utxos, err := c.ChainUTXOs.UTXOs(ctx, sourceChainID)
	if err != nil {
		return nil, err
	}

	slices.SortFunc(utxos, func(a, b *avax.UTXO) int {
		return a.Compare(&b.UTXOID)
	})
	return utxos, nil
}

func (c *testBackend) GetSubnetOwner(_ stdcontext.Context, subnetID ids.ID) (fx.Owner, error) {
	owner, found := c.subnetOwners[subnetID]
	if !found {
		return nil, database.ErrNotFound
	}

	return owner, nil
}
