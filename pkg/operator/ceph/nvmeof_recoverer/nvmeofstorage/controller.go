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
	"time"

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
	"k8s.io/apimachinery/pkg/util/wait"
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

// INITIALIZATION -> ACTIVATED
type ControllerState int

const (
	INITIALIZATION = iota
	ACTIVATED
)

const (
	CR_UPDATED = iota
	OSD_STATE_CHANGED
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
	nvmeOfStorages   map[string]*cephv1.NvmeOfStorage
	domainStateMap   map[string]ControllerState
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
		nvmeOfStorages:   make(map[string]*cephv1.NvmeOfStorage),
		clustermanager:   cm.New(context, opManagerContext),
		domainStateMap:   make(map[string]ControllerState),
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
					// Prevents redundant Reconciler triggers during the cleanup of a faulty OSD pod by the nvmeofstorage controller.
					if newPod.DeletionTimestamp != nil {
						return false
					}
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

func (r *ReconcileNvmeOfStorage) getSystemEvent(e string) ControllerState {
	if strings.Contains(e, "nvmeofstorage") {
		return CR_UPDATED
	} else if strings.Contains(e, "rook-ceph-osd") {
		return OSD_STATE_CHANGED
	}
	panic("wrong event type")
}

func (r *ReconcileNvmeOfStorage) initFabricMap(context context.Context, request reconcile.Request) error {
	// Fetch the NvmeOfStorage CRD object
	domainName := request.Name
	r.nvmeOfStorages[domainName] = &cephv1.NvmeOfStorage{}
	err := r.client.Get(r.opManagerContext, request.NamespacedName, r.nvmeOfStorages[domainName])
	if err != nil {
		logger.Errorf("unable to fetch NvmeOfStorage, err: %v", err)
		return err
	}

	r.reconstructCRUSHMap(context, request.Namespace, domainName)

	// Update the NvmeOfStorage CR to reflect the OSD ID
	err = r.client.Update(context, r.nvmeOfStorages[domainName])
	if err != nil {
		panic(fmt.Sprintf("Failed to update NVMeOfStorage: %v, Namespace: %s, Name: %s", err, request.Namespace, request.Name))
	}
	return err
}

func (r *ReconcileNvmeOfStorage) tryRelocateDevice(request reconcile.Request) error {
	// Get the fabric device info details for the given request
	domainName := request.Name
	osdID := strings.Split(strings.Split(request.Name, osd.AppName+"-")[1], "-")[0]
	deviceInfo := r.findTargetNvmeOfStorageCR(osdID, domainName)

	// Cleanup the OSD that is in CrashLoopBackOff
	r.cleanupOSD(request.Namespace, deviceInfo)

	// Connect the device to the new attachable host
	r.reassignFaultedOSDDevice(request.Namespace, domainName, deviceInfo)

	// Request the OSD to be transferred to the next host
	err := r.updateCephClusterCR(request, deviceInfo.ClusterName)
	if err != nil {
		logger.Errorf("unable to update CephCluster CR, err: %v", err)
		return err
	}
	return err
}

func (r *ReconcileNvmeOfStorage) Reconcile(context context.Context, request reconcile.Request) (reconcile.Result, error) {
	logger.Debugf("reconciling NvmeOfStorage. Request.Namespace: %s, Request.Name: %s", request.Namespace, request.Name)

	event := r.getSystemEvent(request.Name)
	_, exists := r.domainStateMap[request.Name]
	if !exists {
		r.domainStateMap[request.Name] = INITIALIZATION
	}

	var err error
	if event == CR_UPDATED {
		if r.domainStateMap[request.Name] != INITIALIZATION {
			panic("impossible")
		}
		err = r.initFabricMap(context, request)
		r.domainStateMap[request.Name] = ACTIVATED
	} else if event == OSD_STATE_CHANGED {
		if r.domainStateMap[request.Name] == INITIALIZATION {
			panic("impossible")
		}
		err = r.tryRelocateDevice(request)
		r.domainStateMap[request.Name] = ACTIVATED
	} else {
		return reconcile.Result{}, nil
	}

	return reporting.ReportReconcileResult(logger, r.recorder, request, r.nvmeOfStorages[request.Name], reconcile.Result{}, err)
}

func (r *ReconcileNvmeOfStorage) reconstructCRUSHMap(context context.Context, namespace, domainName string) {
	// Wait for all OSDs to be up
	// If trying to update the CRUSH Map before all OSDs are up, the CRUSH Map updates might not apply correctly.
	r.waitForOSDsUp(context, namespace, domainName, 1*time.Minute)

	// Retrieve the nvmeofstorage CR to update the CRUSH map
	for i := range r.nvmeOfStorages[domainName].Spec.Devices {
		device := &r.nvmeOfStorages[domainName].Spec.Devices[i]
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

					logger.Debugf("[OSD.%s] status: %s, deviceName: %s, attachedNode: %s", device.OsdID, pod.Status.Phase, device.DeviceName, device.AttachedNode)
					// Update CRUSH map for OSD relocation to fabric failure domain
					fabricHost := FabricFailureDomainPrefix + "-" + r.nvmeOfStorages[domainName].Spec.Name
					clusterInfo := cephclient.AdminClusterInfo(context, namespace, clusterName)
					cmd := []string{"osd", "crush", "move", "osd." + device.OsdID, "root=default", "host=" + fabricHost}
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

					// Update the OSD deployment depending on the nvmeofstorage CR
					r.clustermanager.AddOSD(device.OsdID, domainName, r.nvmeOfStorages)
				}
			}
		}
	}
}

func (r *ReconcileNvmeOfStorage) findTargetNvmeOfStorageCR(osdID, domainName string) cephv1.FabricDevice {
	for _, device := range r.nvmeOfStorages[domainName].Spec.Devices {
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

func (r *ReconcileNvmeOfStorage) reassignFaultedOSDDevice(namespace, domainName string, deviceInfo cephv1.FabricDevice) {
	nextHostName, err := r.clustermanager.GetNextAttachableHost(deviceInfo.OsdID, domainName)
	if err != nil {
		panic(fmt.Sprintf("Wrong Info"))
	}
	if nextHostName == "" {
		// Return an empty struct when there is no attachable host, which means this OSD will be removed and rebalanced by Ceph
		return
	}

	// Connect the device to the new host
	output, err := r.clustermanager.ConnectOSDDeviceToHost(namespace, nextHostName, deviceInfo)
	if err != nil {
		panic(fmt.Sprintf("failed to connect device with SubNQN %s to host %s: %v",
			deviceInfo.SubNQN, nextHostName, err))
	}

	// Update the attached node for reassigning the device
	r.clustermanager.AddOSD(output.OsdID, domainName, r.nvmeOfStorages)

	// TODO (cheolho.kang): these lines should be moved to initialization phase. Other updatable data (e.g., device name, attached node) should be separated from CR and managed via k8s (e.g., etcd, configmap) (PBDEV-1748)
	// Update the NvmeOfStorage CR
	for i := range r.nvmeOfStorages[domainName].Spec.Devices {
		device := &r.nvmeOfStorages[domainName].Spec.Devices[i]
		if device.OsdID == deviceInfo.OsdID {
			if output.AttachedNode == "" {
				// it means no nodes are available for reassignment
				// In this case, the device will be removed from the nvmeOfStorage CR
				r.nvmeOfStorages[domainName].Spec.Devices = append(r.nvmeOfStorages[domainName].Spec.Devices[:i], r.nvmeOfStorages[domainName].Spec.Devices[i+1:]...)
				logger.Debug("OSD.%s will not be reassigned to any node", device.OsdID)
			} else {
				device.AttachedNode = output.AttachedNode
				device.DeviceName = output.DeviceName
			}
			break
		}
	}
	err = r.client.Update(r.opManagerContext, r.nvmeOfStorages[domainName])
	if err != nil {
		panic(fmt.Sprintf("Failed to update NVMeOfStorage: %s, error: %+v", r.nvmeOfStorages[domainName].Name, err))
	}

	logger.Debugf("successfully reassigned the device for OSD.%s. host: [%s --> %s], device: [%s --> %s], SubNQN: %s",
		output.OsdID, deviceInfo.AttachedNode, output.AttachedNode, deviceInfo.DeviceName, output.DeviceName, output.SubNQN)

	return
}

func (r *ReconcileNvmeOfStorage) updateCephClusterCR(request reconcile.Request, clusterName string) error {
	cephCluster, err := r.context.RookClientset.CephV1().CephClusters(request.Namespace).Get(context.Background(), clusterName, metav1.GetOptions{})
	if err != nil {
		logger.Errorf("failed to get cluster CR. err: %v", err)
		return err
	}

	// nodeMap is a map of node name to a list of devices attached to the node
	nodeMap := make(map[string][]cephv1.Device)
	// Update the nodeMap with the current device information from the NvmeOfStorage CR
	for domainName, resource := range r.nvmeOfStorages {
		for _, device := range resource.Spec.Devices {
			dev := cephv1.Device{
				Name: device.DeviceName,
				Config: map[string]string{
					"failureDomain": FabricFailureDomainPrefix + "-" + domainName,
				},
			}
			nodeMap[device.AttachedNode] = append(nodeMap[device.AttachedNode], dev)
		}
	}

	// Commit the changes to the CephCluster CR
	var nodes []cephv1.Node
	for nodeName, devs := range nodeMap {
		nodes = append(nodes, cephv1.Node{
			Name: nodeName,
			Selection: cephv1.Selection{
				Devices: devs,
			},
		})
	}
	cephCluster.Spec.Storage.Nodes = nodes

	// Apply the updated CephCluster CR
	_, err = r.context.RookClientset.CephV1().CephClusters(request.Namespace).Update(context.TODO(), cephCluster, metav1.UpdateOptions{})
	if err != nil {
		panic(fmt.Sprintf("failed to update CephCluster CR: %v", err))
	}
	logger.Debugf("CephCluster updated successfully. Nodes: %+v", nodeMap)

	return nil
}

func (r *ReconcileNvmeOfStorage) waitForOSDsUp(ctx context.Context, namespace, domainName string, timeOut time.Duration) error {
	opts := metav1.ListOptions{
		LabelSelector: "app=" + osd.AppName,
	}
	runningOSDs := 0
	err := wait.PollUntilContextTimeout(ctx, 3*time.Second, timeOut, true, func(context context.Context) (done bool, err error) {
		var pods *corev1.PodList
		pods = r.getPods(context, namespace, opts)
		runningOSDs = 0
		for _, pod := range pods.Items {
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
					deviceName := getDeviceName(pod)
					for _, fabricDevice := range r.nvmeOfStorages[domainName].Spec.Devices {
						if pod.Spec.NodeName == fabricDevice.AttachedNode && deviceName == fabricDevice.DeviceName {
							runningOSDs++
						}
					}
				}
			}
		}
		if runningOSDs >= len(r.nvmeOfStorages[domainName].Spec.Devices) {
			logger.Debugf("All OSDs are up. (%d/%d)",
				runningOSDs, len(r.nvmeOfStorages[domainName].Spec.Devices))
			return true, nil
		}

		logger.Debugf("Ceph cluster is not fully up yet. Waiting for all OSDs to be up. (%d/%d)",
			runningOSDs, len(r.nvmeOfStorages[domainName].Spec.Devices))
		return false, nil
	})
	if err != nil {
		return errors.Errorf("timeout waiting for OSDs to be up. numFabricDevices: %d, runningOSDs: %d",
			len(r.nvmeOfStorages[domainName].Spec.Devices), runningOSDs)
	}

	return err
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
			return true
		}
	}

	return false
}

func getDeviceName(pod corev1.Pod) string {
	deviceName := ""
	for _, envVar := range pod.Spec.Containers[0].Env {
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
