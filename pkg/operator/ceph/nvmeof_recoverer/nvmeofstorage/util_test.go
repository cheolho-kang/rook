package nvmeofstorage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRelocateOSD(t *testing.T) {
	fm := NewFabricMap()
	fds := []FabricDescriptor{
		{
			SubNQN:       "com.example:subnqn0",
			AttachedNode: "node1",
		},
		{
			SubNQN:       "com.example:subnqn1",
			AttachedNode: "node2",
		},
		{
			SubNQN:       "com.example:subnqn2",
			AttachedNode: "node2",
		},
		{
			SubNQN:       "com.example:subnqn3",
			AttachedNode: "node3",
		},
	}

	for _, fd := range fds {
		fm.AddDescriptor(fd)
	}

	t.Run("TestGetNextAttachableNodeErrorHandling", func(t *testing.T) {
		faultDeviceInfo := fds[0]
		// "node3" will be selected as the next node because it has the fewest number of fabric device attached"
		expectedNextNode := "node3"
		actualNextNode := fm.GetNextAttachableNode(faultDeviceInfo)
		require.Equal(t, expectedNextNode, actualNextNode)

		// "node2" will be seletect because "node1" has been removed from the fabric map
		faultDeviceInfo = fds[3]
		expectedNextNode = "node2"
		actualNextNode = fm.GetNextAttachableNode(faultDeviceInfo)
		require.Equal(t, expectedNextNode, actualNextNode)

		// any node can be selected because "node3" has also been removed from the fabric map, leaving nothing to attach.
		faultDeviceInfo = fds[1]
		expectedNextNode = ""
		actualNextNode = fm.GetNextAttachableNode(faultDeviceInfo)
		require.Equal(t, expectedNextNode, actualNextNode)

		// any node will be selected
		faultDeviceInfo = fds[2]
		expectedNextNode = ""
		actualNextNode = fm.GetNextAttachableNode(faultDeviceInfo)
		require.Equal(t, expectedNextNode, actualNextNode)
	})
}
