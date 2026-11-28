package sandbox

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/nao23/mbx/internal/msb"
	"github.com/nao23/mbx/internal/shell"
)

// DefaultImage is built by mbx from its own Dockerfile; any other image is left to microsandbox to pull.
const DefaultImage = "mbx-default:latest"

//go:embed Dockerfile
var dockerfile string

// labelDockerfile holds the hash of the Dockerfile the image was built from.
const labelDockerfile = "mbx.dockerfile"

// dockerfileHash skips comments and blank lines, so editing comments doesn't trigger a rebuild.
func dockerfileHash() string {
	h := sha256.New()
	for _, line := range strings.Split(dockerfile, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			h.Write([]byte(line + "\n"))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// defaultImageOutdated returns why the default image needs building, or "".
func defaultImageOutdated() string {
	labels, ok := msb.ImageLabels(DefaultImage)
	switch {
	case !ok:
		return DefaultImage + " is missing"
	case labels[labelDockerfile] != dockerfileHash():
		return DefaultImage + " was built from another version of mbx's Dockerfile"
	}
	return ""
}

// Build is an image to build: Image from Dockerfile with Context, or with no Dockerfile,
// DefaultImage from mbx's built-in one. DockerArgs are added to docker build's.
type Build struct {
	Image, Dockerfile, Context string
	DockerArgs                 []string
}

// BuildImage builds an image and loads it into microsandbox; with dryRun, it only shows the commands.
func BuildImage(b Build, dryRun bool) error {
	return buildImage(&msb.Runner{DryRun: dryRun}, b)
}

// buildImage builds the image with the host Docker for the VMs' platform and loads it with docker save | msb
// load. The built-in Dockerfile needs no build context, so it's passed on stdin, labeled with its hash.
func buildImage(r *msb.Runner, b Build) error {
	args := []string{"build", "--platform", msb.VMPlatform()}
	if b.Dockerfile == "" {
		b.Image, b.Context = DefaultImage, "-"
		args = append(args, "--label", labelDockerfile+"="+dockerfileHash())
	} else {
		args = append(args, "-f", b.Dockerfile)
	}
	args = append(append(append(args, "-t", b.Image), b.DockerArgs...), b.Context)
	image := b.Image
	if r.DryRun {
		r.Show("docker", args...)
		if b.Dockerfile == "" {
			fmt.Println("  < (mbx's built-in Dockerfile)")
		}
		fmt.Printf("docker save %s | msb load -t %s\n", shell.Quote(image), shell.Quote(image))
		return nil
	}
	if err := msb.Require("docker"); err != nil {
		return fmt.Errorf("building %s: %w", image, err)
	}
	build := exec.Command("docker", args...)
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if b.Dockerfile == "" {
		build.Stdin = strings.NewReader(dockerfile)
	}
	if err := build.Run(); err != nil {
		return fmt.Errorf("docker build: %w", err)
	}

	save := exec.Command("docker", "save", image)
	load := exec.Command("msb", "load", "-t", image)
	pipe, err := save.StdoutPipe()
	if err != nil {
		return err
	}
	save.Stderr = os.Stderr
	load.Stdin, load.Stdout, load.Stderr = pipe, os.Stdout, os.Stderr
	if err := save.Start(); err != nil {
		return err
	}
	if err := load.Run(); err != nil {
		_ = save.Process.Kill()
		_ = save.Wait()
		return fmt.Errorf("msb load: %w", err)
	}
	if err := save.Wait(); err != nil {
		return fmt.Errorf("docker save: %w", err)
	}
	return nil
}
