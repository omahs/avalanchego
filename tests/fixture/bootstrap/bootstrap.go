// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/ava-labs/avalanchego/config"
	"github.com/ava-labs/avalanchego/version"
)

const (
	ContainerName = "avalanchego"

	mountPath = "/data"
)

type NodePod struct {
	Namespace string
	Name      string
	PVCName   string
}

func StartNodePod(
	ctx context.Context,
	clientset *kubernetes.Clientset,
	namespace string,
	imageName string,
	pvcSize string,
	annotations map[string]string,
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

	// Ensure the pvc mount path matches the data dir for the node
	flags[config.DataDirKey] = mountPath

	// TODO(marun) Ensure images aren't pulled for testing
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "avalanche-node-",
			Annotations:  annotations,
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

	return &NodePod{
		Namespace: namespace,
		Name:      createdPod.Name,
		PVCName:   createdPVC.Name,
	}, nil
}

type ImageDetails struct {
	ImageID  string
	Versions version.Versions
}

func GetImageVersions(
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
