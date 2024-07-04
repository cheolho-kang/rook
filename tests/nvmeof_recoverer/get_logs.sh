NAMESPACE="nvmeof-recoverer"
NODE1="qemu1"
NODE2="qemu2"

kubectl logs -n ${NAMESPACE}-system $(kubectl -n ${NAMESPACE}-system get pod -l "app=rook-ceph-operator" -o jsonpath='{.items[0].metadata.name}') > operator.log
kubectl logs -n ${NAMESPACE}-system $(kubectl -n ${NAMESPACE}-system get pod -l "app=rook-ceph-operator" -o jsonpath='{.items[0].metadata.name}') --previous > operator.log
kubectl logs -n ${NAMESPACE} $(kubectl -n ${NAMESPACE} get pod -l "batch.kubernetes.io/job-name=rook-ceph-osd-prepare-${NODE1}" -o jsonpath='{.items[0].metadata.name}') > operator_prepare_${NODE1}.log
kubectl logs -n ${NAMESPACE} $(kubectl -n ${NAMESPACE} get pod -l "batch.kubernetes.io/job-name=rook-ceph-osd-prepare-${NODE2}" -o jsonpath='{.items[0].metadata.name}') > operator_prepare_${NODE2}.log
