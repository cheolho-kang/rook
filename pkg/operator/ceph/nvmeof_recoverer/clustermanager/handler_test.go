package clustermanager

// func TestReassignOSD(t *testing.T) {
// 	ctx := &clusterd.Context{}
// 	opManagerContext := context.TODO()
// 	cm := New(ctx, opManagerContext)

// 	t.Run("TestGetNextAttachableHost", func(t *testing.T) {
// 		nvmeofstorage := &cephv1.NvmeOfStorage{
// 			Spec: cephv1.NvmeOfStorageSpec{
// 				Devices: []cephv1.FabricDevice{
// 					{
// 						OsdID:        "0",
// 						AttachedNode: "node1",
// 					},
// 					{
// 						OsdID:        "1",
// 						AttachedNode: "node2",
// 					},
// 					{
// 						OsdID:        "2",
// 						AttachedNode: "node2",
// 					},
// 					{
// 						OsdID:        "3",
// 						AttachedNode: "node3",
// 					},
// 				},
// 			},
// 		}
// 		cm.AddOSD(nvmeofstorage.Spec.Devices[0].OsdID, nvmeofstorage)
// 		cm.AddOSD(nvmeofstorage.Spec.Devices[1].OsdID, nvmeofstorage)
// 		cm.AddOSD(nvmeofstorage.Spec.Devices[2].OsdID, nvmeofstorage)
// 		cm.AddOSD(nvmeofstorage.Spec.Devices[3].OsdID, nvmeofstorage)

// 		expectedOSDIDs := []string{"0"}
// 		actualOSDs, _ := cm.fabricMap.FindOSDsByNode("node1")
// 		CheckOSDIDs(t, expectedOSDIDs, actualOSDs)

// 		expectedOSDIDs = []string{"1", "2"}
// 		actualOSDs, _ = cm.fabricMap.FindOSDsByNode("node2")
// 		CheckOSDIDs(t, expectedOSDIDs, actualOSDs)

// 		expectedOSDIDs = []string{"3"}
// 		actualOSDs, _ = cm.fabricMap.FindOSDsByNode("node3")
// 		CheckOSDIDs(t, expectedOSDIDs, actualOSDs)
// 	})

// 	t.Run("TestGetNextAttachableHostErrorHandling", func(t *testing.T) {
// 		osdID := "invalidValue"
// 		actualNextNode, err := cm.GetNextAttachableHost(osdID)
// 		expectedNextNode := ""
// 		require.Error(t, err)
// 		require.Equal(t, expectedNextNode, actualNextNode)

// 		osdID = "0"
// 		actualNextNode, err = cm.GetNextAttachableHost(osdID)
// 		expectedNextNode = "node3"
// 		require.Nil(t, err)
// 		require.Equal(t, expectedNextNode, actualNextNode)

// 		osdID = "3"
// 		actualNextNode, err = cm.GetNextAttachableHost(osdID)
// 		expectedNextNode = "node2"
// 		require.Nil(t, err)
// 		require.Equal(t, expectedNextNode, actualNextNode)

// 		osdID = "1"
// 		actualNextNode, err = cm.GetNextAttachableHost(osdID)
// 		expectedNextNode = ""
// 		require.Nil(t, err)
// 		require.Equal(t, expectedNextNode, actualNextNode)

// 		osdID = "2"
// 		actualNextNode, err = cm.GetNextAttachableHost(osdID)
// 		expectedNextNode = ""
// 		require.Nil(t, err)
// 		require.Equal(t, expectedNextNode, actualNextNode)
// 	})
// }
// func CheckOSDIDs(t *testing.T, expectedOSDIDs []string, actualOSDs []FabricDeviceInfo) {
// 	acutalOSDIDs := []string{}
// 	for _, osd := range actualOSDs {
// 		acutalOSDIDs = append(acutalOSDIDs, osd.OsdID)
// 	}
// 	require.Equal(t, expectedOSDIDs, acutalOSDIDs)
// }

// func TestRunNVMeOfJob_Success(t *testing.T) {
// 	// Setup the fake Kubernetes client
// 	clientset := fake.NewSimpleClientset()
// 	namespace := "test-namespace"
// 	jobName := "nvmeof-conn-control-job"

// 	// Setup the ClusterManager context
// 	cm := &ClusterManager{
// 		context:          &clusterd.Context{Clientset: clientset},
// 		opManagerContext: context.TODO(),
// 	}

// 	// Mock Get again to simulate job completion (succeeded)
// 	clientset.Fake.PrependReactor("get", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
// 		return true, &batchv1.Job{
// 			ObjectMeta: metav1.ObjectMeta{
// 				Name:      "nvmeof-job",
// 				Namespace: namespace,
// 			},
// 			Status: batchv1.JobStatus{
// 				Succeeded: 1, // Simulate a successful job
// 			},
// 		}, nil
// 	})

// 	// Mock Create for job to simulate successful job creation
// 	clientset.Fake.PrependReactor("create", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
// 		return true, nil, nil
// 	})

// 	// Mock List for pods to simulate pod discovery
// 	clientset.Fake.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
// 		// Return a non-empty PodList with one pod
// 		return true, &v1.PodList{
// 			Items: []v1.Pod{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name:      "nvmeof-pod",
// 						Namespace: namespace,
// 						Labels:    map[string]string{"job-name": jobName},
// 					},
// 				},
// 			},
// 		}, nil
// 	})

// 	output, err := cm.runNvmeoFJob("check", namespace, "test-host", "test-address", "test-port", "test-subnqn")
// 	// Ensure the function executed successfully
// 	assert.NoError(t, err)
// 	assert.Contains(t, output, "Successfully connected")
// }
