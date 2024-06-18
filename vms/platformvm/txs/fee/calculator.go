// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package fee

import (
	"errors"
	"fmt"
	"time"

	"github.com/ava-labs/avalanchego/codec"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/wrappers"
	"github.com/ava-labs/avalanchego/vms/components/avax"
	"github.com/ava-labs/avalanchego/vms/components/fee"
	"github.com/ava-labs/avalanchego/vms/components/verify"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs"
	"github.com/ava-labs/avalanchego/vms/platformvm/upgrade"
	"github.com/ava-labs/avalanchego/vms/secp256k1fx"
)

var (
	_ txs.Visitor = (*calculator)(nil)

	errFailedFeeCalculation = errors.New("failed fee calculation")
)

func NewStaticCalculator(
	config StaticConfig,
	upgradeTimes upgrade.Config,
	chainTime time.Time,
) *Calculator {
	return &Calculator{
		c: &calculator{
			upgrades:  upgradeTimes,
			staticCfg: config,
			time:      chainTime,
		},
	}
}

// NewDynamicCalculator must be used post E upgrade activation
func NewDynamicCalculator(
	feeManager *fee.Manager,
	maxGas fee.Gas,
) *Calculator {
	return &Calculator{
		c: &calculator{
			isEActive:  true,
			feeManager: feeManager,
			maxGas:     maxGas,
			// credentials are set when CalculateFee is called
		},
	}
}

type Calculator struct {
	c *calculator
}

func (c *Calculator) GetFee() uint64 {
	return c.c.fee
}

func (c *Calculator) ResetFee(newFee uint64) {
	c.c.fee = newFee
}

func (c *Calculator) ComputeFee(tx txs.UnsignedTx, creds []verify.Verifiable) (uint64, error) {
	c.c.credentials = creds
	c.c.fee = 0 // zero fee among different ComputeFee invocations (unlike Complexity which gets cumulated)
	err := tx.Visit(c.c)
	return c.c.fee, err
}

func (c *Calculator) AddFeesFor(complexity fee.Dimensions) (uint64, error) {
	return c.c.addFeesFor(complexity)
}

func (c *Calculator) RemoveFeesFor(unitsToRm fee.Dimensions) (uint64, error) {
	return c.c.removeFeesFor(unitsToRm)
}

func (c *Calculator) GetGas() fee.Gas {
	if c.c.feeManager != nil {
		return c.c.feeManager.GetBlockGas()
	}
	return 0
}

type calculator struct {
	// setup
	isEActive bool
	staticCfg StaticConfig

	// Pre E-upgrade inputs
	upgrades upgrade.Config
	time     time.Time

	// Post E-upgrade inputs
	feeManager  *fee.Manager
	maxGas      fee.Gas
	credentials []verify.Verifiable

	// outputs of visitor execution
	fee uint64
}

func (c *calculator) AddValidatorTx(*txs.AddValidatorTx) error {
	// AddValidatorTx is banned following Durango activation
	if !c.isEActive {
		c.fee = c.staticCfg.AddPrimaryNetworkValidatorFee
		return nil
	}
	return errFailedFeeCalculation
}

func (c *calculator) AddSubnetValidatorTx(tx *txs.AddSubnetValidatorTx) error {
	if !c.isEActive {
		c.fee = c.staticCfg.AddSubnetValidatorFee
		return nil
	}

	complexity, err := c.meterTx(tx, tx.Outs, tx.Ins)
	if err != nil {
		return err
	}

	_, err = c.addFeesFor(complexity)
	return err
}

func (c *calculator) AddDelegatorTx(*txs.AddDelegatorTx) error {
	// AddDelegatorTx is banned following Durango activation
	if !c.isEActive {
		c.fee = c.staticCfg.AddPrimaryNetworkDelegatorFee
		return nil
	}
	return errFailedFeeCalculation
}

func (c *calculator) CreateChainTx(tx *txs.CreateChainTx) error {
	if !c.isEActive {
		if c.upgrades.IsApricotPhase3Activated(c.time) {
			c.fee = c.staticCfg.CreateBlockchainTxFee
		} else {
			c.fee = c.staticCfg.CreateAssetTxFee
		}
		return nil
	}

	complexity, err := c.meterTx(tx, tx.Outs, tx.Ins)
	if err != nil {
		return err
	}

	_, err = c.addFeesFor(complexity)
	return err
}

func (c *calculator) CreateSubnetTx(tx *txs.CreateSubnetTx) error {
	if !c.isEActive {
		if c.upgrades.IsApricotPhase3Activated(c.time) {
			c.fee = c.staticCfg.CreateSubnetTxFee
		} else {
			c.fee = c.staticCfg.CreateAssetTxFee
		}
		return nil
	}

	complexity, err := c.meterTx(tx, tx.Outs, tx.Ins)
	if err != nil {
		return err
	}

	_, err = c.addFeesFor(complexity)
	return err
}

func (c *calculator) AdvanceTimeTx(*txs.AdvanceTimeTx) error {
	c.fee = 0 // no fees
	return nil
}

func (c *calculator) RewardValidatorTx(*txs.RewardValidatorTx) error {
	c.fee = 0 // no fees
	return nil
}

func (c *calculator) RemoveSubnetValidatorTx(tx *txs.RemoveSubnetValidatorTx) error {
	if !c.isEActive {
		c.fee = c.staticCfg.TxFee
		return nil
	}

	complexity, err := c.meterTx(tx, tx.Outs, tx.Ins)
	if err != nil {
		return err
	}

	_, err = c.addFeesFor(complexity)
	return err
}

func (c *calculator) TransformSubnetTx(tx *txs.TransformSubnetTx) error {
	if !c.isEActive {
		c.fee = c.staticCfg.TransformSubnetTxFee
		return nil
	}

	complexity, err := c.meterTx(tx, tx.Outs, tx.Ins)
	if err != nil {
		return err
	}

	_, err = c.addFeesFor(complexity)
	return err
}

func (c *calculator) TransferSubnetOwnershipTx(tx *txs.TransferSubnetOwnershipTx) error {
	if !c.isEActive {
		c.fee = c.staticCfg.TxFee
		return nil
	}

	complexity, err := c.meterTx(tx, tx.Outs, tx.Ins)
	if err != nil {
		return err
	}

	_, err = c.addFeesFor(complexity)
	return err
}

func (c *calculator) AddPermissionlessValidatorTx(tx *txs.AddPermissionlessValidatorTx) error {
	if !c.isEActive {
		if tx.Subnet != constants.PrimaryNetworkID {
			c.fee = c.staticCfg.AddSubnetValidatorFee
		} else {
			c.fee = c.staticCfg.AddPrimaryNetworkValidatorFee
		}
		return nil
	}

	outs := make([]*avax.TransferableOutput, len(tx.Outs)+len(tx.StakeOuts))
	copy(outs, tx.Outs)
	copy(outs[len(tx.Outs):], tx.StakeOuts)

	complexity, err := c.meterTx(tx, outs, tx.Ins)
	if err != nil {
		return err
	}

	_, err = c.addFeesFor(complexity)
	return err
}

func (c *calculator) AddPermissionlessDelegatorTx(tx *txs.AddPermissionlessDelegatorTx) error {
	if !c.isEActive {
		if tx.Subnet != constants.PrimaryNetworkID {
			c.fee = c.staticCfg.AddSubnetDelegatorFee
		} else {
			c.fee = c.staticCfg.AddPrimaryNetworkDelegatorFee
		}
		return nil
	}

	outs := make([]*avax.TransferableOutput, len(tx.Outs)+len(tx.StakeOuts))
	copy(outs, tx.Outs)
	copy(outs[len(tx.Outs):], tx.StakeOuts)

	complexity, err := c.meterTx(tx, outs, tx.Ins)
	if err != nil {
		return err
	}

	_, err = c.addFeesFor(complexity)
	return err
}

func (c *calculator) BaseTx(tx *txs.BaseTx) error {
	if !c.isEActive {
		c.fee = c.staticCfg.TxFee
		return nil
	}

	complexity, err := c.meterTx(tx, tx.Outs, tx.Ins)
	if err != nil {
		return err
	}

	_, err = c.addFeesFor(complexity)
	return err
}

func (c *calculator) ImportTx(tx *txs.ImportTx) error {
	if !c.isEActive {
		c.fee = c.staticCfg.TxFee
		return nil
	}

	ins := make([]*avax.TransferableInput, len(tx.Ins)+len(tx.ImportedInputs))
	copy(ins, tx.Ins)
	copy(ins[len(tx.Ins):], tx.ImportedInputs)

	complexity, err := c.meterTx(tx, tx.Outs, ins)
	if err != nil {
		return err
	}

	_, err = c.addFeesFor(complexity)
	return err
}

func (c *calculator) ExportTx(tx *txs.ExportTx) error {
	if !c.isEActive {
		c.fee = c.staticCfg.TxFee
		return nil
	}

	outs := make([]*avax.TransferableOutput, len(tx.Outs)+len(tx.ExportedOutputs))
	copy(outs, tx.Outs)
	copy(outs[len(tx.Outs):], tx.ExportedOutputs)

	complexity, err := c.meterTx(tx, outs, tx.Ins)
	if err != nil {
		return err
	}

	_, err = c.addFeesFor(complexity)
	return err
}

func (c *calculator) meterTx(
	uTx txs.UnsignedTx,
	allOuts []*avax.TransferableOutput,
	allIns []*avax.TransferableInput,
) (fee.Dimensions, error) {
	var complexity fee.Dimensions

	uTxSize, err := txs.Codec.Size(txs.CodecVersion, uTx)
	if err != nil {
		return complexity, fmt.Errorf("couldn't calculate UnsignedTx marshal length: %w", err)
	}
	complexity[fee.Bandwidth] = uint64(uTxSize)

	// meter credentials, one by one. Then account for the extra bytes needed to
	// serialize a slice of credentials (codec version bytes + slice size bytes)
	for i, cred := range c.credentials {
		c, ok := cred.(*secp256k1fx.Credential)
		if !ok {
			return complexity, fmt.Errorf("don't know how to calculate complexity of %T", cred)
		}
		credDimensions, err := fee.MeterCredential(txs.Codec, txs.CodecVersion, len(c.Sigs))
		if err != nil {
			return complexity, fmt.Errorf("failed adding credential %d: %w", i, err)
		}
		complexity, err = fee.Add(complexity, credDimensions)
		if err != nil {
			return complexity, fmt.Errorf("failed adding credentials: %w", err)
		}
	}
	complexity[fee.Bandwidth] += wrappers.IntLen // length of the credentials slice
	complexity[fee.Bandwidth] += codec.VersionSize

	for _, in := range allIns {
		inputDimensions, err := fee.MeterInput(txs.Codec, txs.CodecVersion, in)
		if err != nil {
			return complexity, fmt.Errorf("failed retrieving size of inputs: %w", err)
		}
		inputDimensions[fee.Bandwidth] = 0 // inputs bandwidth is already accounted for above, so we zero it
		complexity, err = fee.Add(complexity, inputDimensions)
		if err != nil {
			return complexity, fmt.Errorf("failed adding inputs: %w", err)
		}
	}

	for _, out := range allOuts {
		outputDimensions, err := fee.MeterOutput(txs.Codec, txs.CodecVersion, out)
		if err != nil {
			return complexity, fmt.Errorf("failed retrieving size of outputs: %w", err)
		}
		outputDimensions[fee.Bandwidth] = 0 // outputs bandwidth is already accounted for above, so we zero it
		complexity, err = fee.Add(complexity, outputDimensions)
		if err != nil {
			return complexity, fmt.Errorf("failed adding outputs: %w", err)
		}
	}

	return complexity, nil
}

func (c *calculator) addFeesFor(complexity fee.Dimensions) (uint64, error) {
	if c.feeManager == nil || complexity == fee.Empty {
		return 0, nil
	}

	feeCfg, err := GetDynamicConfig(c.isEActive)
	if err != nil {
		return 0, fmt.Errorf("failed adding fees: %w", err)
	}
	txGas, err := fee.ScalarProd(complexity, feeCfg.FeeDimensionWeights)
	if err != nil {
		return 0, fmt.Errorf("failed adding fees: %w", err)
	}

	if err := c.feeManager.CumulateGas(txGas, c.maxGas); err != nil {
		return 0, fmt.Errorf("failed cumulating complexity: %w", err)
	}
	fee, err := c.feeManager.CalculateFee(txGas)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errFailedFeeCalculation, err)
	}

	c.fee += fee
	return fee, nil
}

func (c *calculator) removeFeesFor(unitsToRm fee.Dimensions) (uint64, error) {
	if c.feeManager == nil || unitsToRm == fee.Empty {
		return 0, nil
	}

	feeCfg, err := GetDynamicConfig(c.isEActive)
	if err != nil {
		return 0, fmt.Errorf("failed adding fees: %w", err)
	}
	txGas, err := fee.ScalarProd(unitsToRm, feeCfg.FeeDimensionWeights)
	if err != nil {
		return 0, fmt.Errorf("failed adding fees: %w", err)
	}

	if err := c.feeManager.RemoveGas(txGas); err != nil {
		return 0, fmt.Errorf("failed removing units: %w", err)
	}
	fee, err := c.feeManager.CalculateFee(txGas)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errFailedFeeCalculation, err)
	}

	c.fee -= fee
	return fee, nil
}
