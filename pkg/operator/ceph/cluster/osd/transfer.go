/*
Copyright 2023 The Rook Authors. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package osd for the Ceph OSDs.
package osd

import (
	"encoding/json"

	"github.com/pkg/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	OSDTransferConfigName = "osd-transfer-config"
	OSDTransferConfigKey  = "config"
)

type OSDTransferInfo struct {
	ID          int    `json:"id"`
	Node        string `json:"node"`
	FaultDomain string `json:"faultDomain"`
}

// getOSDTransferInfo returns an existing OSD info that needs to be transfered to a new node
func (c *Cluster) getOSDTransferInfo() (*OSDTransferInfo, error) {
	cm, err := c.context.Clientset.CoreV1().ConfigMaps(c.clusterInfo.Namespace).Get(
		c.clusterInfo.Context, OSDTransferConfigName, metav1.GetOptions{})
	if err != nil {
		if kerrors.IsNotFound(err) {
			return nil, nil
		}
	}

	configStr, ok := cm.Data[OSDTransferConfigKey]
	if !ok || configStr == "" {
		logger.Debugf("empty config map %q", OSDTransferConfigName)
		return nil, nil
	}

	config := &OSDTransferInfo{}
	err = json.Unmarshal([]byte(configStr), config)
	if err != nil {
		return nil, errors.Wrapf(
			err, "failed to JSON unmarshal osd replace status from the (%q)", configStr)
	}

	return config, nil
}
