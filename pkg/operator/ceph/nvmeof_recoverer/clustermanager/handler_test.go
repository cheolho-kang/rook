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

	t.Run("TestGetNextAttachableNode", func(t *testing.T) {
		nvmeofstorage := &cephv1.NvmeOfStorage{
			Spec: cephv1.NvmeOfStorageSpec{
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
		}
		cm.AddOSD(nvmeofstorage.Spec.Devices[0].OsdID, nvmeofstorage)
		cm.AddOSD(nvmeofstorage.Spec.Devices[1].OsdID, nvmeofstorage)
		cm.AddOSD(nvmeofstorage.Spec.Devices[2].OsdID, nvmeofstorage)
		cm.AddOSD(nvmeofstorage.Spec.Devices[3].OsdID, nvmeofstorage)

		expectedOSDIDs := []string{"0"}
		actualOSDs, _ := cm.fabricMap.FindOSDsByNode("node1")
		CheckOSDIDs(t, expectedOSDIDs, actualOSDs)

		expectedOSDIDs = []string{"1", "2"}
		actualOSDs, _ = cm.fabricMap.FindOSDsByNode("node2")
		CheckOSDIDs(t, expectedOSDIDs, actualOSDs)

		expectedOSDIDs = []string{"3"}
		actualOSDs, _ = cm.fabricMap.FindOSDsByNode("node3")
		CheckOSDIDs(t, expectedOSDIDs, actualOSDs)
	})

	t.Run("TestGetNextAttachableHostErrorHandling", func(t *testing.T) {
		osdID := "invalidValue"
		actualNextNode, err := cm.GetNextAttachableNode(osdID)
		expectedNextNode := ""
		require.Error(t, err)
		require.Equal(t, expectedNextNode, actualNextNode)

		osdID = "0"
		actualNextNode, err = cm.GetNextAttachableNode(osdID)
		expectedNextNode = "node3"
		require.Nil(t, err)
		require.Equal(t, expectedNextNode, actualNextNode)

		osdID = "3"
		actualNextNode, err = cm.GetNextAttachableNode(osdID)
		expectedNextNode = "node2"
		require.Nil(t, err)
		require.Equal(t, expectedNextNode, actualNextNode)

		osdID = "1"
		actualNextNode, err = cm.GetNextAttachableNode(osdID)
		expectedNextNode = ""
		require.Nil(t, err)
		require.Equal(t, expectedNextNode, actualNextNode)

		osdID = "2"
		actualNextNode, err = cm.GetNextAttachableNode(osdID)
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
