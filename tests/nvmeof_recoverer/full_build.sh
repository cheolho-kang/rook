#!/usr/bin/env -S bash -e
ROOT_DIR=$(dirname $(dirname $(realpath $0))/../../../)

cd $ROOT_DIR
make codegen
make docs
make crds
make gen-rbac
make helm-docs
make check-docs
make generate-docs-crds
