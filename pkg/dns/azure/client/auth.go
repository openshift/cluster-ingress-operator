package client

import (
	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/adal"
	"github.com/Azure/go-autorest/autorest/azure"
)

func getAuthorizerForResource(config Config) (autorest.Authorizer, error) {
	env, err := azure.EnvironmentFromName(config.Environment)
	if err != nil {
		return nil, err
	}

	oauthConfig, err := adal.NewOAuthConfig(
		env.ActiveDirectoryEndpoint, config.TenantID)
	if err != nil {
		return nil, err
	}

	token, err := adal.NewServicePrincipalToken(
		*oauthConfig, config.ClientID, config.ClientSecret, env.ResourceManagerEndpoint)
	if err != nil {
		return nil, err
	}
	return autorest.NewBearerAuthorizer(token), err
}
