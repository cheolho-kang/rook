#!/usr/bin/env -S bash -e

num_disks=5

for i in $(seq 0 $((num_disks-1))); do
    DISK="/dev/nvme${i}n1"

    # Zap the disk to a fresh, usable state (zap-all is important, b/c MBR has to be clean)
    sgdisk --zap-all $DISK

    # Wipe a large portion of the beginning of the disk to remove more LVM metadata that may be present
    dd if=/dev/zero of="$DISK" bs=1M count=100 oflag=direct,dsync

    # SSDs may be better cleaned with blkdiscard instead of dd
    blkdiscard $DISK

    # Inform the OS of partition table changes
    partprobe $DISK

    # Create a new partition for rook ceph
    # parted $DISK --script mklabel gpt
    # # 1st partition is for meta data (/var/lib/ceph/osd/ceph-<osd-id>)
    # parted $DISK --script mkpart primary 1MiB 100GiB
    # # 2nd partition is for data (/var/lib/ceph/osd/ceph-<osd-id>/block)
    # parted $DISK --script mkpart primary 100GiB 100%
    # mkfs.ext4 ${DISK}p1
    # parted $DISK --script print

done
