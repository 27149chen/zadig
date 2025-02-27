/*
Copyright 2021 The KodeRover Authors.

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

package aslanx

import (
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/koderover/zadig/pkg/tool/httpclient"
)

type operationLog struct {
	Username       string `json:"username"`
	ProductName    string `json:"product_name"`
	Method         string `json:"method"`
	PermissionUUID string `json:"permission_uuid"`
	Function       string `json:"function"`
	Name           string `json:"name"`
	RequestBody    string `json:"request_body"`
	Status         int    `json:"status"`
	CreatedAt      int64  `json:"created_at"`
}

func (c *Client) AddAuditLog(username, productName, method, function, detail, permissionUUID, requestBody string, log *zap.SugaredLogger) (string, error) {
	url := "/api/aslanx/enterprise/operation"
	req := operationLog{
		Username:       username,
		ProductName:    productName,
		Method:         method,
		PermissionUUID: permissionUUID,
		Function:       function,
		Name:           detail,
		RequestBody:    requestBody,
		Status:         0,
		CreatedAt:      time.Now().Unix(),
	}

	var operationLogID string
	_, err := c.Post(url, httpclient.SetBody(req), httpclient.SetResult(&operationLogID))
	// ignore not found error
	if err != nil && !httpclient.IsNotFound(err) {
		log.Errorf("Failed to add audit log, error: %s", err)
		return "", err
	}

	return operationLogID, nil
}

type updateOperationArgs struct {
	Status int `json:"status"`
}

func (c *Client) UpdateAuditLog(id string, status int, log *zap.SugaredLogger) error {
	url := fmt.Sprintf("/api/aslanx/enterprise/operation/%s", id)
	req := updateOperationArgs{
		Status: status,
	}

	_, err := c.Put(url, httpclient.SetBody(req))
	// ignore not found error
	if err != nil && !httpclient.IsNotFound(err) {
		log.Errorf("Failed to update audit log, error: %s", err)
		return err
	}

	return nil
}
