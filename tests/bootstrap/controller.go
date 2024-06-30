// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/ava-labs/avalanchego/config"
	"github.com/ava-labs/avalanchego/tests/fixture/tmpnet"
	"github.com/ava-labs/avalanchego/utils/logging"
	"github.com/ava-labs/avalanchego/version"
)

const (
	ContainerName = "avalanchego"

	mountPath = "/data"

	DefaultTimeout = 2 * time.Minute
)

type TestID struct {
	NetworkID string
	StateSync bool
}

type TestResult struct {
	TestID

	Image     ImageDetails
	StartTime time.Time
	EndTime   time.Time
	PodName   string
	Error     string
}

type TestConfig struct {
	TestID

	PVCSize string
	Flags   map[string]string
}

type TestConfigStatus struct {
	// Whether the test is currently running
	IsRunning bool
	// The avalanchego commit that is being tested
	Commit string
}

type TestController struct {
	TestConfigs []TestConfig
	Namespace   string
	ImageName   string
	Status      map[TestID]TestConfigStatus

	// TODO(marun) Rename to KubeClientset
	Clientset  *kubernetes.Clientset
	KubeConfig *restclient.Config

	statusLock sync.RWMutex
}

func (c *TestController) GetStatus(testID TestID) (TestConfigStatus, bool) {
	c.statusLock.RLock()
	defer c.statusLock.RUnlock()
	status, ok := c.Status[testID]
	return status, ok
}

func (c *TestController) SetStatus(testID TestID, status TestConfigStatus) {
	c.statusLock.Lock()
	defer c.statusLock.Unlock()

	c.Status[testID] = status

	// TODO(marun) Write to configmap to allow restart
}

type ImageDetails struct {
	// The identifier used to start the image
	ImageID string
	// The versions reported by the image's avalanchego binary
	Versions version.Versions
}

func NewTestController(configs []TestConfig, namespace string, imageName string) (*TestController, error) {

	// Initialize the clientset from the kubeconfig

	// TODO(marun) Use InCluster?

	kubeconfigPath := os.Getenv("KUBECONFIG")
	kubeConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kube clientset: %w", err)
	}

	return &TestController{
		TestConfigs: configs,
		Namespace:   namespace,
		ImageName:   imageName,
		Status:      map[TestID]TestConfigStatus{},
		Clientset:   clientset,
		KubeConfig:  kubeConfig,
	}, nil
}

func (c *TestController) Run() error {
	// TODO(marun) Support detecting running jobs (reuse )
	// - read pods in the namespace with labels indicating running jobs

	return wait.PollImmediateInfinite(time.Minute, func() (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
		defer cancel()

		imageDetails, err := GetImageDetails(ctx, c.Clientset, c.Namespace, c.ImageName)
		if err != nil {
			log.Printf("failed to get image versions: %v", err)
			return false, nil
		}

		for _, cfg := range c.TestConfigs {
			status, ok := c.GetStatus(cfg.TestID)
			if !ok || (!status.IsRunning && status.Commit != imageDetails.Versions.Commit) {
				c.SetStatus(cfg.TestID, TestConfigStatus{
					IsRunning: true,
					Commit:    status.Commit,
				})
				go c.RunJob(cfg, *imageDetails)
			}
		}
		return false, nil
	})
}

func (c *TestController) RunJob(cfg TestConfig, imageDetails ImageDetails) {
	result := TestResult{
		TestID:    cfg.TestID,
		StartTime: time.Now(),
		Image:     imageDetails,
	}

	// On error, log result, add the test result to the set of results (configmap) and mark job as not running
	// On success, log result, add the test result to the set of results (configmap) and mark job as not running
	podName, err := c.TestBootstrap(cfg, imageDetails)
	result.EndTime = time.Now()
	result.PodName = podName
	status := TestConfigStatus{
		IsRunning: false,
	}
	if err != nil {
		result.Error = err.Error()
	} else {
		status.Commit = imageDetails.Versions.Commit
	}

	c.SetStatus(cfg.TestID, status)

	// TODO(marun) Stop the pod

	// TODO(marun) Write result to a configmap

	log.Printf("Test run complete: %v", result)
}

func (c *TestController) TestBootstrap(cfg TestConfig, imageDetails ImageDetails) (string, error) {
	log.Printf("Starting bootstrap test for network %s, state sync %b, commit %s", cfg.NetworkID, cfg.StateSync, imageDetails.Versions.Commit)

	flags := map[string]string{
		config.NetworkNameKey:     cfg.NetworkID,
		config.HealthCheckFreqKey: "500ms", // Ensure rapid detection of a healthy state
		// TODO(marun) This only needs to be set for test purposes?
		config.PublicIPKey:        "127.0.0.1", // Ensure only ipv4 is used to ensure compatibility with client-go's port-forwarding facility
		config.LogDisplayLevelKey: logging.Info.String(),
		config.LogLevelKey:        logging.Off.String(),
	}
	for k, v := range cfg.Flags {
		flags[k] = v
	}

	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()

	podLabels := map[string]string{
		"network":    cfg.NetworkID,
		"state-sync": strconv.FormatBool(cfg.StateSync),
		"commit":     imageDetails.Versions.Commit,
	}

	// TODO(marun) Use the image id from imageDetails to bootstrap with

	nodePod, err := StartNodePod(
		ctx,
		c.Clientset,
		c.Namespace,
		c.ImageName,
		cfg.PVCSize,
		GetDataDogAnnotations(cfg),
		podLabels,
		flags,
	)
	if err != nil {
		wrappedErr := fmt.Errorf("failed to start node pod: %w", err)
		// Resources are left in place for debugging
		return "", wrappedErr
	}

	err = WaitForPodStatus(
		ctx,
		c.Clientset,
		c.Namespace,
		nodePod.Name,
		PodIsRunning,
	)
	if err != nil {
		wrappedErr := fmt.Errorf("failed to wait for pod running: %w", err)
		// Log enough details to fix the problem. Don't delete.
		return nodePod.Name, wrappedErr
	}

	log.Printf("Bootstrap test running for network %s, sync mode %s, commit %s", cfg.NetworkID, strconv.FormatBool(cfg.StateSync), imageDetails.Versions.Commit)

	// The pod IP needs to be set before enabling the local forward
	// TODO(marun) Move to enableLocalForwardForPod
	_, err = WaitForPodIP(ctx, c.Clientset, c.Namespace, nodePod.Name)
	if err != nil {
		wrappedErr := fmt.Errorf("failed to wait for pod IP: %w", err)
		return "", wrappedErr
	}

	// TODO(marun) Use the pod port directly when this controller is running as a pod
	localPort, localPortStopChan, err := EnableLocalForwardForPod(
		c.KubeConfig,
		c.Namespace,
		nodePod.Name,
		config.DefaultHTTPPort,
		os.Stdout,
		os.Stderr,
	)
	if err != nil {
		// TODO(marun) Stop the pod?
		return nodePod.Name, fmt.Errorf("failed to enable local forward: %w", err)
	}
	defer close(localPortStopChan)

	localNodeURI := fmt.Sprintf("http://127.0.0.1:%d", localPort)

	log.Printf("Waiting for node to finish bootstrapping for network %s, sync mode %s, commit %s", cfg.NetworkID, strconv.FormatBool(cfg.StateSync), imageDetails.Versions.Commit)

	// TODO(marun) Make these adjustable to simplify testing
	err = wait.PollImmediate(5*time.Second, 2*time.Minute, func() (bool, error) {
		// TODO(marun) Check if the pod is running

		// TODO(marun) Create constant for this timeout?
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		log.Printf("Checking if node is healthy for network %s, sync mode %s, commit %s", cfg.NetworkID, strconv.FormatBool(cfg.StateSync), imageDetails.Versions.Commit)

		healthReply, err := tmpnet.CheckNodeHealth(ctx, localNodeURI)
		if err != nil {
			log.Printf("Error checking node health: %v", err)
			return false, nil
		}

		return healthReply.Healthy, nil
	})

	return nodePod.Name, err
}

func GetImageDetails(
	ctx context.Context,
	clientset *kubernetes.Clientset,
	namespace string,
	imageName string,
) (*ImageDetails, error) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "avalanchego-version-check-",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    ContainerName,
					Command: []string{"./avalanchego"},
					Args:    []string{"--version-json"},
					Image:   imageName,
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
	createdPod, err := clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	err = WaitForPodStatus(ctx, clientset, namespace, createdPod.Name, PodHasTerminated)
	if err != nil {
		return nil, err
	}

	terminatedPod, err := clientset.CoreV1().Pods(namespace).Get(ctx, createdPod.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	// Get the image id for the avalanchego image
	imageID := ""
	for _, status := range terminatedPod.Status.ContainerStatuses {
		if status.Name == ContainerName {
			imageID = status.ImageID
			break
		}
	}
	if len(imageID) == 0 {
		return nil, fmt.Errorf("failed to get image id for pod %s.%s", namespace, createdPod.Name)
	}
	imageIDParts := strings.Split(imageID, ":")
	if len(imageIDParts) != 2 {
		return nil, fmt.Errorf("unexpected image id format: %s", imageID)
	}

	// Request the logs
	req := clientset.CoreV1().Pods(namespace).GetLogs(createdPod.Name, &corev1.PodLogOptions{
		Container: ContainerName,
	})

	// Stream the logs
	readCloser, err := req.Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer readCloser.Close()

	// Marshal the logs into the versions type
	bytes, err := io.ReadAll(readCloser)
	if err != nil {
		return nil, err
	}
	versions := version.Versions{}
	err = json.Unmarshal(bytes, &versions)
	if err != nil {
		return nil, err
	}

	// Only delete the pod if successful to aid in debugging
	err = clientset.CoreV1().Pods(namespace).Delete(ctx, createdPod.Name, metav1.DeleteOptions{})
	if err != nil {
		return nil, err
	}

	return &ImageDetails{
		ImageID:  imageIDParts[1],
		Versions: versions,
	}, nil
}

func WaitForPodStatus(
	ctx context.Context,
	clientset *kubernetes.Clientset,
	namespace string,
	name string,
	acceptable func(*corev1.PodStatus) bool,
) error {
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

func PodIsRunning(status *corev1.PodStatus) bool {
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

func PodHasTerminated(status *corev1.PodStatus) bool {
	return status.Phase == corev1.PodSucceeded || status.Phase == corev1.PodFailed
}

type NodePod struct {
	Namespace string
	Name      string
	PVCName   string
}

func GetDataDogAnnotations(cfg TestConfig) map[string]string {
	tags := fmt.Sprintf(`["bootstrap-network:%s", "bootstrap-sync-mode:%s", "is_subnet:no"]`, cfg.NetworkID, strconv.FormatBool(cfg.StateSync))
	// TODO(marun) Make the container name configurable
	return map[string]string{
		"ad.datadoghq.com/avalanchego.check_names":  `["openmetrics"]`,
		"ad.datadoghq.com/avalanchego.init_configs": "[{}]",
		"ad.datadoghq.com/tolerate-unready":         "true",
		"ad.datadoghq.com/avalanchego.instances":    `[{"metrics": ["*"], "namespace": "avalanchego", "prometheus_url": "http://%%host%%:%%port_0%%/ext/metrics", "max_returned_metrics": 64000, "send_monotonic_counter": "false"}]`,
		"ad.datadoghq.com/tags":                     tags,
		"ad.datadoghq.com/avalanchego.logs":         fmt.Sprintf(`[{"source": "avalanchego", "service": "bootstrap-tester","tags": %s}]`, tags),
	}
}

func StartNodePod(
	ctx context.Context,
	clientset *kubernetes.Clientset,
	namespace string,
	imageName string,
	pvcSize string,
	podAnnotations map[string]string,
	podLabels map[string]string,
	flags map[string]string,
) (*NodePod, error) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "avalanche-pvc-",
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(pvcSize),
				},
			},
		},
	}
	createdPVC, err := clientset.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	log.Printf("Created PVC %s.%s", namespace, createdPVC.Name)

	// Ensure the pvc mount path matches the data dir for the node
	flags[config.DataDirKey] = mountPath

	// TODO(marun) Ensure images aren't pulled for testing
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "avalanche-node-",
			Annotations:  podAnnotations,
			Labels:       podLabels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  ContainerName,
					Image: imageName,
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "data",
							MountPath: mountPath,
						},
					},
					Env: StringMapToEnvVarSlice(flags),
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
			Volumes: []corev1.Volume{
				{
					Name: "data",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: createdPVC.Name,
						},
					},
				},
			},
		},
	}
	createdPod, err := clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	log.Printf("Created Pod %s.%s", namespace, createdPod.Name)

	return &NodePod{
		Namespace: namespace,
		Name:      createdPod.Name,
		PVCName:   createdPVC.Name,
	}, nil
}

func StringMapToEnvVarSlice(mapping map[string]string) []corev1.EnvVar {
	envVars := make([]corev1.EnvVar, len(mapping))
	var i int
	for k, v := range mapping {
		envVars[i] = corev1.EnvVar{
			Name:  config.KeyToEnvVar(k),
			Value: v,
		}
		i++
	}
	return envVars
}

// enableLocalForwardForPod enables traffic forwarding from a local
// port to the specified pod with client-go. The returned stop channel
// should be closed to stop the port forwarding.
func EnableLocalForwardForPod(kubeConfig *restclient.Config, namespace string, name string, port int, out, errOut io.Writer) (uint16, chan struct{}, error) {
	log.Printf("Forwarding traffic from a local port to port %d of pod %s.%s via the Kube API", port, namespace, name)

	transport, upgrader, err := spdy.RoundTripperFor(kubeConfig)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to create round tripper: %v", err)
	}

	dialer := spdy.NewDialer(
		upgrader,
		&http.Client{
			Transport: transport,
		},
		http.MethodPost,
		&url.URL{
			Scheme: "https",
			Path:   fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", namespace, name),
			Host:   strings.TrimPrefix(kubeConfig.Host, "https://"),
		},
	)
	ports := []string{fmt.Sprintf("0:%d", port)}

	// Need to specify 127.0.0.1 to ensure that forwarding is only
	// attempted for the ipv4 address of the pod. By default, kind is
	// deployed with only ipv4, and attempting to connect to a pod
	// with ipv6 will fail.
	// TODO(marun) This should no longer be required
	addresses := []string{"127.0.0.1"}

	stopChan, readyChan := make(chan struct{}, 1), make(chan struct{}, 1)
	forwarder, err := portforward.NewOnAddresses(dialer, addresses, ports, stopChan, readyChan, out, errOut)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to create forwarder")
	}

	go func() {
		if err := forwarder.ForwardPorts(); err != nil {
			// TODO(marun) Need better error handling here? Or is ok for test-only usage?
			panic(err)
		}
	}()

	<-readyChan // Wait for port forwarding to be ready

	// Retrieve the dynamically allocated local port
	forwardedPorts, err := forwarder.GetPorts()
	if err != nil {
		close(stopChan)
		return 0, nil, fmt.Errorf("failed to get forwarded ports: %v", err)
	}
	if len(forwardedPorts) == 0 {
		close(stopChan)
		return 0, nil, fmt.Errorf("failed to find at least one forwarded port: %v", err)
	}
	return forwardedPorts[0].Local, stopChan, nil
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
