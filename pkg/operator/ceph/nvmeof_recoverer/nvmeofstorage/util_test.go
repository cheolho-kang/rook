package nvmeofstorage

import (
	"testing"

	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	"github.com/stretchr/testify/require"
)

func TestRelocateOSD(t *testing.T) {
	fm := NewFabricMap()
	nvmeofstorage := &cephv1.NvmeOfStorage{
		Spec: cephv1.NvmeOfStorageSpec{
			Devices: []cephv1.FabricDevice{
				{
					SubNQN:       "com.example:subnqn0",
					OsdID:        "0",
					AttachedNode: "node1",
				},
				{
					SubNQN:       "com.example:subnqn1",
					OsdID:        "1",
					AttachedNode: "node2",
				},
				{
					SubNQN:       "com.example:subnqn2",
					OsdID:        "2",
					AttachedNode: "node2",
				},
				{
					SubNQN:       "com.example:subnqn3",
					OsdID:        "3",
					AttachedNode: "node3",
				},
			},
		},
	}

	t.Run("TestGetNextAttachableHost", func(t *testing.T) {
		fm.AddOSD(nvmeofstorage.Spec.Devices[0].OsdID, nvmeofstorage)
		fm.AddOSD(nvmeofstorage.Spec.Devices[1].OsdID, nvmeofstorage)
		fm.AddOSD(nvmeofstorage.Spec.Devices[2].OsdID, nvmeofstorage)
		fm.AddOSD(nvmeofstorage.Spec.Devices[3].OsdID, nvmeofstorage)

		expectedOSDID := nvmeofstorage.Spec.Devices[0].OsdID
		actualDescriptor, _ := fm.FindDescriptorBySubNQN(nvmeofstorage.Spec.Devices[0].SubNQN)
		require.Equal(t, expectedOSDID, actualDescriptor.ID)

		expectedOSDID = nvmeofstorage.Spec.Devices[1].OsdID
		actualDescriptor, _ = fm.FindDescriptorBySubNQN(nvmeofstorage.Spec.Devices[1].SubNQN)
		require.Equal(t, expectedOSDID, actualDescriptor.ID)

		expectedOSDID = nvmeofstorage.Spec.Devices[2].OsdID
		actualDescriptor, _ = fm.FindDescriptorBySubNQN(nvmeofstorage.Spec.Devices[2].SubNQN)
		require.Equal(t, expectedOSDID, actualDescriptor.ID)

		expectedOSDID = nvmeofstorage.Spec.Devices[3].OsdID
		actualDescriptor, _ = fm.FindDescriptorBySubNQN(nvmeofstorage.Spec.Devices[3].SubNQN)
		require.Equal(t, expectedOSDID, actualDescriptor.ID)
	})

	t.Run("TestGetNextAttachableHostErrorHandling", func(t *testing.T) {
		faultDeviceInfo := nvmeofstorage.Spec.Devices[0]
		// "node3" will be selected as the next node because it has the fewest number of fabric device attached"
		expectedNextNode := "node3"
		actualNextNode := fm.GetNextAttachableHost(faultDeviceInfo)
		require.Equal(t, expectedNextNode, actualNextNode)

		// "node2" will be seletect because "node1" has been removed from the fabric map
		faultDeviceInfo = nvmeofstorage.Spec.Devices[3]
		expectedNextNode = "node2"
		actualNextNode = fm.GetNextAttachableHost(faultDeviceInfo)
		require.Equal(t, expectedNextNode, actualNextNode)

		// any node can be selected because "node3" has also been removed from the fabric map, leaving nothing to attach.
		faultDeviceInfo = nvmeofstorage.Spec.Devices[1]
		expectedNextNode = ""
		actualNextNode = fm.GetNextAttachableHost(faultDeviceInfo)
		require.Equal(t, expectedNextNode, actualNextNode)

		// any node will be selected
		faultDeviceInfo = nvmeofstorage.Spec.Devices[2]
		expectedNextNode = ""
		actualNextNode = fm.GetNextAttachableHost(faultDeviceInfo)
		require.Equal(t, expectedNextNode, actualNextNode)
	})
}
