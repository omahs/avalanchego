// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/ava-labs/avalanchego/api/info"
	"github.com/ava-labs/avalanchego/config"
	"github.com/ava-labs/avalanchego/tests"
	"github.com/ava-labs/avalanchego/tests/bootstrap"
	"github.com/ava-labs/avalanchego/tests/fixture/e2e"
	"github.com/ava-labs/avalanchego/tests/fixture/tmpnet"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/logging"

	ginkgo "github.com/onsi/ginkgo/v2"
)

const (
	imageCurrent  = true
	containerName = "avalanchego"
	pvcSize       = "128Mi"
)

func TestE2E(t *testing.T) {
	ginkgo.RunSpecs(t, "bootstrap test suite")
}

var _ = ginkgo.BeforeSuite(func() {
	require := require.New(ginkgo.GinkgoT())

	// TODO(marun) Support configuring the registry via a flag
	imageName := "localhost:5001/avalanchego:latest"

	if imageCurrent {
		tests.Outf("{{yellow}}avalanchego image is up-to-date. skipping image build{{/}}\n")
	} else {
		relativePath := "tests/fixture/bootstrap/e2e"
		// Need the repo root to determine the build dockerfile paths
		repoRoot, err := getRepoRootPath(relativePath)
		require.NoError(err)

		// Get the Go version from build info to for use in building the image
		info, ok := debug.ReadBuildInfo()
		require.True(ok, "Couldn't read build info")
		goVersion := strings.TrimPrefix(info.GoVersion, "go")

		// Build the avalanchego image

		ginkgo.By("Building the avalanchego image")
		require.NoError(buildDockerImage(
			e2e.DefaultContext(),
			goVersion,
			repoRoot,
			filepath.Join(repoRoot, relativePath, "Dockerfile.avalanchego"),
			imageName,
		))

		// TODO(marun) Test the checking of the image version
	}

	ginkgo.By("Loading the image to the kind cluster")
	require.NoError(loadDockerImage(e2e.DefaultContext(), imageName))

	ginkgo.By("Configuring a kubernetes client")
	kubeconfigPath := os.Getenv("KUBECONFIG")
	kubeConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	require.NoError(err)
	clientset, err := kubernetes.NewForConfig(kubeConfig)
	require.NoError(err)

	// TODO(marun) Consider optionally deleting namespaces

	ginkgo.By("Creating a kube namespace to ensure isolation between test runs")
	namespaceSpec := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "bootstrap-test-e2e-",
		},
	}
	createdNamespace, err := clientset.CoreV1().Namespaces().Create(e2e.DefaultContext(), namespaceSpec, metav1.CreateOptions{})
	require.NoError(err)
	namespace := createdNamespace.Name

	flags := map[string]string{
		config.NetworkNameKey:            constants.LocalName,
		config.SybilProtectionEnabledKey: "false",
		config.HealthCheckFreqKey:        "500ms",              // Ensure rapid detection of a healthy state
		config.PublicIPKey:               "127.0.0.1",          // Ensure only ipv4 is used to ensure compatibility with client-go's port-forwarding facility
		config.LogDisplayLevelKey:        logging.Off.String(), // Display logging not needed since nodes run headless
		config.LogLevelKey:               logging.Debug.String(),
	}

	// TODO(marun) Create a unique name for the node pod
	ginkgo.By("Creating a pod for a single-node network")
	networkPod, err := bootstrap.StartNodePod(
		e2e.DefaultContext(),
		clientset,
		namespace,
		imageName,
		pvcSize,
		nil,
		nil,
		flags,
	)
	require.NoError(err)
	require.NoError(bootstrap.WaitForPodStatus(
		e2e.DefaultContext(),
		clientset,
		namespace,
		networkPod.Name,
		bootstrap.PodIsRunning,
	))

	bootstrapIP, err := WaitForPodIP(e2e.DefaultContext(), clientset, namespace, networkPod.Name)
	require.NoError(err)

	localPort, localPortStopChan, err := bootstrap.EnableLocalForwardForPod(kubeConfig, namespace, networkPod.Name, config.DefaultHTTPPort, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
	require.NoError(err)
	ginkgo.DeferCleanup(func() {
		close(localPortStopChan)
	})

	localNodeURI := fmt.Sprintf("http://127.0.0.1:%d", localPort)

	infoClient := info.NewClient(localNodeURI)
	bootstrapNodeID, _, err := infoClient.GetNodeID(e2e.DefaultContext())
	require.NoError(err)

	ginkgo.By(fmt.Sprintf("Waiting for the pod to report a healthy status at %s", localNodeURI))
	require.Eventually(func() bool {
		healthReply, err := tmpnet.CheckNodeHealth(e2e.DefaultContext(), localNodeURI)
		if err != nil {
			tests.Outf("Error checking node health: %s", err)
			return false
		}
		return healthReply.Healthy
	}, e2e.DefaultTimeout, e2e.DefaultPollingInterval)

	// TODO(marun) Expose version detection from a cmd
	ginkgo.By("Starting bootstrap test controller configured to test against the single-node network")

	testConfig := bootstrap.TestConfig{
		TestID: bootstrap.TestID{
			NetworkID: constants.LocalName,
		},
		PVCSize: "128Mi",
		Flags: map[string]string{
			config.BootstrapIPsKey:           fmt.Sprintf("%s:%d", bootstrapIP, config.DefaultStakingPort),
			config.BootstrapIDsKey:           bootstrapNodeID.String(),
			config.SybilProtectionEnabledKey: "false",
		},
	}

	controller, err := bootstrap.NewTestController(
		[]bootstrap.TestConfig{testConfig},
		namespace,
		imageName,
	)
	require.NoError(err)

	go func() {
		if err := controller.Run(); err != nil {
			panic(err)
		}
	}()

	ginkgo.By("Waiting for the bootstrap test controller to complete testing")
	e2e.Eventually(func() bool {
		status := controller.Status[testConfig.TestID]
		return !status.IsRunning && len(status.Commit) > 0
	}, e2e.DefaultTimeout, e2e.DefaultPollingInterval, "bootstrap test was not performed before timeout")
})

var _ = ginkgo.Describe("[Bootstrap Tester]", func() {
	require := require.New(ginkgo.GinkgoT())

	ginkgo.It("should enable testing node bootstrap", func() {
		require.True(true)
	})

})

// Image build and push are implemented by calling the docker CLI instead of using the docker SDK to
// avoid bumping avalanchego runtime dependencies for the sake of an e2e test.
//
// TODO(marun) Update to use the docker SDK if/when reasonable

func buildDockerImage(ctx context.Context, goVersion string, buildPath string, dockerfilePath string, imageName string) error {
	cmd := exec.CommandContext(
		ctx,
		"docker",
		"build",
		"-t", imageName,
		"-f", dockerfilePath,
		"--build-arg", "GO_VERSION="+goVersion,
		buildPath,
	)
	return runCommand(cmd)
}

func loadDockerImage(ctx context.Context, imageName string) error {
	return runCommand(exec.CommandContext(ctx, "kind", "load", "docker-image", imageName))
}

// runCommand runs the provided command and captures output to stdout and stderr so if an
// error occurs the output can be included to provide sufficient detail for troubleshooting.
func runCommand(cmd *exec.Cmd) error {
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Error: %w\nOutput:\n%s\n", err, output)
	}
	return nil
}

func getRepoRootPath(suffix string) (string, error) {
	// - When executed via a test binary, the working directory will be wherever
	// the binary is executed from, but scripts should require execution from
	// the repo root.
	//
	// - When executed via ginkgo (nicer for development + supports
	// parallel execution) the working directory will always be the
	// target path (e.g. [repo root]./tests/bootstrap/e2e) and getting the repo
	// root will require stripping the target path suffix.
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(cwd, suffix), nil
}

func WaitForPodIP(ctx context.Context, clientset kubernetes.Interface, namespace string, name string) (string, error) {
	watch, err := clientset.CoreV1().Pods(namespace).Watch(ctx, metav1.SingleObject(metav1.ObjectMeta{Name: name}))
	if err != nil {
		return "", fmt.Errorf("failed to watch pod: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("failed to wait for pod IP before timeout: %w", ctx.Err())
		case event := <-watch.ResultChan():
			pod, ok := event.Object.(*corev1.Pod)
			if !ok {
				return "", fmt.Errorf("unexpected type: %T", event.Object)
			}
			if pod.Status.PodIP != "" {
				return pod.Status.PodIP, nil
			}
		}
	}
}
