package sandbox

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/nao23/msbx/internal/msb"
	"github.com/nao23/msbx/internal/shell"
)

// DefaultImage is built by msbx from its own Dockerfile; any other image is left to microsandbox to pull.
const DefaultImage = "msbx-default:latest"

//go:embed Dockerfile
var dockerfile string

// labelDockerfile holds the hash of the Dockerfile the image was built from.
const labelDockerfile = "msbx.dockerfile"

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
		return DefaultImage + " was built from another version of msbx's Dockerfile"
	}
	return ""
}

func Rebuild() error {
	return rebuild(&msb.Runner{})
}

// rebuild builds the image with the host Docker for arm64 (as microsandbox VMs on Apple Silicon are) and
// loads it with docker save | msb load. The Dockerfile needs no build context, so it's passed on stdin.
func rebuild(r *msb.Runner) error {
	image := DefaultImage
	args := []string{"build", "--platform", "linux/arm64", "--label", labelDockerfile + "=" + dockerfileHash(), "-t", image, "-"}
	if r.DryRun {
		r.Show("docker", args...)
		fmt.Println("  < (msbx's built-in Dockerfile)")
		fmt.Printf("docker save %s | msb load -t %s\n", shell.Quote(image), shell.Quote(image))
		return nil
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("docker is required to build the image")
	}
	build := exec.Command("docker", args...)
	build.Stdin, build.Stdout, build.Stderr = strings.NewReader(dockerfile), os.Stdout, os.Stderr
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
