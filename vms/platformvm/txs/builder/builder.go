// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package builder

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow"
	"github.com/ava-labs/avalanchego/utils"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/crypto/secp256k1"
	"github.com/ava-labs/avalanchego/utils/math"
	"github.com/ava-labs/avalanchego/utils/timer/mockable"
	"github.com/ava-labs/avalanchego/vms/components/avax"
	"github.com/ava-labs/avalanchego/vms/platformvm/config"
	"github.com/ava-labs/avalanchego/vms/platformvm/fx"
	"github.com/ava-labs/avalanchego/vms/platformvm/signer"
	"github.com/ava-labs/avalanchego/vms/platformvm/state"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs/backends"
	"github.com/ava-labs/avalanchego/vms/platformvm/utxo"
	"github.com/ava-labs/avalanchego/vms/secp256k1fx"
)

// Max number of items allowed in a page
const MaxPageSize = 1024

var (
	_ Builder = (*builder)(nil)

	ErrNoFunds = errors.New("no spendable funds were found")
)

type Builder interface {
	AtomicTxBuilder
	DecisionTxBuilder
	ProposalTxBuilder
}

type AtomicTxBuilder interface {
	// chainID: chain to import UTXOs from
	// to: address of recipient
	// keys: keys to import the funds
	// changeAddr: address to send change to, if there is any
	NewImportTx(
		chainID ids.ID,
		to ids.ShortID,
		keys []*secp256k1.PrivateKey,
		changeAddr ids.ShortID,
		memo []byte,
	) (*txs.Tx, error)

	// amount: amount of tokens to export
	// chainID: chain to send the UTXOs to
	// to: address of recipient
	// keys: keys to pay the fee and provide the tokens
	// changeAddr: address to send change to, if there is any
	NewExportTx(
		amount uint64,
		chainID ids.ID,
		to ids.ShortID,
		keys []*secp256k1.PrivateKey,
		changeAddr ids.ShortID,
		memo []byte,
	) (*txs.Tx, error)
}

type DecisionTxBuilder interface {
	// subnetID: ID of the subnet that validates the new chain
	// genesisData: byte repr. of genesis state of the new chain
	// vmID: ID of VM this chain runs
	// fxIDs: ids of features extensions this chain supports
	// chainName: name of the chain
	// keys: keys to sign the tx
	// changeAddr: address to send change to, if there is any
	NewCreateChainTx(
		subnetID ids.ID,
		genesisData []byte,
		vmID ids.ID,
		fxIDs []ids.ID,
		chainName string,
		keys []*secp256k1.PrivateKey,
		changeAddr ids.ShortID,
		memo []byte,
	) (*txs.Tx, error)

	// threshold: [threshold] of [ownerAddrs] needed to manage this subnet
	// ownerAddrs: control addresses for the new subnet
	// keys: keys to pay the fee
	// changeAddr: address to send change to, if there is any
	NewCreateSubnetTx(
		threshold uint32,
		ownerAddrs []ids.ShortID,
		keys []*secp256k1.PrivateKey,
		changeAddr ids.ShortID,
		memo []byte,
	) (*txs.Tx, error)

	NewTransformSubnetTx(
		subnetID ids.ID,
		assetID ids.ID,
		initialSupply uint64,
		maxSupply uint64,
		minConsumptionRate uint64,
		maxConsumptionRate uint64,
		minValidatorStake uint64,
		maxValidatorStake uint64,
		minStakeDuration time.Duration,
		maxStakeDuration time.Duration,
		minDelegationFee uint32,
		minDelegatorStake uint64,
		maxValidatorWeightFactor byte,
		uptimeRequirement uint32,
		keys []*secp256k1.PrivateKey,
		changeAddr ids.ShortID,
		memo []byte,
	) (*txs.Tx, error)

	// amount: amount the sender is sending
	// owner: recipient of the funds
	// keys: keys to sign the tx and pay the amount
	// changeAddr: address to send change to, if there is any
	NewBaseTx(
		amount uint64,
		owner secp256k1fx.OutputOwners,
		keys []*secp256k1.PrivateKey,
		changeAddr ids.ShortID,
		memo []byte,
	) (*txs.Tx, error)
}

type ProposalTxBuilder interface {
	// stakeAmount: amount the validator stakes
	// startTime: unix time they start validating
	// endTime: unix time they stop validating
	// nodeID: ID of the node we want to validate with
	// rewardAddress: address to send reward to, if applicable
	// shares: 10,000 times percentage of reward taken from delegators
	// keys: Keys providing the staked tokens
	// changeAddr: Address to send change to, if there is any
	NewAddValidatorTx(
		stakeAmount,
		startTime,
		endTime uint64,
		nodeID ids.NodeID,
		rewardAddress ids.ShortID,
		shares uint32,
		keys []*secp256k1.PrivateKey,
		changeAddr ids.ShortID,
		memo []byte,
	) (*txs.Tx, error)

	// stakeAmount: amount the validator stakes
	// startTime: unix time they start validating
	// endTime: unix time they stop validating
	// nodeID: ID of the node we want to validate with
	// pop: the node proof of possession
	// rewardAddress: address to send reward to, if applicable
	// shares: 10,000 times percentage of reward taken from delegators
	// keys: Keys providing the staked tokens
	// changeAddr: Address to send change to, if there is any
	NewAddPermissionlessValidatorTx(
		stakeAmount,
		startTime,
		endTime uint64,
		nodeID ids.NodeID,
		pop *signer.ProofOfPossession,
		rewardAddress ids.ShortID,
		shares uint32,
		keys []*secp256k1.PrivateKey,
		changeAddr ids.ShortID,
		memo []byte,
	) (*txs.Tx, error)

	// stakeAmount: amount the delegator stakes
	// startTime: unix time they start delegating
	// endTime: unix time they stop delegating
	// nodeID: ID of the node we are delegating to
	// rewardAddress: address to send reward to, if applicable
	// keys: keys providing the staked tokens
	// changeAddr: address to send change to, if there is any
	NewAddDelegatorTx(
		stakeAmount,
		startTime,
		endTime uint64,
		nodeID ids.NodeID,
		rewardAddress ids.ShortID,
		keys []*secp256k1.PrivateKey,
		changeAddr ids.ShortID,
		memo []byte,
	) (*txs.Tx, error)

	// stakeAmount: amount the delegator stakes
	// startTime: unix time they start delegating
	// endTime: unix time they stop delegating
	// nodeID: ID of the node we are delegating to
	// rewardAddress: address to send reward to, if applicable
	// keys: keys providing the staked tokens
	// changeAddr: address to send change to, if there is any
	NewAddPermissionlessDelegatorTx(
		stakeAmount,
		startTime,
		endTime uint64,
		nodeID ids.NodeID,
		rewardAddress ids.ShortID,
		keys []*secp256k1.PrivateKey,
		changeAddr ids.ShortID,
		memo []byte,
	) (*txs.Tx, error)

	// weight: sampling weight of the new validator
	// startTime: unix time they start delegating
	// endTime:  unix time they top delegating
	// nodeID: ID of the node validating
	// subnetID: ID of the subnet the validator will validate
	// keys: keys to use for adding the validator
	// changeAddr: address to send change to, if there is any
	NewAddSubnetValidatorTx(
		weight,
		startTime,
		endTime uint64,
		nodeID ids.NodeID,
		subnetID ids.ID,
		keys []*secp256k1.PrivateKey,
		changeAddr ids.ShortID,
		memo []byte,
	) (*txs.Tx, error)

	// Creates a transaction that removes [nodeID]
	// as a validator from [subnetID]
	// keys: keys to use for removing the validator
	// changeAddr: address to send change to, if there is any
	NewRemoveSubnetValidatorTx(
		nodeID ids.NodeID,
		subnetID ids.ID,
		keys []*secp256k1.PrivateKey,
		changeAddr ids.ShortID,
		memo []byte,
	) (*txs.Tx, error)

	// Creates a transaction that transfers ownership of [subnetID]
	// threshold: [threshold] of [ownerAddrs] needed to manage this subnet
	// ownerAddrs: control addresses for the new subnet
	// keys: keys to use for modifying the subnet
	// changeAddr: address to send change to, if there is any
	NewTransferSubnetOwnershipTx(
		subnetID ids.ID,
		threshold uint32,
		ownerAddrs []ids.ShortID,
		keys []*secp256k1.PrivateKey,
		changeAddr ids.ShortID,
		memo []byte,
	) (*txs.Tx, error)
}

func New(
	ctx *snow.Context,
	cfg *config.Config,
	clk *mockable.Clock,
	fx fx.Fx,
	state state.State,
	atomicUTXOManager avax.AtomicUTXOManager,
	utxoSpender utxo.Spender,
) Builder {
	return &builder{
		AtomicUTXOManager: atomicUTXOManager,
		Spender:           utxoSpender,
		state:             state,
		cfg:               cfg,
		ctx:               ctx,
		clk:               clk,
		fx:                fx,
	}
}

type builder struct {
	avax.AtomicUTXOManager
	utxo.Spender
	state state.State

	cfg *config.Config
	ctx *snow.Context
	clk *mockable.Clock
	fx  fx.Fx
}

func (b *builder) NewImportTx(
	from ids.ID,
	to ids.ShortID,
	keys []*secp256k1.PrivateKey,
	changeAddr ids.ShortID,
	memo []byte,
) (*txs.Tx, error) {
	kc := secp256k1fx.NewKeychain(keys...)

	atomicUTXOs, _, _, err := b.GetAtomicUTXOs(from, kc.Addresses(), ids.ShortEmpty, ids.Empty, MaxPageSize)
	if err != nil {
		return nil, fmt.Errorf("problem retrieving atomic UTXOs: %w", err)
	}

	importedInputs := []*avax.TransferableInput{}
	signers := [][]*secp256k1.PrivateKey{}

	importedAmounts := make(map[ids.ID]uint64)
	now := b.clk.Unix()
	for _, utxo := range atomicUTXOs {
		inputIntf, utxoSigners, err := kc.Spend(utxo.Out, now)
		if err != nil {
			continue
		}
		input, ok := inputIntf.(avax.TransferableIn)
		if !ok {
			continue
		}
		assetID := utxo.AssetID()
		importedAmounts[assetID], err = math.Add64(importedAmounts[assetID], input.Amount())
		if err != nil {
			return nil, err
		}
		importedInputs = append(importedInputs, &avax.TransferableInput{
			UTXOID: utxo.UTXOID,
			Asset:  utxo.Asset,
			In:     input,
		})
		signers = append(signers, utxoSigners)
	}
	avax.SortTransferableInputsWithSigners(importedInputs, signers)

	if len(importedAmounts) == 0 {
		return nil, ErrNoFunds // No imported UTXOs were spendable
	}

	importedAVAX := importedAmounts[b.ctx.AVAXAssetID]

	ins := []*avax.TransferableInput{}
	outs := []*avax.TransferableOutput{}
	switch {
	case importedAVAX < b.cfg.TxFee: // imported amount goes toward paying tx fee
		toBurn := map[ids.ID]uint64{
			b.ctx.AVAXAssetID: b.cfg.TxFee - importedAVAX,
		}
		toStake := make(map[ids.ID]uint64)
		ins, outs, _, err = b.Spend(b.state, keys, toBurn, toStake, changeAddr)
		if err != nil {
			return nil, fmt.Errorf("couldn't generate tx inputs/outputs: %w", err)
		}
		delete(importedAmounts, b.ctx.AVAXAssetID)
	case importedAVAX == b.cfg.TxFee:
		delete(importedAmounts, b.ctx.AVAXAssetID)
	default:
		importedAmounts[b.ctx.AVAXAssetID] -= b.cfg.TxFee
	}

	for assetID, amount := range importedAmounts {
		outs = append(outs, &avax.TransferableOutput{
			Asset: avax.Asset{ID: assetID},
			Out: &secp256k1fx.TransferOutput{
				Amt: amount,
				OutputOwners: secp256k1fx.OutputOwners{
					Locktime:  0,
					Threshold: 1,
					Addrs:     []ids.ShortID{to},
				},
			},
		})
	}

	avax.SortTransferableOutputs(outs, txs.Codec) // sort imported outputs

	// Create the transaction
	utx := &txs.ImportTx{
		BaseTx: txs.BaseTx{BaseTx: avax.BaseTx{
			NetworkID:    b.ctx.NetworkID,
			BlockchainID: b.ctx.ChainID,
			Outs:         outs,
			Ins:          ins,
			Memo:         memo,
		}},
		SourceChain:    from,
		ImportedInputs: importedInputs,
	}

	s := backends.New(
		secp256k1fx.NewKeychain(keys...),
		NewSignerBackend(b.state, from, atomicUTXOs),
	)
	tx, err := backends.SignUnsigned(context.Background(), s, utx)
	if err != nil {
		return nil, err
	}
	return tx, tx.SyntacticVerify(b.ctx)
}

// TODO: should support other assets than AVAX
func (b *builder) NewExportTx(
	amount uint64,
	chainID ids.ID,
	to ids.ShortID,
	keys []*secp256k1.PrivateKey,
	changeAddr ids.ShortID,
	memo []byte,
) (*txs.Tx, error) {
	amtToBurn, err := math.Add64(amount, b.cfg.TxFee)
	if err != nil {
		return nil, fmt.Errorf("amount (%d) + tx fee(%d) overflows", amount, b.cfg.TxFee)
	}
	toBurn := map[ids.ID]uint64{
		b.ctx.AVAXAssetID: amtToBurn,
	}
	toStake := make(map[ids.ID]uint64)
	ins, outs, _, err := b.Spend(b.state, keys, toBurn, toStake, changeAddr)
	if err != nil {
		return nil, fmt.Errorf("couldn't generate tx inputs/outputs: %w", err)
	}

	// Create the transaction
	utx := &txs.ExportTx{
		BaseTx: txs.BaseTx{BaseTx: avax.BaseTx{
			NetworkID:    b.ctx.NetworkID,
			BlockchainID: b.ctx.ChainID,
			Ins:          ins,
			Outs:         outs, // Non-exported outputs
			Memo:         memo,
		}},
		DestinationChain: chainID,
		ExportedOutputs: []*avax.TransferableOutput{{ // Exported to X-Chain
			Asset: avax.Asset{ID: b.ctx.AVAXAssetID},
			Out: &secp256k1fx.TransferOutput{
				Amt: amount,
				OutputOwners: secp256k1fx.OutputOwners{
					Locktime:  0,
					Threshold: 1,
					Addrs:     []ids.ShortID{to},
				},
			},
		}},
	}

	s := backends.New(
		secp256k1fx.NewKeychain(keys...),
		NewSignerBackend(b.state, ids.Empty, nil),
	)
	tx, err := backends.SignUnsigned(context.Background(), s, utx)
	if err != nil {
		return nil, err
	}
	return tx, tx.SyntacticVerify(b.ctx)
}

func (b *builder) NewCreateChainTx(
	subnetID ids.ID,
	genesisData []byte,
	vmID ids.ID,
	fxIDs []ids.ID,
	chainName string,
	keys []*secp256k1.PrivateKey,
	changeAddr ids.ShortID,
	memo []byte,
) (*txs.Tx, error) {
	timestamp := b.state.GetTimestamp()
	createBlockchainTxFee := b.cfg.GetCreateBlockchainTxFee(timestamp)
	toBurn := map[ids.ID]uint64{
		b.ctx.AVAXAssetID: createBlockchainTxFee,
	}
	toStake := make(map[ids.ID]uint64)
	ins, outs, _, err := b.Spend(b.state, keys, toBurn, toStake, changeAddr)
	if err != nil {
		return nil, fmt.Errorf("couldn't generate tx inputs/outputs: %w", err)
	}

	subnetAuth, _, err := b.Authorize(b.state, subnetID, keys)
	if err != nil {
		return nil, fmt.Errorf("couldn't authorize tx's subnet restrictions: %w", err)
	}

	// Sort the provided fxIDs
	utils.Sort(fxIDs)

	// Create the tx
	utx := &txs.CreateChainTx{
		BaseTx: txs.BaseTx{BaseTx: avax.BaseTx{
			NetworkID:    b.ctx.NetworkID,
			BlockchainID: b.ctx.ChainID,
			Ins:          ins,
			Outs:         outs,
			Memo:         memo,
		}},
		SubnetID:    subnetID,
		ChainName:   chainName,
		VMID:        vmID,
		FxIDs:       fxIDs,
		GenesisData: genesisData,
		SubnetAuth:  subnetAuth,
	}

	s := backends.New(
		secp256k1fx.NewKeychain(keys...),
		NewSignerBackend(b.state, ids.Empty, nil),
	)
	tx, err := backends.SignUnsigned(context.Background(), s, utx)
	if err != nil {
		return nil, err
	}
	return tx, tx.SyntacticVerify(b.ctx)
}

func (b *builder) NewCreateSubnetTx(
	threshold uint32,
	ownerAddrs []ids.ShortID,
	keys []*secp256k1.PrivateKey,
	changeAddr ids.ShortID,
	memo []byte,
) (*txs.Tx, error) {
	timestamp := b.state.GetTimestamp()
	createSubnetTxFee := b.cfg.GetCreateSubnetTxFee(timestamp)
	toBurn := map[ids.ID]uint64{
		b.ctx.AVAXAssetID: createSubnetTxFee,
	}
	toStake := make(map[ids.ID]uint64)
	ins, outs, _, err := b.Spend(b.state, keys, toBurn, toStake, changeAddr)
	if err != nil {
		return nil, fmt.Errorf("couldn't generate tx inputs/outputs: %w", err)
	}

	// Sort control addresses
	utils.Sort(ownerAddrs)

	// Create the tx
	utx := &txs.CreateSubnetTx{
		BaseTx: txs.BaseTx{BaseTx: avax.BaseTx{
			NetworkID:    b.ctx.NetworkID,
			BlockchainID: b.ctx.ChainID,
			Ins:          ins,
			Outs:         outs,
			Memo:         memo,
		}},
		Owner: &secp256k1fx.OutputOwners{
			Threshold: threshold,
			Addrs:     ownerAddrs,
		},
	}

	s := backends.New(
		secp256k1fx.NewKeychain(keys...),
		NewSignerBackend(b.state, ids.Empty, nil),
	)
	tx, err := backends.SignUnsigned(context.Background(), s, utx)
	if err != nil {
		return nil, err
	}
	return tx, tx.SyntacticVerify(b.ctx)
}

func (b *builder) NewTransformSubnetTx(
	subnetID ids.ID,
	assetID ids.ID,
	initialSupply uint64,
	maxSupply uint64,
	minConsumptionRate uint64,
	maxConsumptionRate uint64,
	minValidatorStake uint64,
	maxValidatorStake uint64,
	minStakeDuration time.Duration,
	maxStakeDuration time.Duration,
	minDelegationFee uint32,
	minDelegatorStake uint64,
	maxValidatorWeightFactor byte,
	uptimeRequirement uint32,
	keys []*secp256k1.PrivateKey,
	changeAddr ids.ShortID,
	memo []byte,
) (*txs.Tx, error) {
	toBurn := map[ids.ID]uint64{
		b.ctx.AVAXAssetID: b.cfg.TransformSubnetTxFee,
	}
	toStake := make(map[ids.ID]uint64)
	ins, outs, _, err := b.Spend(b.state, keys, toBurn, toStake, changeAddr)
	if err != nil {
		return nil, fmt.Errorf("couldn't generate tx inputs/outputs: %w", err)
	}

	subnetAuth, _, err := b.Authorize(b.state, subnetID, keys)
	if err != nil {
		return nil, fmt.Errorf("couldn't authorize tx's subnet restrictions: %w", err)
	}

	utx := &txs.TransformSubnetTx{
		BaseTx: txs.BaseTx{
			BaseTx: avax.BaseTx{
				NetworkID:    b.ctx.NetworkID,
				BlockchainID: b.ctx.ChainID,
				Ins:          ins,
				Outs:         outs,
				Memo:         memo,
			},
		},
		Subnet:                   subnetID,
		AssetID:                  assetID,
		InitialSupply:            initialSupply,
		MaximumSupply:            maxSupply,
		MinConsumptionRate:       minConsumptionRate,
		MaxConsumptionRate:       maxConsumptionRate,
		MinValidatorStake:        minValidatorStake,
		MaxValidatorStake:        maxValidatorStake,
		MinStakeDuration:         uint32(minStakeDuration / time.Second),
		MaxStakeDuration:         uint32(maxStakeDuration / time.Second),
		MinDelegationFee:         minDelegationFee,
		MinDelegatorStake:        minDelegatorStake,
		MaxValidatorWeightFactor: maxValidatorWeightFactor,
		UptimeRequirement:        uptimeRequirement,
		SubnetAuth:               subnetAuth,
	}

	s := backends.New(
		secp256k1fx.NewKeychain(keys...),
		NewSignerBackend(b.state, ids.Empty, nil),
	)
	tx, err := backends.SignUnsigned(context.Background(), s, utx)
	if err != nil {
		return nil, err
	}
	return tx, tx.SyntacticVerify(b.ctx)
}

func (b *builder) NewAddValidatorTx(
	stakeAmount,
	startTime,
	endTime uint64,
	nodeID ids.NodeID,
	rewardAddress ids.ShortID,
	shares uint32,
	keys []*secp256k1.PrivateKey,
	changeAddr ids.ShortID,
	memo []byte,
) (*txs.Tx, error) {
	toBurn := map[ids.ID]uint64{
		b.ctx.AVAXAssetID: b.cfg.AddPrimaryNetworkValidatorFee,
	}
	toStake := map[ids.ID]uint64{
		b.ctx.AVAXAssetID: stakeAmount,
	}
	ins, unstakedOuts, stakedOuts, err := b.Spend(b.state, keys, toBurn, toStake, changeAddr)
	if err != nil {
		return nil, fmt.Errorf("couldn't generate tx inputs/outputs: %w", err)
	}
	// Create the tx
	utx := &txs.AddValidatorTx{
		BaseTx: txs.BaseTx{BaseTx: avax.BaseTx{
			NetworkID:    b.ctx.NetworkID,
			BlockchainID: b.ctx.ChainID,
			Ins:          ins,
			Outs:         unstakedOuts,
			Memo:         memo,
		}},
		Validator: txs.Validator{
			NodeID: nodeID,
			Start:  startTime,
			End:    endTime,
			Wght:   stakeAmount,
		},
		StakeOuts: stakedOuts,
		RewardsOwner: &secp256k1fx.OutputOwners{
			Locktime:  0,
			Threshold: 1,
			Addrs:     []ids.ShortID{rewardAddress},
		},
		DelegationShares: shares,
	}

	s := backends.New(
		secp256k1fx.NewKeychain(keys...),
		NewSignerBackend(b.state, ids.Empty, nil),
	)
	tx, err := backends.SignUnsigned(context.Background(), s, utx)
	if err != nil {
		return nil, err
	}
	return tx, tx.SyntacticVerify(b.ctx)
}

func (b *builder) NewAddPermissionlessValidatorTx(
	stakeAmount,
	startTime,
	endTime uint64,
	nodeID ids.NodeID,
	pop *signer.ProofOfPossession,
	rewardAddress ids.ShortID,
	shares uint32,
	keys []*secp256k1.PrivateKey,
	changeAddr ids.ShortID,
	memo []byte,
) (*txs.Tx, error) {
	toBurn := map[ids.ID]uint64{
		b.ctx.AVAXAssetID: b.cfg.AddPrimaryNetworkValidatorFee,
	}
	toStake := map[ids.ID]uint64{
		b.ctx.AVAXAssetID: stakeAmount,
	}
	ins, unstakedOuts, stakedOuts, err := b.Spend(b.state, keys, toBurn, toStake, changeAddr)
	if err != nil {
		return nil, fmt.Errorf("couldn't generate tx inputs/outputs: %w", err)
	}
	// Create the tx
	utx := &txs.AddPermissionlessValidatorTx{
		BaseTx: txs.BaseTx{BaseTx: avax.BaseTx{
			NetworkID:    b.ctx.NetworkID,
			BlockchainID: b.ctx.ChainID,
			Ins:          ins,
			Outs:         unstakedOuts,
			Memo:         memo,
		}},
		Validator: txs.Validator{
			NodeID: nodeID,
			Start:  startTime,
			End:    endTime,
			Wght:   stakeAmount,
		},
		Subnet:    constants.PrimaryNetworkID,
		Signer:    pop,
		StakeOuts: stakedOuts,
		ValidatorRewardsOwner: &secp256k1fx.OutputOwners{
			Locktime:  0,
			Threshold: 1,
			Addrs:     []ids.ShortID{rewardAddress},
		},
		DelegatorRewardsOwner: &secp256k1fx.OutputOwners{
			Locktime:  0,
			Threshold: 1,
			Addrs:     []ids.ShortID{rewardAddress},
		},
		DelegationShares: shares,
	}

	s := backends.New(
		secp256k1fx.NewKeychain(keys...),
		NewSignerBackend(b.state, ids.Empty, nil),
	)
	tx, err := backends.SignUnsigned(context.Background(), s, utx)
	if err != nil {
		return nil, err
	}
	return tx, tx.SyntacticVerify(b.ctx)
}

func (b *builder) NewAddDelegatorTx(
	stakeAmount,
	startTime,
	endTime uint64,
	nodeID ids.NodeID,
	rewardAddress ids.ShortID,
	keys []*secp256k1.PrivateKey,
	changeAddr ids.ShortID,
	memo []byte,
) (*txs.Tx, error) {
	toBurn := map[ids.ID]uint64{
		b.ctx.AVAXAssetID: b.cfg.AddPrimaryNetworkDelegatorFee,
	}
	toStake := map[ids.ID]uint64{
		b.ctx.AVAXAssetID: stakeAmount,
	}
	ins, unlockedOuts, lockedOuts, err := b.Spend(b.state, keys, toBurn, toStake, changeAddr)
	if err != nil {
		return nil, fmt.Errorf("couldn't generate tx inputs/outputs: %w", err)
	}
	// Create the tx
	utx := &txs.AddDelegatorTx{
		BaseTx: txs.BaseTx{BaseTx: avax.BaseTx{
			NetworkID:    b.ctx.NetworkID,
			BlockchainID: b.ctx.ChainID,
			Ins:          ins,
			Outs:         unlockedOuts,
			Memo:         memo,
		}},
		Validator: txs.Validator{
			NodeID: nodeID,
			Start:  startTime,
			End:    endTime,
			Wght:   stakeAmount,
		},
		StakeOuts: lockedOuts,
		DelegationRewardsOwner: &secp256k1fx.OutputOwners{
			Locktime:  0,
			Threshold: 1,
			Addrs:     []ids.ShortID{rewardAddress},
		},
	}

	s := backends.New(
		secp256k1fx.NewKeychain(keys...),
		NewSignerBackend(b.state, ids.Empty, nil),
	)
	tx, err := backends.SignUnsigned(context.Background(), s, utx)
	if err != nil {
		return nil, err
	}
	return tx, tx.SyntacticVerify(b.ctx)
}

func (b *builder) NewAddPermissionlessDelegatorTx(
	stakeAmount,
	startTime,
	endTime uint64,
	nodeID ids.NodeID,
	rewardAddress ids.ShortID,
	keys []*secp256k1.PrivateKey,
	changeAddr ids.ShortID,
	memo []byte,
) (*txs.Tx, error) {
	toBurn := map[ids.ID]uint64{
		b.ctx.AVAXAssetID: b.cfg.AddPrimaryNetworkDelegatorFee,
	}
	toStake := map[ids.ID]uint64{
		b.ctx.AVAXAssetID: stakeAmount,
	}
	ins, unlockedOuts, lockedOuts, err := b.Spend(b.state, keys, toBurn, toStake, changeAddr)
	if err != nil {
		return nil, fmt.Errorf("couldn't generate tx inputs/outputs: %w", err)
	}
	// Create the tx
	utx := &txs.AddPermissionlessDelegatorTx{
		BaseTx: txs.BaseTx{BaseTx: avax.BaseTx{
			NetworkID:    b.ctx.NetworkID,
			BlockchainID: b.ctx.ChainID,
			Ins:          ins,
			Outs:         unlockedOuts,
			Memo:         memo,
		}},
		Validator: txs.Validator{
			NodeID: nodeID,
			Start:  startTime,
			End:    endTime,
			Wght:   stakeAmount,
		},
		Subnet:    constants.PrimaryNetworkID,
		StakeOuts: lockedOuts,
		DelegationRewardsOwner: &secp256k1fx.OutputOwners{
			Locktime:  0,
			Threshold: 1,
			Addrs:     []ids.ShortID{rewardAddress},
		},
	}

	s := backends.New(
		secp256k1fx.NewKeychain(keys...),
		NewSignerBackend(b.state, ids.Empty, nil),
	)
	tx, err := backends.SignUnsigned(context.Background(), s, utx)
	if err != nil {
		return nil, err
	}
	return tx, tx.SyntacticVerify(b.ctx)
}

func (b *builder) NewAddSubnetValidatorTx(
	weight,
	startTime,
	endTime uint64,
	nodeID ids.NodeID,
	subnetID ids.ID,
	keys []*secp256k1.PrivateKey,
	changeAddr ids.ShortID,
	memo []byte,
) (*txs.Tx, error) {
	toBurn := map[ids.ID]uint64{
		b.ctx.AVAXAssetID: b.cfg.TxFee,
	}
	toStake := make(map[ids.ID]uint64)
	ins, outs, _, err := b.Spend(b.state, keys, toBurn, toStake, changeAddr)
	if err != nil {
		return nil, fmt.Errorf("couldn't generate tx inputs/outputs: %w", err)
	}

	subnetAuth, _, err := b.Authorize(b.state, subnetID, keys)
	if err != nil {
		return nil, fmt.Errorf("couldn't authorize tx's subnet restrictions: %w", err)
	}

	// Create the tx
	utx := &txs.AddSubnetValidatorTx{
		BaseTx: txs.BaseTx{BaseTx: avax.BaseTx{
			NetworkID:    b.ctx.NetworkID,
			BlockchainID: b.ctx.ChainID,
			Ins:          ins,
			Outs:         outs,
			Memo:         memo,
		}},
		SubnetValidator: txs.SubnetValidator{
			Validator: txs.Validator{
				NodeID: nodeID,
				Start:  startTime,
				End:    endTime,
				Wght:   weight,
			},
			Subnet: subnetID,
		},
		SubnetAuth: subnetAuth,
	}

	s := backends.New(
		secp256k1fx.NewKeychain(keys...),
		NewSignerBackend(b.state, ids.Empty, nil),
	)
	tx, err := backends.SignUnsigned(context.Background(), s, utx)
	if err != nil {
		return nil, err
	}
	return tx, tx.SyntacticVerify(b.ctx)
}

func (b *builder) NewRemoveSubnetValidatorTx(
	nodeID ids.NodeID,
	subnetID ids.ID,
	keys []*secp256k1.PrivateKey,
	changeAddr ids.ShortID,
	memo []byte,
) (*txs.Tx, error) {
	toBurn := map[ids.ID]uint64{
		b.ctx.AVAXAssetID: b.cfg.TxFee,
	}
	toStake := make(map[ids.ID]uint64)
	ins, outs, _, err := b.Spend(b.state, keys, toBurn, toStake, changeAddr)
	if err != nil {
		return nil, fmt.Errorf("couldn't generate tx inputs/outputs: %w", err)
	}

	subnetAuth, _, err := b.Authorize(b.state, subnetID, keys)
	if err != nil {
		return nil, fmt.Errorf("couldn't authorize tx's subnet restrictions: %w", err)
	}

	// Create the tx
	utx := &txs.RemoveSubnetValidatorTx{
		BaseTx: txs.BaseTx{BaseTx: avax.BaseTx{
			NetworkID:    b.ctx.NetworkID,
			BlockchainID: b.ctx.ChainID,
			Ins:          ins,
			Outs:         outs,
			Memo:         memo,
		}},
		Subnet:     subnetID,
		NodeID:     nodeID,
		SubnetAuth: subnetAuth,
	}

	s := backends.New(
		secp256k1fx.NewKeychain(keys...),
		NewSignerBackend(b.state, ids.Empty, nil),
	)
	tx, err := backends.SignUnsigned(context.Background(), s, utx)
	if err != nil {
		return nil, err
	}
	return tx, tx.SyntacticVerify(b.ctx)
}

func (b *builder) NewTransferSubnetOwnershipTx(
	subnetID ids.ID,
	threshold uint32,
	ownerAddrs []ids.ShortID,
	keys []*secp256k1.PrivateKey,
	changeAddr ids.ShortID,
	memo []byte,
) (*txs.Tx, error) {
	toBurn := map[ids.ID]uint64{
		b.ctx.AVAXAssetID: b.cfg.TxFee,
	}
	toStake := make(map[ids.ID]uint64)
	ins, outs, _, err := b.Spend(b.state, keys, toBurn, toStake, changeAddr)
	if err != nil {
		return nil, fmt.Errorf("couldn't generate tx inputs/outputs: %w", err)
	}

	subnetAuth, _, err := b.Authorize(b.state, subnetID, keys)
	if err != nil {
		return nil, fmt.Errorf("couldn't authorize tx's subnet restrictions: %w", err)
	}

	utx := &txs.TransferSubnetOwnershipTx{
		BaseTx: txs.BaseTx{BaseTx: avax.BaseTx{
			NetworkID:    b.ctx.NetworkID,
			BlockchainID: b.ctx.ChainID,
			Ins:          ins,
			Outs:         outs,
			Memo:         memo,
		}},
		Subnet:     subnetID,
		SubnetAuth: subnetAuth,
		Owner: &secp256k1fx.OutputOwners{
			Threshold: threshold,
			Addrs:     ownerAddrs,
		},
	}

	s := backends.New(
		secp256k1fx.NewKeychain(keys...),
		NewSignerBackend(b.state, ids.Empty, nil),
	)
	tx, err := backends.SignUnsigned(context.Background(), s, utx)
	if err != nil {
		return nil, err
	}
	return tx, tx.SyntacticVerify(b.ctx)
}

func (b *builder) NewBaseTx(
	amount uint64,
	owner secp256k1fx.OutputOwners,
	keys []*secp256k1.PrivateKey,
	changeAddr ids.ShortID,
	memo []byte,
) (*txs.Tx, error) {
	amtToBurn, err := math.Add64(amount, b.cfg.TxFee)
	if err != nil {
		return nil, fmt.Errorf("amount (%d) + tx fee(%d) overflows", amount, b.cfg.TxFee)
	}
	toBurn := map[ids.ID]uint64{
		b.ctx.AVAXAssetID: amtToBurn,
	}
	toStake := make(map[ids.ID]uint64)
	ins, outs, _, err := b.Spend(b.state, keys, toBurn, toStake, changeAddr)
	if err != nil {
		return nil, fmt.Errorf("couldn't generate tx inputs/outputs: %w", err)
	}

	outs = append(outs, &avax.TransferableOutput{
		Asset: avax.Asset{ID: b.ctx.AVAXAssetID},
		Out: &secp256k1fx.TransferOutput{
			Amt:          amount,
			OutputOwners: owner,
		},
	})

	avax.SortTransferableOutputs(outs, txs.Codec)

	utx := &txs.BaseTx{
		BaseTx: avax.BaseTx{
			NetworkID:    b.ctx.NetworkID,
			BlockchainID: b.ctx.ChainID,
			Ins:          ins,
			Outs:         outs,
			Memo:         memo,
		},
	}

	s := backends.New(
		secp256k1fx.NewKeychain(keys...),
		NewSignerBackend(b.state, ids.Empty, nil),
	)
	tx, err := backends.SignUnsigned(context.Background(), s, utx)
	if err != nil {
		return nil, err
	}
	return tx, tx.SyntacticVerify(b.ctx)
}
