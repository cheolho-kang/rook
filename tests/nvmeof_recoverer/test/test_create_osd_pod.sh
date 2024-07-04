#!/usr/bin/env -S bash -e
pwd=$(dirname $(dirname $(realpath $0)))

kubectl cp rook-ceph/$(kubectl -n rook-ceph get pod -l "app=rook-ceph-mon" -o jsonpath='{.items[0].metadata.name}'):/etc/ceph/keyring-store/..data/keyring ${pwd}/../conf/keyring

# press enter to continue
read -p "Press enter to continue"

kubectl apply -f ${pwd}/../manifest/test_pod.yaml
