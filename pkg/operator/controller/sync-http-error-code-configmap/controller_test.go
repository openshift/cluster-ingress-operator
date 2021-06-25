package sync_http_error_code_configmap

import (
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// errorpage404 is a data value for custom error code page configmap.
	errorpage404 = "HTTP/1.0  404 Custom Error\r\nPragma: no-cache\r\nCache-Control: private, max-age=0, no-cache, no-store\r\nConnection: close\r\nContent-Type: text/html\r\n\r\n<html>\r\n  <head>\r\n    <meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\r\n\r\n  <style type=\"text/css\">\r\n  /*!\r\n   * Bootstrap v3.3.5 (http://getbootstrap.com)\r\n   * Copyright 2011-2015 Twitter, Inc.\r\n   * Licensed under MIT (https://github.com/twbs/bootstrap/blob/master/LICENSE)\r\n   */\r\n  /*! normalize.css v3.0.3 | MIT License | github.com/necolas/normalize.css */\r\n  html {\r\n    font-family: sans-serif;\r\n    -ms-text-size-adjust: 100%;\r\n    -webkit-text-size-adjust: 100%;\r\n  }\r\n  body {\r\n    margin: 0;\r\n  }\r\n  h1 {\r\n    font-size: 1.7em;\r\n    font-weight: 400;\r\n    line-height: 1.3;\r\n    margin: 0.68em 0;\r\n  }\r\n  * {\r\n    -webkit-box-sizing: border-box;\r\n    -moz-box-sizing: border-box;\r\n    box-sizing: border-box;\r\n  }\r\n  *:before,\r\n  *:after {\r\n    -webkit-box-sizing: border-box;\r\n    -moz-box-sizing: border-box;\r\n    box-sizing: border-box;\r\n  }\r\n  html {\r\n    -webkit-tap-highlight-color: rgba(0, 0, 0, 0);\r\n  }\r\n  body {\r\n    font-family: \"Helvetica Neue\", Helvetica, Arial, sans-serif;\r\n    line-height: 1.66666667;\r\n    font-size: 13px;\r\n    color: #333333;\r\n    background-color: #ffffff;\r\n    margin: 2em 1em;\r\n  }\r\n  p {\r\n    margin: 0 0 10px;\r\n    font-size: 13px;\r\n  }\r\n  .alert.alert-info {\r\n    padding: 15px;\r\n    margin-bottom: 20px;\r\n    border: 1px solid transparent;\r\n    background-color: #f5f5f5;\r\n    border-color: #8b8d8f;\r\n    color: #363636;\r\n    margin-top: 30px;\r\n  }\r\n  .alert p {\r\n    padding-left: 35px;\r\n  }\r\n  a {\r\n    color: #0088ce;\r\n  }\r\n\r\n  ul {\r\n    position: relative;\r\n    padding-left: 51px;\r\n  }\r\n  p.info {\r\n    position: relative;\r\n    font-size: 15px;\r\n    margin-bottom: 10px;\r\n  }\r\n  p.info:before, p.info:after {\r\n    content: \"\";\r\n    position: absolute;\r\n    top: 9%;\r\n    left: 0;\r\n  }\r\n  p.info:before {\r\n    content: \"i\";\r\n    left: 3px;\r\n    width: 20px;\r\n    height: 20px;\r\n    font-family: serif;\r\n    font-size: 15px;\r\n    font-weight: bold;\r\n    line-height: 21px;\r\n    text-align: center;\r\n    color: #fff;\r\n    background: #4d5258;\r\n    border-radius: 16px;\r\n  }\r\n\r\n  @media (min-width: 768px) {\r\n    body {\r\n      margin: 4em 3em;\r\n    }\r\n    h1 {\r\n      font-size: 2.15em;}\r\n  }\r\n\r\n  </style>\r\n  </head>\r\n  <body>\r\n    <div>\r\n      <h1>Application or Document Not Found</h1>\r\n      <p>No application was found at the provided URL.</p>\r\n\r\n      <div class=\"alert alert-info\">\r\n        <p class=\"info\">\r\n          Possible reasons you are seeing this page:\r\n        </p>\r\n        <ul>\r\n          <li>\r\n            <strong>Moving a page.</strong>\r\n              If you recently added or moved a page, it's possible that the page was placed in the wrong folder.\r\n          </li>\r\n          <li>\r\n            <strong>Moving a page's directory.</strong>\r\n              Sometimes the page itself may not be the cause of a 404 — it could be the page's containing folder.\r\n          </li>\r\n          <li>\r\n            <strong>The host doesn't exist.</strong>\r\n            Make sure the hostname was typed correctly and that a route matching this hostname exists.\r\n          </li>\r\n          <li>\r\n            <strong>The host exists, but doesn't have a matching path.</strong>\r\n            Check if the URL path was typed correctly and that the route was created using the desired path.\r\n          </li>\r\n          <li>\r\n            <strong>Route and path matches, but all pods are down.</strong>\r\n            Make sure that the resources exposed by this route (pods, services, deployment configs, etc) have at least one pod running.\r\n          </li>\r\n        </ul>\r\n      </div>\r\n    </div>\r\n  </body>\r\n</html>\r\n"
	// errorpage503 is a data value for custom error code page configmap.
	errorpage503 = "HTTP/1.0 503 Custom Service Unavailable\r\nPragma: no-cache\r\nCache-Control: private, max-age=0, no-cache, no-store\r\nConnection: close\r\nContent-Type: text/html\r\n\r\n<html>\r\n  <head>\r\n    <meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\r\n\r\n  <style type=\"text/css\">\r\n  /*!\r\n   * Bootstrap v3.3.5 (http://getbootstrap.com)\r\n   * Copyright 2011-2015 Twitter, Inc.\r\n   * Licensed under MIT (https://github.com/twbs/bootstrap/blob/master/LICENSE)\r\n   */\r\n  /*! normalize.css v3.0.3 | MIT License | github.com/necolas/normalize.css */\r\n  html {\r\n    font-family: sans-serif;\r\n    -ms-text-size-adjust: 100%;\r\n    -webkit-text-size-adjust: 100%;\r\n  }\r\n  body {\r\n    margin: 0;\r\n  }\r\n  h1 {\r\n    font-size: 1.7em;\r\n    font-weight: 400;\r\n    line-height: 1.3;\r\n    margin: 0.68em 0;\r\n  }\r\n  * {\r\n    -webkit-box-sizing: border-box;\r\n    -moz-box-sizing: border-box;\r\n    box-sizing: border-box;\r\n  }\r\n  *:before,\r\n  *:after {\r\n    -webkit-box-sizing: border-box;\r\n    -moz-box-sizing: border-box;\r\n    box-sizing: border-box;\r\n  }\r\n  html {\r\n    -webkit-tap-highlight-color: rgba(0, 0, 0, 0);\r\n  }\r\n  body {\r\n    font-family: \"Helvetica Neue\", Helvetica, Arial, sans-serif;\r\n    line-height: 1.66666667;\r\n    font-size: 13px;\r\n    color: #333333;\r\n    background-color: #ffffff;\r\n    margin: 2em 1em;\r\n  }\r\n  p {\r\n    margin: 0 0 10px;\r\n    font-size: 13px;\r\n  }\r\n  .alert.alert-info {\r\n    padding: 15px;\r\n    margin-bottom: 20px;\r\n    border: 1px solid transparent;\r\n    background-color: #f5f5f5;\r\n    border-color: #8b8d8f;\r\n    color: #363636;\r\n    margin-top: 30px;\r\n  }\r\n  .alert p {\r\n    padding-left: 35px;\r\n  }\r\n  a {\r\n    color: #0088ce;\r\n  }\r\n\r\n  ul {\r\n    position: relative;\r\n    padding-left: 51px;\r\n  }\r\n  p.info {\r\n    position: relative;\r\n    font-size: 15px;\r\n    margin-bottom: 10px;\r\n  }\r\n  p.info:before, p.info:after {\r\n    content: \"\";\r\n    position: absolute;\r\n    top: 9%;\r\n    left: 0;\r\n  }\r\n  p.info:before {\r\n    content: \"i\";\r\n    left: 3px;\r\n    width: 20px;\r\n    height: 20px;\r\n    font-family: serif;\r\n    font-size: 15px;\r\n    font-weight: bold;\r\n    line-height: 21px;\r\n    text-align: center;\r\n    color: #fff;\r\n    background: #4d5258;\r\n    border-radius: 16px;\r\n  }\r\n\r\n  @media (min-width: 768px) {\r\n    body {\r\n      margin: 4em 3em;\r\n    }\r\n    h1 {\r\n      font-size: 2.15em;}\r\n  }\r\n\r\n  </style>\r\n  </head>\r\n  <body>\r\n    <div>\r\n      <h1>Application is not available</h1>\r\n      <p>The application is currently not serving requests at this endpoint. It may not have been started or is still starting.</p>\r\n\r\n      <div class=\"alert alert-info\">\r\n        <p class=\"info\">\r\n          Possible reasons you are seeing this page:\r\n        </p>\r\n        <ul>\r\n          <li>\r\n            <strong>The host doesn't exist.</strong>\r\n            Make sure the hostname was typed correctly and that a route matching this hostname exists.\r\n          </li>\r\n          <li>\r\n            <strong>The host exists, but doesn't have a matching path.</strong>\r\n            Check if the URL path was typed correctly and that the route was created using the desired path.\r\n          </li>\r\n          <li>\r\n            <strong>Route and path matches, but all pods are down.</strong>\r\n            Make sure that the resources exposed by this route (pods, services, deployment configs, etc) have at least one pod running.\r\n          </li>\r\n        </ul>\r\n      </div>\r\n    </div>\r\n  </body>\r\n</html>\r\n"
)

// newSecret returns a secret with the specified name and with data fields
// "tls.crt" and "tls.key" containing valid PEM-encoded certificate and private
// key, respectively.  Note that the certificate and key are valid only in the
// sense that they respect PEM encoding, not that they have any particular
// subject etc.
func newConfigMap(name, namespace string, flag string) corev1.ConfigMap {
	var data map[string]string
	if flag == "configMapWithCustom404And503" {
		data = map[string]string{
			"error-page-404.http": errorpage404,
			"error-page-503.http": errorpage503,
		}
	}
	if flag == "configMapWithCustom404" {
		data = map[string]string{
			"error-page-404.http": errorpage404,
		}
	}
	if flag == "configMapWithCustom503" {
		data = map[string]string{
			"error-page-503.http": errorpage503,
		}
	}
	return corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: data,
	}
}

// TestDesiredHttpErrorCodeConfigMap verifies that
// desiredHttpErrorCodeConfigMap returns the expected configmap.
func TestDesiredHttpErrorCodeConfigMap(t *testing.T) {
	type testInputs struct {
		configmap corev1.ConfigMap
	}
	type testOutputs struct {
		configMap *corev1.ConfigMap
	}
	var (
		configMapWithCustom404And503 = newConfigMap("my-custom-error-code-pages", "openshift-config", "configMapWithCustom404And503")
		configMapWithCustom404       = newConfigMap("my-custom-error-code-pages", "openshift-config", "configMapWithCustom404")
		configMapWithCustom503       = newConfigMap("my-custom-error-code-pages", "openshift-config", "configMapWithCustom503")
	)
	deployment := &appsv1.Deployment{}
	trueVar := true
	deploymentRef := metav1.OwnerReference{
		APIVersion: appsv1.SchemeGroupVersion.String(),
		Kind:       "Deployment",
		Name:       deployment.Name,
		UID:        deployment.UID,
		Controller: &trueVar,
	}

	testCases := []struct {
		description string
		inputs      testInputs
		output      testOutputs
	}{
		{
			description: "ingress controller has custom error code configmap name defined and custom error code config map is present in openshift-config namespace",
			inputs: testInputs{
				configMapWithCustom404And503,
			},
			output: testOutputs{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-custom-error-code-pages",
						Namespace: "openshift-ingress",
					},
					Data: map[string]string{
						"error-page-404.http": errorpage404,
						"error-page-503.http": errorpage503,
					},
				},
			},
		},
		{
			description: "ingress controller has only one custom error code configmap name defined i.e for 404 and custom error code config map is present in openshift-config namespace",
			inputs: testInputs{
				configMapWithCustom404,
			},
			output: testOutputs{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-custom-error-code-pages",
						Namespace: "openshift-ingress",
					},
					Data: map[string]string{
						"error-page-404.http": errorpage404,
						"error-page-503.http": DEFAULT_503_ERROR_PAGE,
					},
				},
			},
		},
		{
			description: "ingress controller has only one custom error code configmap name defined i.e for 503 and custom error code config map is present in openshift-config namespace",
			inputs: testInputs{
				configMapWithCustom503,
			},
			output: testOutputs{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-custom-error-code-pages",
						Namespace: "openshift-ingress",
					},
					Data: map[string]string{
						"error-page-404.http": DEFAULT_404_ERROR_PAGE,
						"error-page-503.http": errorpage503,
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		expected := tc.output.configMap
		name := types.NamespacedName{Name: tc.inputs.configmap.Name, Namespace: tc.inputs.configmap.Namespace}
		_, actual, err := desiredHttpErrorCodeConfigMap(true, &tc.inputs.configmap, name, deploymentRef)
		if err != nil {
			t.Errorf("failed to get error-page configmap: %v", err)
			continue
		}

		if !reflect.DeepEqual(expected.Data, actual.Data) {
			t.Errorf("expected #{expected} and actual #{actual} configmap don't match")
		}

		if expected == nil || actual == nil {
			if expected != nil {
				t.Errorf("%q: expected %v, got nil", tc.description, expected)
			}
			if actual != nil {
				t.Errorf("%q: expected nil, got %v", tc.description, actual)
			}
			continue
		}
	}
}
