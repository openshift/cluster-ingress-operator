package sync_http_error_code_configmap

import (
	"context"
	"fmt"
	"reflect"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	operatorv1 "github.com/openshift/api/operator/v1"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	DEFAULT_503_ERROR_PAGE = "HTTP/1.0 503 Service Unavailable\r\nPragma: no-cache\r\nCache-Control: private, max-age=0, no-cache, no-store\r\nConnection: close\r\nContent-Type: text/html\r\n\r\n<html>\r\n  <head>\r\n    <meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\r\n\r\n  <style type=\"text/css\">\r\n  /*!\r\n   * Bootstrap v3.3.5 (http://getbootstrap.com)\r\n   * Copyright 2011-2015 Twitter, Inc.\r\n   * Licensed under MIT (https://github.com/twbs/bootstrap/blob/master/LICENSE)\r\n   */\r\n  /*! normalize.css v3.0.3 | MIT License | github.com/necolas/normalize.css */\r\n  html {\r\n    font-family: sans-serif;\r\n    -ms-text-size-adjust: 100%;\r\n    -webkit-text-size-adjust: 100%;\r\n  }\r\n  body {\r\n    margin: 0;\r\n  }\r\n  h1 {\r\n    font-size: 1.7em;\r\n    font-weight: 400;\r\n    line-height: 1.3;\r\n    margin: 0.68em 0;\r\n  }\r\n  * {\r\n    -webkit-box-sizing: border-box;\r\n    -moz-box-sizing: border-box;\r\n    box-sizing: border-box;\r\n  }\r\n  *:before,\r\n  *:after {\r\n    -webkit-box-sizing: border-box;\r\n    -moz-box-sizing: border-box;\r\n    box-sizing: border-box;\r\n  }\r\n  html {\r\n    -webkit-tap-highlight-color: rgba(0, 0, 0, 0);\r\n  }\r\n  body {\r\n    font-family: \"Helvetica Neue\", Helvetica, Arial, sans-serif;\r\n    line-height: 1.66666667;\r\n    font-size: 13px;\r\n    color: #333333;\r\n    background-color: #ffffff;\r\n    margin: 2em 1em;\r\n  }\r\n  p {\r\n    margin: 0 0 10px;\r\n    font-size: 13px;\r\n  }\r\n  .alert.alert-info {\r\n    padding: 15px;\r\n    margin-bottom: 20px;\r\n    border: 1px solid transparent;\r\n    background-color: #f5f5f5;\r\n    border-color: #8b8d8f;\r\n    color: #363636;\r\n    margin-top: 30px;\r\n  }\r\n  .alert p {\r\n    padding-left: 35px;\r\n  }\r\n  a {\r\n    color: #0088ce;\r\n  }\r\n\r\n  ul {\r\n    position: relative;\r\n    padding-left: 51px;\r\n  }\r\n  p.info {\r\n    position: relative;\r\n    font-size: 15px;\r\n    margin-bottom: 10px;\r\n  }\r\n  p.info:before, p.info:after {\r\n    content: \"\";\r\n    position: absolute;\r\n    top: 9%;\r\n    left: 0;\r\n  }\r\n  p.info:before {\r\n    content: \"i\";\r\n    left: 3px;\r\n    width: 20px;\r\n    height: 20px;\r\n    font-family: serif;\r\n    font-size: 15px;\r\n    font-weight: bold;\r\n    line-height: 21px;\r\n    text-align: center;\r\n    color: #fff;\r\n    background: #4d5258;\r\n    border-radius: 16px;\r\n  }\r\n\r\n  @media (min-width: 768px) {\r\n    body {\r\n      margin: 4em 3em;\r\n    }\r\n    h1 {\r\n      font-size: 2.15em;}\r\n  }\r\n\r\n  </style>\r\n  </head>\r\n  <body>\r\n    <div>\r\n      <h1>Application is not available</h1>\r\n      <p>The application is currently not serving requests at this endpoint. It may not have been started or is still starting.</p>\r\n\r\n      <div class=\"alert alert-info\">\r\n        <p class=\"info\">\r\n          Possible reasons you are seeing this page:\r\n        </p>\r\n        <ul>\r\n          <li>\r\n            <strong>The host doesn't exist.</strong>\r\n            Make sure the hostname was typed correctly and that a route matching this hostname exists.\r\n          </li>\r\n          <li>\r\n            <strong>The host exists, but doesn't have a matching path.</strong>\r\n            Check if the URL path was typed correctly and that the route was created using the desired path.\r\n          </li>\r\n          <li>\r\n            <strong>Route and path matches, but all pods are down.</strong>\r\n            Make sure that the resources exposed by this route (pods, services, deployment configs, etc) have at least one pod running.\r\n          </li>\r\n        </ul>\r\n      </div>\r\n    </div>\r\n  </body>\r\n</html>\r\n"
	DEFAULT_404_ERROR_PAGE = "HTTP/1.0 404 Error\r\nPragma: no-cache\r\nCache-Control: private, max-age=0, no-cache, no-store\r\nConnection: close\r\nContent-Type: text/html\r\n\r\n<html>\r\n  <head>\r\n    <meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\r\n\r\n  <style type=\"text/css\">\r\n  /*!\r\n   * Bootstrap v3.3.5 (http://getbootstrap.com)\r\n   * Copyright 2011-2015 Twitter, Inc.\r\n   * Licensed under MIT (https://github.com/twbs/bootstrap/blob/master/LICENSE)\r\n   */\r\n  /*! normalize.css v3.0.3 | MIT License | github.com/necolas/normalize.css */\r\n  html {\r\n    font-family: sans-serif;\r\n    -ms-text-size-adjust: 100%;\r\n    -webkit-text-size-adjust: 100%;\r\n  }\r\n  body {\r\n    margin: 0;\r\n  }\r\n  h1 {\r\n    font-size: 1.7em;\r\n    font-weight: 400;\r\n    line-height: 1.3;\r\n    margin: 0.68em 0;\r\n  }\r\n  * {\r\n    -webkit-box-sizing: border-box;\r\n    -moz-box-sizing: border-box;\r\n    box-sizing: border-box;\r\n  }\r\n  *:before,\r\n  *:after {\r\n    -webkit-box-sizing: border-box;\r\n    -moz-box-sizing: border-box;\r\n    box-sizing: border-box;\r\n  }\r\n  html {\r\n    -webkit-tap-highlight-color: rgba(0, 0, 0, 0);\r\n  }\r\n  body {\r\n    font-family: \"Helvetica Neue\", Helvetica, Arial, sans-serif;\r\n    line-height: 1.66666667;\r\n    font-size: 13px;\r\n    color: #333333;\r\n    background-color: #ffffff;\r\n    margin: 2em 1em;\r\n  }\r\n  p {\r\n    margin: 0 0 10px;\r\n    font-size: 13px;\r\n  }\r\n  .alert.alert-info {\r\n    padding: 15px;\r\n    margin-bottom: 20px;\r\n    border: 1px solid transparent;\r\n    background-color: #f5f5f5;\r\n    border-color: #8b8d8f;\r\n    color: #363636;\r\n    margin-top: 30px;\r\n  }\r\n  .alert p {\r\n    padding-left: 35px;\r\n  }\r\n  a {\r\n    color: #0088ce;\r\n  }\r\n\r\n  ul {\r\n    position: relative;\r\n    padding-left: 51px;\r\n  }\r\n  p.info {\r\n    position: relative;\r\n    font-size: 15px;\r\n    margin-bottom: 10px;\r\n  }\r\n  p.info:before, p.info:after {\r\n    content: \"\";\r\n    position: absolute;\r\n    top: 9%;\r\n    left: 0;\r\n  }\r\n  p.info:before {\r\n    content: \"i\";\r\n    left: 3px;\r\n    width: 20px;\r\n    height: 20px;\r\n    font-family: serif;\r\n    font-size: 15px;\r\n    font-weight: bold;\r\n    line-height: 21px;\r\n    text-align: center;\r\n    color: #fff;\r\n    background: #4d5258;\r\n    border-radius: 16px;\r\n  }\r\n\r\n  @media (min-width: 768px) {\r\n    body {\r\n      margin: 4em 3em;\r\n    }\r\n    h1 {\r\n      font-size: 2.15em;}\r\n  }\r\n\r\n  </style>\r\n  </head>\r\n  <body>\r\n    <div>\r\n      <h1>Application or Document Not Found</h1>\r\n      <p>No application was found at the provided URL.</p>\r\n\r\n      <div class=\"alert alert-info\">\r\n        <p class=\"info\">\r\n          Possible reasons you are seeing this page:\r\n        </p>\r\n        <ul>\r\n          <li>\r\n            <strong>Moving a page.</strong>\r\n              If you recently added or moved a page, it's possible that the page was placed in the wrong folder.\r\n          </li>\r\n          <li>\r\n            <strong>Moving a page's directory.</strong>\r\n              Sometimes the page itself may not be the cause of a 404 — it could be the page's containing folder.\r\n          </li>\r\n          <li>\r\n            <strong>The host doesn't exist.</strong>\r\n            Make sure the hostname was typed correctly and that a route matching this hostname exists.\r\n          </li>\r\n          <li>\r\n            <strong>The host exists, but doesn't have a matching path.</strong>\r\n            Check if the URL path was typed correctly and that the route was created using the desired path.\r\n          </li>\r\n          <li>\r\n            <strong>Route and path matches, but all pods are down.</strong>\r\n            Make sure that the resources exposed by this route (pods, services, deployment configs, etc) have at least one pod running.\r\n          </li>\r\n        </ul>\r\n      </div>\r\n    </div>\r\n  </body>\r\n</html>\r\n"
)

// ensureHttpErrorCodeConfigMap ensures the http error code configmap exists for
// a given ingresscontroller and syncs them between openshift-config and
// openshift-ingress.  Returns a Boolean indicating whether the configmap
// exists, the configmap if it does exist, and an error value.
func (r *reconciler) ensureHttpErrorCodeConfigMap(ic *operatorv1.IngressController, deploymentRef metav1.OwnerReference) (bool, *corev1.ConfigMap, error) {
	sourceName := types.NamespacedName{
		Namespace: operatorcontroller.GlobalUserSpecifiedConfigNamespace,
		Name:      ic.Spec.HttpErrorCodePages.Name,
	}
	haveSource, source, err := r.currentHttpErrorCodeConfigMap(sourceName)
	if err != nil {
		return false, nil, err
	}
	name := operatorcontroller.HttpErrorCodePageConfigMapName(ic)
	have, current, err := r.currentHttpErrorCodeConfigMap(name)
	if err != nil {
		return false, nil, err
	}
	want, desired, err := desiredHttpErrorCodeConfigMap(haveSource, source, name, deploymentRef)
	if err != nil {
		return have, current, err
	}
	switch {
	case !want && !have:
		return false, nil, nil
	case !want && have:
		if err := r.client.Delete(context.TODO(), current); err != nil {
			if !errors.IsNotFound(err) {
				return true, current, fmt.Errorf("failed to delete configmap: %w", err)
			}
		} else {
			log.Info("deleted configmap", "namespace", current.Namespace, "name", current.Name)
		}
		return false, nil, nil
	case want && !have:
		if err := r.client.Create(context.TODO(), desired); err != nil {
			return false, nil, fmt.Errorf("failed to create configmap %s/%s: %v", desired.Namespace, desired.Name, err)
		}
		log.Info("created configmap", "namespace", desired.Namespace, "name", desired.Name)
		return r.currentHttpErrorCodeConfigMap(name)
	case want && have:
		if updated, err := r.updateHttpErrorCodeConfigMap(current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update configmap %s/%s: %v", current.Namespace, current.Name, err)
		} else if updated {
			return r.currentHttpErrorCodeConfigMap(name)
		}
	}

	return have, current, nil
}

// desiredHttpErrorCodeConfigMap returns the desired error-page configmap.
// Returns a Boolean indicating whether a configmap is desired, as well as the
// configmap if one is desired.
func desiredHttpErrorCodeConfigMap(haveSource bool, sourceConfigmap *corev1.ConfigMap, name types.NamespacedName, deploymentRef metav1.OwnerReference) (bool, *corev1.ConfigMap, error) {
	if !haveSource {
		return false, nil, nil
	}
	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
		},
		Data: map[string]string{},
	}
	if val, ok := sourceConfigmap.Data["error-page-503.http"]; ok {
		cm.Data["error-page-503.http"] = val
	} else {
		cm.Data["error-page-503.http"] = DEFAULT_503_ERROR_PAGE
	}
	if val, ok := sourceConfigmap.Data["error-page-404.http"]; ok {
		cm.Data["error-page-404.http"] = val
	} else {
		cm.Data["error-page-404.http"] = DEFAULT_404_ERROR_PAGE
	}
	cm.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
	return true, &cm, nil
}

// currentHttpErrorCodeConfigMap returns the current configmap.  Returns a
// Boolean indicating whether the configmap existed, the configmap if it did
// exist, and an error value.
func (r *reconciler) currentHttpErrorCodeConfigMap(name types.NamespacedName) (bool, *corev1.ConfigMap, error) {
	if len(name.Name) == 0 {
		return false, nil, nil
	}
	cm := &corev1.ConfigMap{}
	if err := r.client.Get(context.TODO(), name, cm); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, cm, nil
}

// updateHttpErrorCodeConfigMap updates a configmap.  Returns a Boolean
// indicating whether the configmap was updated, and an error value.
func (r *reconciler) updateHttpErrorCodeConfigMap(current, desired *corev1.ConfigMap) (bool, error) {
	if configmapsEqual(current, desired) {
		return false, nil
	}
	updated := current.DeepCopy()
	updated.Data = desired.Data
	if err := r.client.Update(context.TODO(), updated); err != nil {
		if errors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, err
	}
	log.Info("updated configmap", "namespace", updated.Namespace, "name", updated.Name)
	return true, nil
}

// configmapsEqual compares two httpErrorCodePage configmaps.  Returns true if
// the configmaps should be considered equal for the purpose of determining
// whether an update is necessary, false otherwise.
func configmapsEqual(a, b *corev1.ConfigMap) bool {
	if !reflect.DeepEqual(a.Data, b.Data) {
		return false
	}
	return true
}
