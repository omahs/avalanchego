// Copyright (C) 2019-2022, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package common

import (
	"github.com/ava-labs/avalanchego/snow"
	"github.com/ava-labs/avalanchego/snow/engine/common/tracker"
	"github.com/ava-labs/avalanchego/snow/validators"
)

// DefaultConfigTest returns a test configuration
func DefaultConfigTest() Config {
	ctx := snow.DefaultConsensusContextTest()
	beacons := validators.NewSet()

	connectedPeers := tracker.NewPeers()
	startupTracker := tracker.NewStartup(connectedPeers, 0)
	beacons.RegisterCallbackListener(startupTracker)

	return Config{
		Ctx:                            ctx,
		Beacons:                        beacons,
		StartupTracker:                 startupTracker,
		Sender:                         &SenderTest{},
		Bootstrapable:                  &BootstrapableTest{},
		Timer:                          &TimerTest{},
		AncestorsMaxContainersSent:     2000,
		AncestorsMaxContainersReceived: 2000,
		SharedCfg:                      &SharedConfig{},
	}
}
