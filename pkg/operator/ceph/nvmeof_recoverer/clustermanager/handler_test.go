package clustermanager

import (
	"context"
	"testing"

	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	"github.com/rook/rook/pkg/clusterd"
	"github.com/stretchr/testify/require"
)

func TestReassignOSD(t *testing.T) {
	ctx := &clusterd.Context{}
	opManagerContext := context.TODO()
	cm := New(ctx, opManagerContext)

	t.Run("TestGetNextAttachableHost", func(t *testing.T) {
		// Given: a FabricMap with 4 OSDs attached to 3 different nodes
		nvmeofstorages := map[string]*cephv1.NvmeOfStorage{
			"domain1": {
				Spec: cephv1.NvmeOfStorageSpec{
					Name: "domain1",
					Devices: []cephv1.FabricDevice{
						{
							OsdID:        "0",
							AttachedNode: "node1",
						},
						{
							OsdID:        "1",
							AttachedNode: "node2",
						},
						{
							OsdID:        "2",
							AttachedNode: "node2",
						},
						{
							OsdID:        "3",
							AttachedNode: "node3",
						},
					},
				},
			},
		}
		for domainName, nvmeofstorage := range nvmeofstorages {
			for _, device := range nvmeofstorage.Spec.Devices {
				cm.AddOSD(device.OsdID, domainName, nvmeofstorages)
			}
		}

		// Check if the OSDs are added correctly
		targetDomain := "domain1"
		expectedOSDIDs := []string{"0"}
		actualOSDs, _ := cm.fabricMap.FindOSDsByNode(targetDomain, "node1")
		CheckOSDIDs(t, expectedOSDIDs, actualOSDs)

		expectedOSDIDs = []string{"1", "2"}
		actualOSDs, _ = cm.fabricMap.FindOSDsByNode(targetDomain, "node2")
		CheckOSDIDs(t, expectedOSDIDs, actualOSDs)

		expectedOSDIDs = []string{"3"}
		actualOSDs, _ = cm.fabricMap.FindOSDsByNode(targetDomain, "node3")
		CheckOSDIDs(t, expectedOSDIDs, actualOSDs)
	})

	t.Run("TestGetNextAttachableHostErrorHandling", func(t *testing.T) {
		// Test with invalid osdID, it should return an error
		targetDomain := "domain1"
		osdID := "invalidValue"
		_, err := cm.GetNextAttachableHost(osdID, targetDomain)
		expectedNextNode := ""
		require.Error(t, err)

		// Test with correct osdID, it should return an attachable node
		osdID = "0"
		actualNextNode, err := cm.GetNextAttachableHost(osdID, targetDomain)
		expectedNextNode = "node3"
		require.Nil(t, err)
		require.Equal(t, expectedNextNode, actualNextNode)

		// Test with correct osdID, it should return an attachable node
		osdID = "3"
		actualNextNode, err = cm.GetNextAttachableHost(osdID, targetDomain)
		expectedNextNode = "node2"
		require.Nil(t, err)
		require.Equal(t, expectedNextNode, actualNextNode)

		// Test with correct osdID when there is no attachable node, it should return an empty string
		osdID = "1"
		actualNextNode, err = cm.GetNextAttachableHost(osdID, targetDomain)
		expectedNextNode = ""
		require.Nil(t, err)
		require.Equal(t, expectedNextNode, actualNextNode)

		// Test with correct osdID when there is no attachable node, it should return an empty string
		osdID = "2"
		actualNextNode, err = cm.GetNextAttachableHost(osdID, targetDomain)
		expectedNextNode = ""
		require.Nil(t, err)
		require.Equal(t, expectedNextNode, actualNextNode)
	})

	t.Run("TestGetNextAttachableHostWithMultiDomain", func(t *testing.T) {
		// Given: a FabricMap with 6 OSDs attached to 3 different nodes in 2 different domains
		nvmeofstorages := map[string]*cephv1.NvmeOfStorage{
			"domain1": {
				Spec: cephv1.NvmeOfStorageSpec{
					Name: "domain1",
					Devices: []cephv1.FabricDevice{
						{
							OsdID:        "0",
							AttachedNode: "node1",
						},
						{
							OsdID:        "1",
							AttachedNode: "node2",
						},
						{
							OsdID:        "2",
							AttachedNode: "node2",
						},
						{
							OsdID:        "3",
							AttachedNode: "node3",
						},
					},
				},
			},
			"domain2": {
				Spec: cephv1.NvmeOfStorageSpec{
					Name: "domain2",
					Devices: []cephv1.FabricDevice{
						{
							OsdID:        "4",
							AttachedNode: "node1",
						},
						{
							OsdID:        "5",
							AttachedNode: "node2",
						},
					},
				},
			},
		}
		for domainName, nvmeofstorage := range nvmeofstorages {
			for _, device := range nvmeofstorage.Spec.Devices {
				cm.AddOSD(device.OsdID, domainName, nvmeofstorages)
			}
		}

		// Check if the OSDs are added correctly
		targetDomain := "domain1"
		expectedOSDIDs := []string{"0"}
		actualOSDs, _ := cm.fabricMap.FindOSDsByNode(targetDomain, "node1")
		CheckOSDIDs(t, expectedOSDIDs, actualOSDs)

		expectedOSDIDs = []string{"1", "2"}
		actualOSDs, _ = cm.fabricMap.FindOSDsByNode(targetDomain, "node2")
		CheckOSDIDs(t, expectedOSDIDs, actualOSDs)

		expectedOSDIDs = []string{"3"}
		actualOSDs, _ = cm.fabricMap.FindOSDsByNode(targetDomain, "node3")
		CheckOSDIDs(t, expectedOSDIDs, actualOSDs)

		targetDomain = "domain2"
		expectedOSDIDs = []string{"4"}
		actualOSDs, _ = cm.fabricMap.FindOSDsByNode(targetDomain, "node1")
		CheckOSDIDs(t, expectedOSDIDs, actualOSDs)

		expectedOSDIDs = []string{"5"}
		actualOSDs, _ = cm.fabricMap.FindOSDsByNode(targetDomain, "node2")
		CheckOSDIDs(t, expectedOSDIDs, actualOSDs)

		// Test with correct osdID, it should return an attachable node
		targetDomain = "domain1"
		osdID := "0"
		actualNextNode, err := cm.GetNextAttachableHost(osdID, targetDomain)
		expectedNextNode := "node3"
		require.Nil(t, err)
		require.Equal(t, expectedNextNode, actualNextNode)

		// Test with wrong domain name, it should return an error
		targetDomain = "domain1"
		osdID = "4"
		_, err = cm.GetNextAttachableHost(osdID, targetDomain)
		require.Error(t, err)

		// Test with correct osdID, it should return an attachable node from domain2
		// if actualNextNode is "node1" that means FabricMap is not considering domain name
		targetDomain = "domain2"
		osdID = "4"
		actualNextNode, err = cm.GetNextAttachableHost(osdID, targetDomain)
		expectedNextNode = "node2"
		require.Nil(t, err)
		require.Equal(t, expectedNextNode, actualNextNode)

		// Test with correct osdID, it should return an attachable node
		targetDomain = "domain1"
		osdID = "1"
		actualNextNode, err = cm.GetNextAttachableHost(osdID, targetDomain)
		expectedNextNode = "node3"
		require.Nil(t, err)
		require.Equal(t, expectedNextNode, actualNextNode)

		// Test if this domain has no more attachable host, it should return an empty string
		targetDomain = "domain2"
		osdID = "5"
		actualNextNode, err = cm.GetNextAttachableHost(osdID, targetDomain)
		expectedNextNode = ""
		require.Nil(t, err)
		require.Equal(t, expectedNextNode, actualNextNode)

		// Test with correct osdID, it should return an attachable node
		targetDomain = "domain1"
		osdID = "2"
		actualNextNode, err = cm.GetNextAttachableHost(osdID, targetDomain)
		expectedNextNode = "node3"
		require.Nil(t, err)
		require.Equal(t, expectedNextNode, actualNextNode)

		// Test if this domain has no more attachable host, it should return an empty string
		targetDomain = "domain1"
		osdID = "3"
		actualNextNode, err = cm.GetNextAttachableHost(osdID, targetDomain)
		expectedNextNode = ""
		require.Nil(t, err)
		require.Equal(t, expectedNextNode, actualNextNode)
	})
}

func CheckOSDIDs(t *testing.T, expectedOSDIDs []string, actualOSDs []FabricOSDInfo) {
	acutalOSDIDs := []string{}
	for _, osd := range actualOSDs {
		acutalOSDIDs = append(acutalOSDIDs, osd.id)
	}
	require.Equal(t, expectedOSDIDs, acutalOSDIDs)
}
