// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/regclient/regclient"

	"github.com/ava-labs/avalanchego/config"
	"github.com/ava-labs/avalanchego/tests/fixture/tmpnet"
)

func main() {
	startNode()
}

func startNode() {
	kubeconfigPath := os.Getenv("KUBECONFIG")
	kubeConfig, _ := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	clientset, _ := kubernetes.NewForConfig(kubeConfig)

	// Create a PVC
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "bootstrap-pvc-",
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("128Mi"),
				},
			},
		},
	}

	createdPVC, err := clientset.CoreV1().PersistentVolumeClaims("default").Create(context.TODO(), pvc, metav1.CreateOptions{})
	if err != nil {
		log.Fatalf("Error creating PVC: %v", err)

	} else {
		log.Println("Successfully created PVC")
	}

	dataDir := "/data"

	flags := map[string]string{
		config.NetworkNameKey:                   "fuji",
		config.DataDirKey:                       dataDir,
		config.StakingEphemeralCertEnabledKey:   "true",
		config.StakingEphemeralSignerEnabledKey: "true",
		// TODO: Externalize this configuration to enable testing
		// TODO Generate keys to use for consistency across tests e.g.

		// configuration1: {keys, network name, sync configuration}
		// ...
		// configurationN: {keys, network name, sync configuration}
	}

	env := make([]corev1.EnvVar, len(flags))
	var i int
	for k, v := range flags {
		env[i] = corev1.EnvVar{
			Name:  config.KeyToEnvVar(k),
			Value: v,
		}
		i++
	}

	// Create a pod that uses the PVC
	// TODO Configure the pod to connect to an existing network
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "bootstrap-test-",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "mycontainer",
					// TODO(marun) Support local images
					Image: "avaplatform/avalanchego:latest",
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "mypvc",
							MountPath: dataDir,
						},
					},
					Env: env,
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "mypvc",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: createdPVC.Name,
						},
					},
				},
			},
		},
	}

	_, err = clientset.CoreV1().Pods("default").Create(context.TODO(), pod, metav1.CreateOptions{})
	if err != nil {
		log.Fatalf("Error creating pod: %v", err)
	} else {
		log.Println("Successfully created pod")
	}

	// Wait for the IP of the pod
	watch, err := clientset.CoreV1().Pods("default").Watch(context.TODO(), metav1.SingleObject(metav1.ObjectMeta{Name: "mypod"}))
	if err != nil {
		log.Fatalf("Error watching pod: %v", err)
	}

	var podIP string
	for event := range watch.ResultChan() {
		pod, ok := event.Object.(*corev1.Pod)
		if !ok {
			log.Fatalf("Unexpected type: %T", event.Object)

		}

		if pod.Status.PodIP != "" {
			podIP = pod.Status.PodIP
			watch.Stop()
		}
	}

	uri := fmt.Sprintf("http://%s:%d", podIP, config.DefaultHTTPPort)
	// Check the health of the node at podIP:9550

	// TODO(marun) How long to wait?
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := waitForNodeHealth(ctx, uri); err != nil {
		log.Fatalf("Error waiting for node to be healthy: %v", err)
	}
}
