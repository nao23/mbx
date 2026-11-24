package feature

import (
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"github.com/nao23/msbx/internal/config"
	"github.com/nao23/msbx/internal/secret"
	"github.com/nao23/msbx/internal/shell"
)

//go:embed ecr.sh
var ecrScript string

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
	region, source := ecrRegion(e.Registry), e.SecretSource
	if source == (secret.Source{}) {
		if region == "" {
			return nil, fmt.Errorf("can't tell the region of %q; set docker.ecr.secret_source", e.Registry)
		}
		source = secret.Source{Command: "aws ecr get-login-password --region " + region}
	}
	f := &Feature{
		Name:   "docker.ecr",
		Secret: &Secret{Env: "ECR_TOKEN", Host: e.Registry, Source: source},
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

// ecrRegion returns the region of <account>.dkr.ecr.<region>.amazonaws.com, or ""
func ecrRegion(registry string) string {
	parts := strings.Split(registry, ".")
	if len(parts) < 4 || parts[1] != "dkr" || parts[2] != "ecr" {
		return ""
	}
	return parts[3]
}
