// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package bootstrap

// Utility functions in support of bootstrap testing

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/ava-labs/avalanchego/config"
	"github.com/ava-labs/avalanchego/tests/fixture/tmpnet"
)

var errContextRequired = errors.New("unable to wait for health node with a context without a deadline")

func WaitForNodeHealth(ctx context.Context, uri string) error {
	if _, ok := ctx.Deadline(); !ok {
		return errContextRequired
	}
	ticker := time.NewTicker(tmpnet.DefaultNodeTickerInterval)
	defer ticker.Stop()

	for {
		healthy, err := tmpnet.CheckNodeHealth(ctx, uri)
		if err != nil {
			return fmt.Errorf("failed to wait for node health: %w", err)
		}
		if healthy {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("failed to wait for node health before timeout: %w", ctx.Err())
		case <-ticker.C:
		}
	}
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
