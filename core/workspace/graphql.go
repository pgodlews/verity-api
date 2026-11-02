package workspace

import (
	"context"
	"net/http"
	"time"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/graphql"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/runner"
	"github.com/pgodlews/verity-api/core/spi"
)

// GraphQLTemplateRequest selects the step whose schema supplies operations.
type GraphQLTemplateRequest struct {
	Path        string `json:"path"`
	Suite       string `json:"suite"`
	Case        string `json:"case"`
	Index       int    `json:"index"`
	Environment string `json:"environment"`
}

func graphqlRunOptions(env string) graphql.Options {
	const timeout = 30 * time.Second
	return graphql.Options{
		Environment: env,
		Registry:    bootstrap.Default(),
		Timeout:     timeout,
		NewClient: func(cfg *spi.TLSConfig) (*http.Client, error) {
			return runner.NewHTTPClient(cfg, timeout, true)
		},
	}
}

// GraphQLTemplates lists insertable operations for one graphql step.
// Depth is 2. The SDL and any error text are redacted by core/graphql.
// sessions carries desktop OAuth sign-ins; nil is the CLI and dev bridge.
func GraphQLTemplates(ctx context.Context, req GraphQLTemplateRequest, sessions *OAuthSessions) ([]graphql.Template, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	p, err := model.Load(req.Path)
	if err != nil {
		return nil, err
	}
	loc, err := graphql.FindAt(p, req.Suite, req.Case, req.Index)
	if err != nil {
		return nil, err
	}
	opt, err := graphqlOptionsFor(p, req.Path, req.Environment, nil, sessions)
	if err != nil {
		return nil, err
	}
	return graphql.StepTemplates(ctx, p, loc, 2, opt)
}

func graphqlOptionsFor(p *model.Project, path, environment string, extra map[string]string, sessions *OAuthSessions) (graphql.Options, error) {
	opt := graphqlRunOptions(environment)
	tokens, err := sessions.forRun(p, RunOptions{Path: path, Environment: environment, Vars: extra})
	if err != nil {
		return graphql.Options{}, err
	}
	opt.AuthSessions = tokens
	return opt, nil
}
