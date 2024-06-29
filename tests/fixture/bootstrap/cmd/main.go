// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/ava-labs/avalanchego/config"
	"github.com/ava-labs/avalanchego/tests/fixture/bootstrap"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/logging"
)

const (
	bootstrappingImage = "avaplatform/avalanchego:latest"
	namespace          = "marun-bootstrap"
	pvcSize            = "128Mi"
)

// Load running jobs
// - add health check loop for running jobs
// If there are jobs that are not running
// - check the version and start test runs for jobs that have not validated the latest version
//   - map["network_sync-mode"]
// set a timer for n minutes (5, 30, etc) to check for a new version

// health check loops
// - every interval before timeout, check that pod is running and is healthy
// - if timeout, fail the job
// - if not running, fail the job
// - if healthy, pass the job
// pass/fail
//  - need to record details of last job finished for reporting to github (and logging)
//  - need to record image ID that has been tested for the job

// job configuration: network, sync mode, enabled (supports ad-hoc testing)
// job results: network, sync mode,

// One loop that checks versions periodically.
// - if a new version is available, start any test configurations that are not running
// Other loops that check health of running bootstrap nodes

func main() {
	flags := map[string]string{
		config.NetworkNameKey:              constants.FujiName,
		config.PublicIPKey:                 "127.0.0.1", // Ensure only ipv4 is used to ensure compatibility with client-go's port-forwarding facility
		config.LogLevelKey:                 logging.Debug.String(),
		config.HealthCheckFreqKey:          "2s",
		config.NetworkMaxReconnectDelayKey: "1s",
		config.StakingSignerKeyContentKey:  "ZpXuP/XHSbCgQA8mY2k+B3FWOTYiaw/DSobl8qaPBOc=",
		config.StakingCertContentKey:       "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUJFVENCdDZBREFnRUNBZ0VBTUFvR0NDcUdTTTQ5QkFNQ01BQXdJQmNOT1RreE1qTXhNREF3TURBd1doZ1AKTWpFeU5EQTJNakV3TlRVM016ZGFNQUF3V1RBVEJnY3Foa2pPUFFJQkJnZ3Foa2pPUFFNQkJ3TkNBQVFWa28yZApVNGFraE4wYU5zd2N4cTZpdmx5cGpheHBVYUo2NkxOdTJtQU45c2trRm0wdnplVHk3VUNNVk5RTzZ0R0JUZ3VLCjFDdWtQZGlTVmtqb2NySUVveUF3SGpBT0JnTlZIUThCQWY4RUJBTUNCNEF3REFZRFZSMFRBUUgvQkFJd0FEQUsKQmdncWhrak9QUVFEQWdOSkFEQkdBaUVBaTIwT2VTY25uOWtmdmd5aFAwa0tWZldXT1J1T0tlc2ZSRW4yS0dRZwpZUUVDSVFDZllkY0RWK1NoQ0NMUHBoM3orQ293a3BpWDQvQVU4eTFQWTREY2lQdUhMQT09Ci0tLS0tRU5EIENFUlRJRklDQVRFLS0tLS0K",
		config.StakingTLSKeyContentKey:     "LS0tLS1CRUdJTiBQUklWQVRFIEtFWS0tLS0tCk1JR0hBZ0VBTUJNR0J5cUdTTTQ5QWdFR0NDcUdTTTQ5QXdFSEJHMHdhd0lCQVFRZ0MxSGZ2VVJGMkluS3A1a0sKUHpDQk0vblB4S0I4dFVrcnNqWDNoemEvYjJXaFJBTkNBQVFWa28yZFU0YWtoTjBhTnN3Y3hxNml2bHlwamF4cApVYUo2NkxOdTJtQU45c2trRm0wdnplVHk3VUNNVk5RTzZ0R0JUZ3VLMUN1a1BkaVNWa2pvY3JJRQotLS0tLUVORCBQUklWQVRFIEtFWS0tLS0tCg==",
	}

	kubeconfigPath := os.Getenv("KUBECONFIG")
	kubeConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		log.Fatalf("Error building kubeconfig: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		log.Fatalf("Error creating kube clientset: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// TODO(marun) Capture version information
	// TODO(marun) Set annotations
	tags := `["bootstrap-network:testnet", "bootstrap-sync-mode:full", "is_subnet:no"]`
	annotations := map[string]string{
		"ad.datadoghq.com/avalanchego.check_names":  `["openmetrics"]`,
		"ad.datadoghq.com/avalanchego.init_configs": "[{}]",
		"ad.datadoghq.com/tolerate-unready":         "true",
		"ad.datadoghq.com/avalanchego.instances":    `[{"metrics": ["*"], "namespace": "avalanchego", "prometheus_url": "http://%%host%%:%%port_0%%/ext/metrics", "max_returned_metrics": 64000, "send_monotonic_counter": "false"}]`,
		"ad.datadoghq.com/tags":                     tags,
		"ad.datadoghq.com/avalanchego.logs":         fmt.Sprintf(`[{"source": "avalanchego", "service": "bootstrap-tester","tags": %s}]`, tags),
	}

	nodePod, err := bootstrap.StartNodePod(ctx, clientset, namespace, bootstrappingImage, pvcSize, annotations, flags)
	if err != nil {
		log.Fatalf("Error starting node pod: %v", err)
	}

	log.Printf("Node pod: %s.%s, PVC: %s.%s", namespace, nodePod.Name, namespace, nodePod.PVCName)

	// Look for pods that are actively running tests
	// If one of the test configurations is not running
	//  - check the commit of the latest image
	//  - check the execution history for the test configuration
	//  - if the current commit is not in the commit history
	//    - start a new test
	// watch for tests that have completed (nodes are healthy indicating successful bootstrap)
	// - update execution history
	//
}
