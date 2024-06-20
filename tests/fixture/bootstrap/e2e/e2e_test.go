// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ava-labs/avalanchego/utils/logging"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/ava-labs/avalanchego/api/info"
	"github.com/ava-labs/avalanchego/config"
	"github.com/ava-labs/avalanchego/tests"
	"github.com/ava-labs/avalanchego/tests/fixture/bootstrap"
	"github.com/ava-labs/avalanchego/tests/fixture/e2e"
	"github.com/ava-labs/avalanchego/tests/fixture/tmpnet"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/version"

	// "github.com/ava-labs/avalanchego/tests/fixture/tmpnet"

	ginkgo "github.com/onsi/ginkgo/v2"
)

const (
	imageCurrent  = true
	containerName = "avalanchego"
)

func TestE2E(t *testing.T) {
	ginkgo.RunSpecs(t, "bootstrap test suite")
}

var _ = ginkgo.BeforeSuite(func() {
	require := require.New(ginkgo.GinkgoT())

	// TODO(marun) Support configuring the registry via a flag
	imageName := "avalanchego:latest"

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

		// Build and push the avalanchego image

		ginkgo.By("Building the avalanchego image")
		require.NoError(buildDockerImage(
			e2e.DefaultContext(),
			goVersion,
			repoRoot,
			filepath.Join(repoRoot, relativePath, "Dockerfile.avalanchego"),
			imageName,
		))
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
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "bootstrap-test-e2e-",
		},
	}
	createdNamespace, err := clientset.CoreV1().Namespaces().Create(e2e.DefaultContext(), namespace, metav1.CreateOptions{})
	require.NoError(err)

	flags := map[string]string{
		config.NetworkNameKey:            constants.LocalName,
		config.SybilProtectionEnabledKey: "false",
		config.HealthCheckFreqKey:        "500ms",              // Ensure rapid detection of a healthy state
		config.PublicIPKey:               "127.0.0.1",          // Ensure only ipv4 is used to ensure compatibility with client-go's port-forwarding facility
		config.LogDisplayLevelKey:        logging.Off.String(), // Display logging not needed since nodes run headless
		config.LogLevelKey:               logging.Debug.String(),
	}

	ginkgo.By("Creating a pod for a single-node network")
	createdPod := startNodePod(e2e.DefaultContext(), clientset, createdNamespace.Name, imageName, flags, nil)
	require.NoError(waitForPodStatus(e2e.DefaultContext(), clientset, createdPod.Namespace, createdPod.Name, podIsRunning))

	bootstrapIP, err := bootstrap.WaitForPodIP(e2e.DefaultContext(), clientset, createdPod.Namespace, createdPod.Name)
	require.NoError(err)

	// TODO(marun) Tunnel through the kube api to the pod's HTTP port

	// localPort := enableLocalForwardForPodKubectl(service.Namespace, service.Name, config.DefaultHTTPPort)

	localPort, localPortStopChan := enableLocalForwardForPodClientGo(kubeConfig, createdPod.Namespace, createdPod.Name, config.DefaultHTTPPort)
	ginkgo.DeferCleanup(func() {
		close(localPortStopChan)
	})

	localNodeURI := fmt.Sprintf("http://127.0.0.1:%d", localPort)

	ginkgo.By(fmt.Sprintf("Waiting for the pod to report a healthy status at %s", localNodeURI))
	require.Eventually(func() bool {
		healthy, err := tmpnet.CheckNodeHealth(e2e.DefaultContext(), localNodeURI)
		if err != nil {
			tests.Outf("Error checking node health: %s", err)
			return false
		}
		return healthy
	}, e2e.DefaultTimeout, e2e.DefaultPollingInterval)

	// TODO(marun) Get the current latest image and start a pod to check its version and digest.
	// TODO(marun) Start bootstrap pods with imagename:sha256:version to ensure the specific version is used
	// TODO(marun) Reference the pod uuid in the test output for traceability
	// - bootstrap config {network, bootstrapType}
	//   - image digest (and associated avalanchego version info )
	//   - pod details {uuid, createdTimestamp}
	//   - what else?
	// TODO(marun) Enable timeout?
	ginkgo.By("Starting a pod to check the avalanchego version and image digest of the image tagged with 'latest'")

	versionPod := startNodePod(e2e.DefaultContext(), clientset, createdNamespace.Name, imageName, flags, []string{"--version-json"})
	require.NoError(waitForPodStatus(e2e.DefaultContext(), clientset, versionPod.Namespace, versionPod.Name, podHasTerminated))

	versionPod, err = clientset.CoreV1().Pods(versionPod.Namespace).Get(e2e.DefaultContext(), versionPod.Name, metav1.GetOptions{})
	require.NoError(err)

	// Get the image id for the avalanchego image
	imageID := ""
	for _, status := range versionPod.Status.ContainerStatuses {
		if status.Name == containerName {
			imageID = status.ImageID
			break
		}
	}
	require.NotEmpty(imageID)
	imageIDParts := strings.Split(imageID, ":")
	require.Len(imageIDParts, 2)
	//imageSHA := imageIDParts[1]

	// Request the logs
	req := clientset.CoreV1().Pods(versionPod.Namespace).GetLogs(versionPod.Name, &corev1.PodLogOptions{
		Container: containerName,
	})

	// Stream the logs
	readCloser, err := req.Stream(e2e.DefaultContext())
	require.NoError(err)
	defer readCloser.Close()

	// Read and print the logs
	bytes, err := io.ReadAll(readCloser)
	require.NoError(err)

	versions := &version.Versions{}
	require.NoError(json.Unmarshal(bytes, &versions))

	// Delete the pod
	require.NoError(clientset.CoreV1().Pods(versionPod.Namespace).Delete(e2e.DefaultContext(), versionPod.Name, metav1.DeleteOptions{}))

	infoClient := info.NewClient(localNodeURI)
	bootstrapNodeID, _, err := infoClient.GetNodeID(e2e.DefaultContext())
	require.NoError(err)

	// TODO(marun) Use the image id from the version check as the image to bootstrap with
	bootstrappingImage := imageName

	ginkgo.By("Creating a pod to validate bootstrap")

	// Create node to bootstrap from the single-node network
	flags[config.BootstrapIPsKey] = fmt.Sprintf("%s:%d", bootstrapIP, config.DefaultStakingPort)
	flags[config.BootstrapIDsKey] = bootstrapNodeID.String()
	// Uses the image ID instead of image name to use a known version of the image
	bootstrappingPod := startNodePod(e2e.DefaultContext(), clientset, createdNamespace.Name, bootstrappingImage, flags, nil)
	require.NoError(waitForPodStatus(e2e.DefaultContext(), clientset, bootstrappingPod.Namespace, bootstrappingPod.Name, podIsRunning))

	localBootstrappingPort, localBootstrappingStopChan := enableLocalForwardForPodClientGo(kubeConfig, bootstrappingPod.Namespace, bootstrappingPod.Name, config.DefaultHTTPPort)
	ginkgo.DeferCleanup(func() {
		close(localBootstrappingStopChan)
	})

	localBootstrappingURI := fmt.Sprintf("http://127.0.0.1:%d", localBootstrappingPort)

	ginkgo.By(fmt.Sprintf("Waiting for the bootstrapping pod to report a healthy status at %s", localBootstrappingURI))
	require.Eventually(func() bool {
		healthy, err := tmpnet.CheckNodeHealth(e2e.DefaultContext(), localBootstrappingURI)
		if err != nil {
			tests.Outf("Error checking node health: %s", err)
			return false
		}
		return healthy
	}, e2e.DefaultTimeout, e2e.DefaultPollingInterval)

	// Write status somewhere
	// - to configmap in the cluster

})

func startNodePod(ctx context.Context, clientset *kubernetes.Clientset, namespace string, imageName string, flags map[string]string, args []string) *corev1.Pod {
	require := require.New(ginkgo.GinkgoT())

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "avalanche-node",
			Labels: map[string]string{
				// TODO(marun) What is the purpose of this label?
				"app": "single-node-network",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    containerName,
					Command: []string{"./avalanchego"},
					Args:    args,
					Image:   imageName,
					Env:     bootstrap.StringMapToEnvVarSlice(flags),
					// Images are loaded manually for testing purposes
					ImagePullPolicy: corev1.PullNever,
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
	createdPod, err := clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	require.NoError(err)

	// defer func() {
	// 	err := clientset.CoreV1().Pods(createdNamespace.Name).Delete(e2e.DefaultContext(), createdPod.Name, metav1.DeleteOptions{})
	// 	require.NoError(err)
	// }()

	// ginkgo.By("Waiting for the pod's IP address to be reported in its status")
	// bootstrapIP, err := bootstrap.WaitForPodIP(e2e.DefaultContext(), clientset, createdNamespace.Name, createdPod.Name)
	// require.NoError(err)

	return createdPod
}

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

func podIsRunning(status *corev1.PodStatus) bool {
	if status.Phase != corev1.PodRunning {
		return false
	}

	for _, containerStatus := range status.ContainerStatuses {
		if !containerStatus.Ready {
			return false
		}
	}
	return true
}

func podHasTerminated(status *corev1.PodStatus) bool {
	return status.Phase == corev1.PodSucceeded || status.Phase == corev1.PodFailed
}

// waitForPodReady waits for a pod to be ready.
func waitForPodStatus(ctx context.Context, clientset *kubernetes.Clientset, namespace string, name string, acceptable func(*corev1.PodStatus) bool) error {
	watch, err := clientset.CoreV1().Pods(namespace).Watch(ctx, metav1.SingleObject(metav1.ObjectMeta{Name: name}))
	if err != nil {
		return err
	}

	for {
		select {
		case event := <-watch.ResultChan():
			pod, ok := event.Object.(*corev1.Pod)
			if !ok {
				return fmt.Errorf("expected pod type")
			}

			if acceptable(&pod.Status) {
				return nil
			}
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for pod readiness")
		}
	}

	return nil
}

// enableLocalForwardForPodKubectl enables traffic forwarding from a local port to the specified pod with kubectl.
func enableLocalForwardForServiceKubectl(namespace string, name string, port uint16) uint16 {
	require := require.New(ginkgo.GinkgoT())

	ginkgo.By("Forwarding traffic from a local port to the pod with kubectl")

	// Rebind a randomly assigned port to avoid having to parse kubectl output to determine the local port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(err)
	localPort := uint16(listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()

	// kubectl needs to be in the path
	args := []string{
		"kubectl",
		"--namespace", namespace,
		"--address", "127.0.0.1",
		"port-forward",
		fmt.Sprintf("service/%s", name),
		fmt.Sprintf("%d:%d", localPort, port),
	}
	fullCommand := strings.Join(args, " ")
	tests.Outf("Running: %s\n", fullCommand)
	cmd := exec.Command("bash", "-c", fullCommand)
	cmd.Env = os.Environ()
	cmd.Stdout = ginkgo.GinkgoWriter
	cmd.Stderr = ginkgo.GinkgoWriter
	require.NoError(cmd.Start())
	// TODO(marun) Enable cleanup of command

	// Wait for the port forward to be ready
	addrAndPort := fmt.Sprintf("127.0.0.1:%d", localPort)
	require.Eventually(func() bool {
		_, err := net.Dial("tcp", addrAndPort)
		if err != nil {
			tests.Outf("Failed to connect to %s: %s", addrAndPort, err)
			return false
		}
		return true

	}, e2e.DefaultTimeout, e2e.DefaultPollingInterval)

	return localPort
}

// enableLocalForwardForPodClientGo enables traffic forwarding from a local port to the specified pod with
// client-go. The returned stop channel should be closed to stop the port forwarding.
func enableLocalForwardForPodClientGo(kubeConfig *restclient.Config, namespace string, name string, port int) (uint16, chan struct{}) {
	require := require.New(ginkgo.GinkgoT())

	ginkgo.By("Forwarding traffic from a local port to the pod with client-go")

	// TODO(marun) Enable testing of bootstrap tester as a pod

	transport, upgrader, err := spdy.RoundTripperFor(kubeConfig)
	require.NoError(err)

	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", namespace, name)
	tests.Outf("path %s", path)

	dialer := spdy.NewDialer(
		upgrader,
		&http.Client{
			Transport: transport,
		},
		http.MethodPost,
		&url.URL{
			Scheme: "https",
			Path:   path,
			Host:   strings.TrimPrefix(kubeConfig.Host, "https://"),
		},
	)
	ports := []string{fmt.Sprintf("0:%d", port)}

	// Need to specify 127.0.0.1 to ensure that forwarding is only
	// attempted for the ipv4 address of the pod. By default, kind is
	// deployed with only ipv4, and attempting to connect to a pod
	// with ipv6 will fail.
	addresses := []string{"127.0.0.1"}

	stopChan, readyChan := make(chan struct{}, 1), make(chan struct{}, 1)
	forwarder, err := portforward.NewOnAddresses(dialer, addresses, ports, stopChan, readyChan, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
	require.NoError(err)

	go func() {
		defer ginkgo.GinkgoRecover()
		if err := forwarder.ForwardPorts(); err != nil {
			panic(err)
		}
	}()

	<-readyChan // Wait for port forwarding to be ready

	// Retrieve the dynamically allocated local port
	forwardedPorts, err := forwarder.GetPorts()
	require.NoError(err)
	require.NotZero(len(forwardedPorts))
	return forwardedPorts[0].Local, stopChan
}
