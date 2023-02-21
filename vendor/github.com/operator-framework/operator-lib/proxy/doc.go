// Copyright 2021 The Operator-SDK Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/*
Package proxy implements helper functions to facilitate making operators proxy-aware.

This package assumes that proxy environment variables `HTTPS_PROXY`,
`HTTP_PROXY`, and/or `NO_PROXY` are set in the Operator environment, typically
via the Operator deployment.

Proxy-aware operators can use the ReadProxyVarsFromEnv to retrieve these values
as a slice of corev1 EnvVars. Each of the proxy variables are duplicated in
upper and lower case to support applications that use either. In their
reconcile functions, Operator authors are then responsible for setting these
variables in the Container Envs that must use the proxy, For example:

	// Pods with Kubernetes < 1.22
	for i, cSpec := range (myPod.Spec.Containers) {
		myPod.Spec.Containers[i].Env = append(cSpec.Env, ReadProxyVarsFromEnv()...)
	}
*/
package proxy
