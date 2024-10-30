package nextjsauth0

import (
	"context"
	"github.com/codefly-dev/core/configurations"
	v0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/resources"
)

type Config struct {
	Auth0 struct {
		Audience     string `yaml:"audience"`
		Domain       string `yaml:"domain"`
		ClientId     string `yaml:"client_id"`
		ClientSecret string `yaml:"client_secret"`
	} `yaml:"auth0"`
}

func HandleConfig(ctx context.Context, info *v0.ConfigurationInformation, env *resources.EnvironmentVariableManager) error {
	var config Config
	err := configurations.InformationUnmarshal(info, &config)
	if err != nil {
		return err
	}
	env.AddEnvironmentVariable(ctx, "NEXT_PUBLIC_AUTH_TYPE", "auth0")
	env.AddEnvironmentVariable(ctx, "AUTH_TYPE", "auth0")
	env.AddEnvironmentVariable(ctx, "AUTH0_DOMAIN", config.Auth0.Domain)
	env.AddEnvironmentVariable(ctx, "AUTH0_CLIENT_ID", config.Auth0.ClientId)
	env.AddEnvironmentVariable(ctx, "AUTH0_CLIENT_SECRET", config.Auth0.ClientSecret)
	env.AddEnvironmentVariable(ctx, "AUTH0_AUDIENCE", config.Auth0.Audience)
	return nil
}
