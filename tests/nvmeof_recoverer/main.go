package main

import (
	"context"
	"fmt"
	"log"

	"github.com/rook/rook/pkg/operator/k8sutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	config, err := clientcmd.BuildConfigFromFlags("", "/root/.kube/config")
	if err != nil {
		log.Fatal(err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	// Get the attached hostname for the OSD
	opts := metav1.ListOptions{
		FieldSelector: "metadata.name=rook-ceph-osd-99-df4984bcf-5c2sh",
	}
	pods, err := clientset.CoreV1().Pods("rook-ceph").List(context.TODO(), opts)
	if err != nil || len(pods.Items) == 0 {
		log.Fatalf("Pod 조회 실패: %v", err)
	}
	attachedNode := pods.Items[0].Spec.NodeName

	// Find the device for relocation to a new node
	deviceName := ""
	for _, envVar := range pods.Items[0].Spec.Containers[0].Env {
		if envVar.Name == "ROOK_BLOCK_PATH" {
			deviceName = envVar.Value
			break
		}
	}
	fmt.Printf("attachedNode: %s, deviceName: %s\n", attachedNode, deviceName)

	// Delete the OSD deployment that is in CrashLoopBackOff
	deploymentName := "rook-ceph-osd-" + pods.Items[0].Labels["ceph-osd-id"]
	err = k8sutil.DeleteDeployment(
		context.TODO(),
		clientset,
		"rook-ceph",
		deploymentName,
	)
	if err != nil {
		panic(err)
	}
}
