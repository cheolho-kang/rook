#!/usr/bin/env -S bash -e
# this file path
pwd="$(dirname "$(realpath "$0")")"

# ConfigMap 생성에 사용할 변수 설정
CONFIGMAP_NAME="osd-transfer-config"
NAMESPACE="rook-ceph"
OSDID=$1
HOSTNAME="fabric-host-pbssd1"
TARGET_NODE=$3

# ConfigMap 생성을 위한 YAML 파일 생성
cat <<EOF > "${pwd}"/../manifest/${CONFIGMAP_NAME}.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${CONFIGMAP_NAME}
  namespace: ${NAMESPACE}
data:
  config: '{"id":${OSDID},"path":"${HOSTNAME}","node":"${TARGET_NODE}"}'
EOF

# ConfigMap 생성
kubectl apply -f "${pwd}"/../manifest/${CONFIGMAP_NAME}.yaml

# # 생성된 YAML 파일 삭제 (선택적)
# rm ${CONFIGMAP_NAME}.yaml

echo "ConfigMap ${CONFIGMAP_NAME} has been created."
kubectl get configmap ${CONFIGMAP_NAME} -n ${NAMESPACE} -o yaml
