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

package aslan

import (
	"go.uber.org/zap"

	"github.com/koderover/zadig/pkg/tool/httpclient"
)

type Testing struct {
	Name     string `bson:"name"                     json:"name"`
	TestType string `bson:"test_type"                json:"test_type"`
}

func (c *Client) ListTestings(log *zap.SugaredLogger) ([]*Testing, error) {
	url := "/api/testing/test"
	resp := make([]*Testing, 0)

	_, err := c.Get(url, httpclient.SetResult(&resp))
	if err != nil {
		log.Errorf("ListTestings error: %s", err)
		return nil, err
	}

	return resp, nil
}

type TestTaskStat struct {
	Name          string `bson:"name"                    json:"name"`
	TotalSuccess  int    `bson:"total_success"           json:"totalSuccess"`
	TotalFailure  int    `bson:"total_failure"           json:"totalFailure"`
	TotalDuration int64  `bson:"total_duration"          json:"totalDuration"`
	TestCaseNum   int    `bson:"test_case_num"           json:"testCaseNum"`
	CreateTime    int64  `bson:"create_time"             json:"createTime"`
	UpdateTime    int64  `bson:"update_time"             json:"updateTime"`
}

func (c *Client) ListTestTaskStats(log *zap.SugaredLogger) ([]*TestTaskStat, error) {
	url := "/api/testing/teststat"
	resp := make([]*TestTaskStat, 0)

	_, err := c.Get(url, httpclient.SetResult(&resp))
	if err != nil {
		log.Errorf("list test task stat error: %s", err)
		return nil, err
	}

	return resp, nil
}
