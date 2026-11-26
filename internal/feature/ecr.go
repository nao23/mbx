package feature

import (
	_ "embed"
	"errors"

	"github.com/nao23/msbx/internal/config"
	"github.com/nao23/msbx/internal/secret"
	"github.com/nao23/msbx/internal/shell"
)

//go:embed ecr.sh
var ecrScript string

func ecrSecret(cfg *config.Config) *Secret {
	e := cfg.Docker.ECR
	return &Secret{Env: "ECR_TOKEN", Hosts: []string{e.Registry}, Source: e.SecretSource}
}

func newECR(cfg *config.Config) (*Feature, error) {
	e := cfg.Docker.ECR
	if !e.Enabled {
		return nil, nil
	}
	if !cfg.Docker.Enabled {
		return nil, errors.New("docker.ecr requires docker (remove --no-docker)")
	}
	if e.Registry == "" {
		return nil, errors.New("docker.ecr.registry is not set")
	}
	if e.SecretSource == (secret.Source{}) {
		return nil, errors.New("docker.ecr.secret_source is not set")
	}
	region := config.ECRRegion(e.Registry)
	f := &Feature{
		Name:   "docker.ecr",
		Secret: ecrSecret(cfg),
		Setup: &Setup{
			"Log in to ECR ($ECR_TOKEN is a placeholder, swapped for the real value on send)",
			"ECR_REGISTRY=" + shell.Quote(e.Registry) + "\n" + ecrScript,
		},
	}
	// Image layer downloads are redirected to the region's S3 bucket
	if region != "" {
		f.Allow = []string{"prod-" + region + "-starport-layer-bucket.s3." + region + ".amazonaws.com"}
	}
	return f, nil
}
