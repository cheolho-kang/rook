/*
Copyright 2024 The Rook Authors. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package nvmeofstorage to reconcile a NvmeOfStorage CR.
package nvmeofstorage

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"emperror.dev/errors"
	"github.com/coreos/pkg/capnslog"

	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	"github.com/rook/rook/pkg/clusterd"
	cephclient "github.com/rook/rook/pkg/daemon/ceph/client"
	"github.com/rook/rook/pkg/operator/ceph/cluster/osd"
	opcontroller "github.com/rook/rook/pkg/operator/ceph/controller"
	cm "github.com/rook/rook/pkg/operator/ceph/nvmeof_recoverer/clustermanager"
	"github.com/rook/rook/pkg/operator/ceph/reporting"
	"github.com/rook/rook/pkg/operator/k8sutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName = "nvmeofstorage-controller"
	// FabricFailureDomainPrefix is the prefix for the fabric failure domain name
	FabricFailureDomainPrefix = "fabric-host"
)

var logger = capnslog.NewPackageLogger("github.com/rook/rook", controllerName)

var nvmeOfStorageKind = reflect.TypeOf(cephv1.NvmeOfStorage{}).Name()

// Sets the type meta for the controller main object
var controllerTypeMeta = metav1.TypeMeta{
	Kind:       nvmeOfStorageKind,
	APIVersion: fmt.Sprintf("%s/%s", cephv1.CustomResourceGroup, cephv1.Version),
}

var _ reconcile.Reconciler = &ReconcileNvmeOfStorage{}

// ReconcileNvmeOfStorage reconciles a NvmeOfStorage object
type ReconcileNvmeOfStorage struct {
	client           client.Client
	scheme           *runtime.Scheme
	context          *clusterd.Context
	opManagerContext context.Context
	recorder         record.EventRecorder
	clustermanager   *cm.ClusterManager
	nvmeOfStorage    *cephv1.NvmeOfStorage
}

// Add creates a new NvmeOfStorage Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, context *clusterd.Context, opManagerContext context.Context, opConfig opcontroller.OperatorConfig) error {
	return add(mgr, newReconciler(mgr, context, opManagerContext))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, context *clusterd.Context, opManagerContext context.Context) reconcile.Reconciler {
	return &ReconcileNvmeOfStorage{
		client:           mgr.GetClient(),
		context:          context,
		scheme:           mgr.GetScheme(),
		opManagerContext: opManagerContext,
		recorder:         mgr.GetEventRecorderFor("rook-" + controllerName),
		nvmeOfStorage:    &cephv1.NvmeOfStorage{},
		clustermanager:   cm.New(context, opManagerContext),
	}
}

func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: r})
	if err != nil {
		return errors.Wrapf(err, "failed to create %s controller", controllerName)
	}
	logger.Info("successfully started")

	// Watch for changes on the NvmeOfStorage CRD object
	cmKind := source.Kind(
		mgr.GetCache(),
		&cephv1.NvmeOfStorage{TypeMeta: controllerTypeMeta})
	err = c.Watch(cmKind, &handler.EnqueueRequestForObject{}, opcontroller.WatchControllerPredicate())
	if err != nil {
		return err
	}

	// Watch for changes on the OSD Pod object
	podKind := source.Kind(
		mgr.GetCache(),
		&corev1.Pod{})
	err = c.Watch(podKind, &handler.EnqueueRequestForObject{},
		predicate.Funcs{
			UpdateFunc: func(event event.UpdateEvent) bool {
				oldPod, okOld := event.ObjectOld.(*corev1.Pod)
				newPod, okNew := event.ObjectNew.(*corev1.Pod)
				if !okOld || !okNew {
					return false
				}
				if isOSDPod(newPod.Labels) && isPodDead(oldPod, newPod) {
					namespacedName := fmt.Sprintf("%s/%s", newPod.Namespace, newPod.Name)
					logger.Debugf("update event on Pod %q", namespacedName)
					return true
				}
				return false
			},
			CreateFunc: func(e event.CreateEvent) bool {
				return false
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return false
			},
			GenericFunc: func(e event.GenericEvent) bool {
				return false
			},
		})
	if err != nil {
		return errors.Wrap(err, "failed to watch for changes on the Pod object")
	}

	return nil
}

func (r *ReconcileNvmeOfStorage) Reconcile(context context.Context, request reconcile.Request) (reconcile.Result, error) {
	logger.Debugf("reconciling NvmeOfStorage. Request.Namespace: %s, Request.Name: %s", request.Namespace, request.Name)

	if strings.Contains(request.Name, "nvmeofstorage") {
		// Fetch the NvmeOfStorage CRD object
		err := r.client.Get(r.opManagerContext, request.NamespacedName, r.nvmeOfStorage)
		if err != nil {
			logger.Errorf("unable to fetch NvmeOfStorage, err: %v", err)
			return reconcile.Result{}, err
		}

		r.reconstructCRUSHMap(context, request.Namespace)

		// Update the NvmeOfStorage CR to reflect the OSD ID
		err = r.client.Update(context, r.nvmeOfStorage)
		if err != nil {
			panic(fmt.Sprintf("Failed to update NVMeOfStorage: %v, Namespace: %s, Name: %s", err, request.Namespace, request.Name))
		}

		return reporting.ReportReconcileResult(logger, r.recorder, request, r.nvmeOfStorage, reconcile.Result{}, err)
		// TODO (cheolho.kang): Refactor this method to use a more reliable way of identifying the events,
		// such as checking labels or annotations, instead of string parsing.
	} else if strings.Contains(request.Name, "rook-ceph-osd") {
		// Get the fabric device info details for the given request
		osdID := strings.Split(strings.Split(request.Name, osd.AppName+"-")[1], "-")[0]
		deviceInfo := r.findTargetNvmeOfStorageCR(osdID)

		// Cleanup the OSD that is in CrashLoopBackOff
		r.cleanupOSD(request.Namespace, deviceInfo)

		// Connect the device to the new attachable host
		newDeviceInfo := r.reassignFaultedOSDDevice(request.Namespace, deviceInfo)

		// Request the OSD to be transferred to the next host
		err := r.updateCephClusterCR(request, deviceInfo, newDeviceInfo)
		if err != nil {
			logger.Errorf("unable to update CephCluster CR, err: %v", err)
			return reconcile.Result{}, err
		}

		return reporting.ReportReconcileResult(logger, r.recorder, request, r.nvmeOfStorage, reconcile.Result{}, nil)
	}

	return reconcile.Result{}, nil
}

func (r *ReconcileNvmeOfStorage) reconstructCRUSHMap(context context.Context, namespace string) {
	// Retrieve the nvmeofstorage CR to update the CRUSH map
	for i := range r.nvmeOfStorage.Spec.Devices {
		device := &r.nvmeOfStorage.Spec.Devices[i]
		// Get OSD pods with label "app=rook-ceph-osd"
		var clusterName string
		opts := metav1.ListOptions{
			LabelSelector: "app=" + osd.AppName,
		}
		pods := r.getPods(context, namespace, opts)
		// Find the osd id and cluster name for the fabric device listed in the nvmeofstorage CR
		for _, pod := range pods.Items {
			for _, envVar := range pod.Spec.Containers[0].Env {
				if pod.Spec.NodeName == device.AttachedNode && envVar.Name == "ROOK_BLOCK_PATH" && envVar.Value == device.DeviceName {
					device.OsdID = pod.Labels["ceph-osd-id"]
					clusterName = pod.Labels["app.kubernetes.io/part-of"]

					// Update CRUSH map for OSD relocation to fabric failure domain
					fabricHost := FabricFailureDomainPrefix + "-" + r.nvmeOfStorage.Spec.Name
					clusterInfo := cephclient.AdminClusterInfo(context, namespace, clusterName)
					cmd := []string{"osd", "crush", "move", "osd." + device.OsdID, "host=" + fabricHost}
					exec := cephclient.NewCephCommand(r.context, clusterInfo, cmd)
					exec.JsonOutput = true
					buf, err := exec.Run()
					if err != nil {
						logger.Error(err, "Failed to move osd", "osdID", device.OsdID, "srcHost", device.AttachedNode,
							"destHost", fabricHost, "result", string(buf))
						panic(err)
					}
					logger.Debugf("Successfully updated CRUSH Map. osdID: %s, srcHost: %s, destHost: %s",
						device.OsdID, device.AttachedNode, fabricHost)

					// Update the AttachableHosts
					err = r.clustermanager.AddAttachbleHost(device.AttachedNode)
					if err != nil {
						panic(err)
					}
				}
			}
		}

		// Update device endpoint map
		r.clustermanager.UpdateDeviceEndpointeMap(r.nvmeOfStorage)
	}
}

func (r *ReconcileNvmeOfStorage) findTargetNvmeOfStorageCR(osdID string) cephv1.FabricDevice {
	for _, device := range r.nvmeOfStorage.Spec.Devices {
		if device.OsdID == osdID {
			return device
		}
	}
	panic("no attached node found")
}

func (r *ReconcileNvmeOfStorage) getPods(context context.Context, namespace string, opts metav1.ListOptions) *corev1.PodList {
	pods, err := r.context.Clientset.CoreV1().Pods(namespace).List(context, opts)
	if err != nil || len(pods.Items) == 0 {
		panic(err)
	}
	return pods
}

func (r *ReconcileNvmeOfStorage) cleanupOSD(namespace string, deviceInfo cephv1.FabricDevice) {
	// Delete the OSD deployment that is in CrashLoopBackOff
	err := k8sutil.DeleteDeployment(
		r.opManagerContext,
		r.context.Clientset,
		namespace,
		osd.AppName+"-"+deviceInfo.OsdID,
	)
	if err != nil {
		panic(fmt.Sprintf("failed to delete OSD deployment %q in namespace %q: %v",
			osd.AppName+"-"+deviceInfo.OsdID, namespace, err))
	}

	// Disconnect the device used by this OSD
	_, err = r.clustermanager.DisconnectOSDDevice(namespace, deviceInfo)
	if err != nil {
		panic(fmt.Sprintf("failed to disconnect OSD device with SubNQN %s: %v", deviceInfo.SubNQN, err))
	}
	logger.Debugf("successfully deleted the OSD deployment. Name: %q", osd.AppName+"-"+deviceInfo.OsdID)
}

func (r *ReconcileNvmeOfStorage) reassignFaultedOSDDevice(namespace string, deviceInfo cephv1.FabricDevice) cephv1.FabricDevice {
	nextHostName := r.clustermanager.GetNextAttachableHost(deviceInfo.AttachedNode)
	if nextHostName == "" {
		panic(fmt.Sprintf("no attachable hosts found for device with SubNQN %s on node %s",
			deviceInfo.SubNQN, deviceInfo.AttachedNode))
	}

	// Connect the device to the new host
	output, err := r.clustermanager.ConnectOSDDeviceToHost(namespace, nextHostName, deviceInfo)
	if err != nil {
		panic(fmt.Sprintf("failed to connect device with SubNQN %s to host %s: %v",
			deviceInfo.SubNQN, nextHostName, err))
	}
	logger.Debugf("successfully reassigned the device. SubNQN: %s, DeviceName: %s, AttachedNode: %s",
		deviceInfo.SubNQN, deviceInfo.DeviceName, output.AttachedNode)

	return output
}

func (r *ReconcileNvmeOfStorage) updateCephClusterCR(request reconcile.Request, oldDeviceInfo, newDeviceInfo cephv1.FabricDevice) error {
	cephCluster, err := r.context.RookClientset.CephV1().CephClusters(request.Namespace).Get(context.Background(), newDeviceInfo.ClusterName, metav1.GetOptions{})
	if err != nil {
		logger.Errorf("failed to get cluster CR. err: %v", err)
		return err
	}

	// Update the devices for the CephCluster CR
	for i, node := range cephCluster.Spec.Storage.Nodes {
		if node.Name == oldDeviceInfo.AttachedNode {
			// Remove the device from the old node
			var filteredDevices []cephv1.Device
			for _, device := range node.Devices {
				if device.Name != oldDeviceInfo.DeviceName {
					filteredDevices = append(filteredDevices, device)
				}
			}
			cephCluster.Spec.Storage.Nodes[i].Devices = filteredDevices
		} else if node.Name == newDeviceInfo.AttachedNode {
			// Add the new device to the new node
			fabricHost := FabricFailureDomainPrefix + "-" + r.nvmeOfStorage.Spec.Name
			newDevice := cephv1.Device{
				Name: newDeviceInfo.DeviceName,
				Config: map[string]string{
					"failureDomain": fabricHost,
				},
			}
			// Check for existing device with the same name
			for _, device := range node.Devices {
				if device.Name == newDeviceInfo.DeviceName {
					panic(fmt.Sprintf("device %s already exists in the new host", newDeviceInfo.DeviceName))
				}
			}
			cephCluster.Spec.Storage.Nodes[i].Devices = append(node.Devices, newDevice)
			break
		}
	}

	// Apply the updated CephCluster CR
	_, err = r.context.RookClientset.CephV1().CephClusters(request.Namespace).Update(context.TODO(), cephCluster, metav1.UpdateOptions{})
	if err != nil {
		panic(fmt.Sprintf("failed to update CephCluster CR: %v", err))
	}
	logger.Debugf("CephCluster updated successfully. oldNode: %s, oldDevicePath: %s, newNode: %s, newDevicePath: %s",
		oldDeviceInfo.AttachedNode, oldDeviceInfo.DeviceName, newDeviceInfo.AttachedNode, newDeviceInfo.DeviceName)

	return nil
}

func isOSDPod(labels map[string]string) bool {
	if labels["app"] == "rook-ceph-osd" && labels["ceph-osd-id"] != "" {
		return true
	}

	return false
}

func isPodDead(oldPod *corev1.Pod, newPod *corev1.Pod) bool {
	namespacedName := fmt.Sprintf("%s/%s", newPod.Namespace, newPod.Name)
	for _, cs := range newPod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
			logger.Infof("OSD Pod %q is in CrashLoopBackOff, oldPod.Status.Phase: %s", namespacedName, oldPod.Status.Phase)
			logger.Debug("!!label: ", newPod.Labels)
			return true
		}
	}

	return false
}

func getDeviceName(pods *corev1.PodList) string {
	deviceName := ""
	for _, envVar := range pods.Items[0].Spec.Containers[0].Env {
		if envVar.Name == "ROOK_BLOCK_PATH" {
			deviceName = envVar.Value
			break
		}
	}
	return deviceName
}

func getAttachedDeviceInfo(deviceList []cephv1.FabricDevice, attachedNode, deviceName string) cephv1.FabricDevice {
	for _, device := range deviceList {
		if device.AttachedNode == attachedNode && device.DeviceName == deviceName {
			return device
		}
	}
	panic("device not found")
}
