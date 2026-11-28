package feature

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/nao23/mbx/internal/config"
	"github.com/nao23/mbx/internal/msb"
	"github.com/nao23/mbx/internal/shell"
	"github.com/nao23/mbx/internal/ui"
)

//go:embed emulation.sh
var emulationScript string

//go:embed docker.sh
var dockerScript string

func newEmulation(cfg *config.Config) (*Feature, error) {
	if !cfg.Docker.Emulation {
		return nil, nil
	}
	return &Feature{Name: "docker.emulation", Setup: &Setup{"Run binaries for other architectures (amd64, arm64, etc.) under QEMU; native ones are unaffected", emulationScript}}, nil
}

// newDocker gives Docker an ext4 disk, as it can't create containers on the overlay root filesystem. With
// host_images, it copies them from the host's Docker after start (see hostImagesTask).
func newDocker(cfg *config.Config) (*Feature, error) {
	if !cfg.Docker.Enabled {
		if len(cfg.Docker.HostImages) > 0 {
			return nil, errors.New("docker.host_images requires docker (docker.enabled)")
		}
		return nil, nil
	}
	f := &Feature{
		Name: "docker",
		Flags: []msb.Flag{{
			Name:  "--mount-owned",
			Value: "/var/lib/docker:kind=disk,size=" + cfg.Docker.Disk,
			Note:  "ext4 disk for Docker (removed with the sandbox)",
		}},
		Setup: &Setup{"Start Docker Engine, relaying container DNS through dnsmasq; containers and builds trust microsandbox's CA", dockerScript},
	}
	if refs := cfg.Docker.HostImages; len(refs) > 0 {
		f.PostStart, f.Prompt = hostImagesTask(refs), hostImagesPrompt
	}
	return f, nil
}

// hostImagesFile in the VM lists docker.host_images and how far their copy from the host has got.
const hostImagesFile = "/run/mbx/host-images"

// hostImagesTask copies docker.host_images from the host's Docker to the VM's in the background,
// as the copy can take a while and the agent rarely needs Docker right away.
// So the VM needs no login to their registries.
func hostImagesTask(refs []string) *HostTask {
	return &HostTask{
		Title:  "Copy images from the host's Docker",
		Detail: "In the background while the agent runs, skipping those already in the VM. Progress goes to " + hostImagesFile + " in the VM",
		Prepare: func(r *msb.Runner, name string) (func(ctx context.Context), error) {
			return prepareHostImages(r, name, refs)
		},
	}
}

// hostImagesPrompt is the line about the copy in the sandbox prompt given to claude.
const hostImagesPrompt = "- Some Docker images are copied from the host's Docker in the background after start, " +
	"as the VM may not be able to pull them from their registries. " +
	"`" + hostImagesFile + "` lists them and how far the copy has got. " +
	"If one is missing, check that file and wait for its copy instead of pulling it; " +
	"if its copy failed, tell the user what the file says. " +
	"Avoid pulling them (`docker compose pull`, `--pull always`), which may fail or replace the copy"

type hostImage struct {
	ref      string
	platform string // the platform copied: the first in hostImagePlatforms the host has
	id       string // the image's ID for that platform, which the VM's Docker gives it once loaded
	state    string // pending, copying, copied, present (already in the VM), failed or missing (not on the host)
	detail   string
}

// hostImagePlatforms is the VM's platform, then the other one, which runs under emulation.
func hostImagePlatforms() []string {
	p, other := msb.VMPlatform(), "linux/amd64"
	if p == other {
		other = "linux/arm64"
	}
	return []string{p, other}
}

// findHostImages looks the images up in the host's Docker. A multi-platform image may hold content for only
// some of its platforms, and inspect leaves Os and Architecture empty for those without it.
func findHostImages(refs []string) []*hostImage {
	var images []*hostImage
	for _, ref := range refs {
		img := &hostImage{ref: ref, state: "missing", detail: "not in the host's Docker for " + strings.Join(hostImagePlatforms(), " or ")}
		for _, p := range hostImagePlatforms() {
			out, err := exec.Command("docker", "image", "inspect", "--platform", p, "-f", "{{.Os}}/{{.Architecture}} {{.Id}}", ref).Output()
			if f := strings.Fields(string(out)); err == nil && len(f) == 2 && f[0] == p {
				img.platform, img.id, img.state, img.detail = p, f[1], "pending", ""
				break
			}
		}
		images = append(images, img)
	}
	return images
}

// prepareHostImages finds the images on the host and shows them, and returns the copy to run.
func prepareHostImages(r *msb.Runner, name string, refs []string) (func(ctx context.Context), error) {
	if err := msb.Require("docker"); err != nil {
		return nil, err
	}
	images := findHostImages(refs)
	for _, img := range images {
		switch {
		case r.DryRun && img.state == "missing":
			fmt.Printf("# %s: %s\n", img.ref, img.detail)
		case r.DryRun:
			fmt.Println(shell.Join(saveArgs(img)) + " | " + shell.Join(append([]string{"msb"}, loadArgs(name)...)))
		case img.state == "missing":
			ui.Item(ui.Err.Failed(), img.ref, img.detail)
		default:
			ui.Item(ui.Err.Pending(), img.ref, img.platform)
		}
	}
	if r.DryRun {
		return nil, nil
	}
	return func(ctx context.Context) { copyHostImages(ctx, name, images) }, nil
}

func saveArgs(img *hostImage) []string {
	return []string{"docker", "save", "--platform", img.platform, img.ref}
}

func loadArgs(name string) []string {
	return []string{"exec", "--stream", name, "--", "docker", "load", "--quiet"}
}

// copyHostImages copies the images one at a time, keeping hostImagesFile up to date.
func copyHostImages(ctx context.Context, name string, images []*hostImage) {
	writeHostImagesFile(ctx, name, images, false)
	for _, img := range images {
		if img.state != "pending" {
			continue
		}
		if vmImageID(ctx, name, img.ref) == img.id {
			img.state = "present"
		} else {
			img.state = "copying"
			writeHostImagesFile(ctx, name, images, false)
			if err := copyHostImage(ctx, name, img); err != nil {
				img.state, img.detail = "failed", err.Error()
			} else {
				img.state = "copied"
			}
		}
		if ctx.Err() != nil {
			return
		}
		writeHostImagesFile(ctx, name, images, false)
	}
	writeHostImagesFile(ctx, name, images, true)
}

func vmImageID(ctx context.Context, name, ref string) string {
	out, _ := exec.CommandContext(ctx, "msb", "exec", name, "--", "docker", "image", "inspect", "-f", "{{.Id}}", ref).Output()
	return strings.TrimSpace(string(out))
}

// copyHostImage runs docker save | msb exec ... docker load.
func copyHostImage(ctx context.Context, name string, img *hostImage) error {
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	var saveOut, loadOut bytes.Buffer
	save := exec.CommandContext(ctx, saveArgs(img)[0], saveArgs(img)[1:]...)
	save.Stdout, save.Stderr = pw, &saveOut
	load := exec.CommandContext(ctx, "msb", loadArgs(name)...)
	load.Stdin, load.Stdout, load.Stderr = pr, &loadOut, &loadOut
	err = save.Start()
	if err == nil {
		if err = load.Start(); err != nil {
			_ = save.Process.Kill()
			_ = save.Wait()
		}
	}
	// Only the children hold the pipe now, so either side exiting ends the other
	pw.Close()
	pr.Close()
	if err != nil {
		return err
	}
	loadErr, saveErr := load.Wait(), save.Wait()
	switch {
	case saveErr != nil:
		return fmt.Errorf("docker save: %s", firstLine(saveOut.String(), saveErr))
	case loadErr != nil:
		return fmt.Errorf("docker load: %s", firstLine(loadOut.String(), loadErr))
	}
	return nil
}

func firstLine(out string, err error) string {
	if line, _, _ := strings.Cut(strings.TrimSpace(out), "\n"); line != "" {
		return line
	}
	return err.Error()
}

// writeHostImagesFile replaces hostImagesFile in the VM, so a reader never sees it half written.
func writeHostImagesFile(ctx context.Context, name string, images []*hostImage, finished bool) {
	var b strings.Builder
	b.WriteString("# Images copied from the host's Docker (docker.host_images in mbx's config), updated as the copy goes\n")
	for _, img := range images {
		fmt.Fprintf(&b, "%-8s %s", img.state, img.ref)
		if img.platform != "" {
			b.WriteString("  " + img.platform)
		}
		if img.detail != "" {
			b.WriteString("  " + img.detail)
		}
		b.WriteString("\n")
	}
	if finished {
		b.WriteString("# Finished\n")
	}
	cmd := exec.CommandContext(
		ctx,
		"msb", "exec", "--stream", name, "--", "sh", "-c",
		"mkdir -p /run/mbx && cat >"+hostImagesFile+".tmp && mv "+hostImagesFile+".tmp "+hostImagesFile,
	)
	cmd.Stdin = strings.NewReader(b.String())
	_ = cmd.Run()
}
