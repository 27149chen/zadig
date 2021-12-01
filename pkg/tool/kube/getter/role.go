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

package getter

import (
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func GetRole(ns, name string, cl client.Client) (*rbacv1.Role, bool, error) {
	g := &rbacv1.Role{}
	found, err := GetResourceInCache(ns, name, g, cl)
	if err != nil || !found {
		g = nil
	}

	return g, found, err
}

func GetClusterRole(name string, cl client.Client) (*rbacv1.ClusterRole, bool, error) {
	g := &rbacv1.ClusterRole{}
	found, err := GetResourceInCache("", name, g, cl)
	if err != nil || !found {
		g = nil
	}

	return g, found, err
}
