package framework

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	"github.com/google/uuid"
	oauthv1 "github.com/openshift/api/oauth/v1"
	userv1 "github.com/openshift/api/user/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
)

func (c *CLI) GetClientConfigForUser(username string) (*rest.Config, error) {
	// Not supported on non Openshift tests
	if c.openshiftVersion == nil {
		return nil, fmt.Errorf("GetClientConfigForUser is not supported on non Openshift environments")
	}

	ctx := context.Background()
	userClient := c.AdminUserClient()

	user, err := userClient.UserV1().Users().Get(ctx, username, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}
	if err != nil {
		user, err = userClient.UserV1().Users().Create(ctx, &userv1.User{
			ObjectMeta: metav1.ObjectMeta{Name: username},
		}, metav1.CreateOptions{})
		if err != nil {
			return nil, err
		}
		c.AddResourceToDelete(userv1.GroupVersion.WithResource("users"), user)
	}

	oauthClient := c.AdminOAuthClient()
	oauthClientName := "e2e-client-" + c.GetNamespace()
	oauthClientObj, err := oauthClient.OauthV1().OAuthClients().Create(ctx, &oauthv1.OAuthClient{
		ObjectMeta:  metav1.ObjectMeta{Name: oauthClientName},
		GrantMethod: oauthv1.GrantHandlerAuto,
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		FatalErr(err)
	}
	if oauthClientObj != nil {
		c.AddExplicitResourceToDelete(oauthv1.GroupVersion.WithResource("oauthclients"), "", oauthClientName)
	}

	privToken, pubToken := GenerateOAuthTokenPair()
	token, err := oauthClient.OauthV1().OAuthAccessTokens().Create(ctx, &oauthv1.OAuthAccessToken{
		ObjectMeta:  metav1.ObjectMeta{Name: pubToken},
		ClientName:  oauthClientName,
		UserName:    username,
		UserUID:     string(user.UID),
		Scopes:      []string{"user:full"},
		RedirectURI: "https://localhost:8443/oauth/token/implicit",
	}, metav1.CreateOptions{})
	if err != nil {
		FatalErr(err)
	}
	c.AddResourceToDelete(oauthv1.GroupVersion.WithResource("oauthaccesstokens"), token)

	userClientConfig := rest.AnonymousClientConfig(turnOffRateLimiting(rest.CopyConfig(c.AdminConfig())))
	userClientConfig.BearerToken = privToken

	return userClientConfig, nil
}

// GenerateOAuthTokenPair returns two tokens to use with OpenShift OAuth-based authentication.
// The first token is a private token meant to be used as a Bearer token to send
// queries to the API, the second token is a hashed token meant to be stored in
// the database.
func GenerateOAuthTokenPair() (privToken, pubToken string) {
	const sha256Prefix = "sha256~"
	randomToken := base64.RawURLEncoding.EncodeToString([]byte(uuid.NewString()))
	hashed := sha256.Sum256([]byte(randomToken))
	return sha256Prefix + string(randomToken), sha256Prefix + base64.RawURLEncoding.EncodeToString(hashed[:])
}

// turnOffRateLimiting reduces the chance that a flaky test can be written while using this package
func turnOffRateLimiting(config *rest.Config) *rest.Config {
	configCopy := *config
	configCopy.QPS = 10000
	configCopy.Burst = 10000
	configCopy.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
	// We do not set a timeout because that will cause watches to fail
	// Integration tests are already limited to 5 minutes
	// configCopy.Timeout = time.Minute
	return &configCopy
}
