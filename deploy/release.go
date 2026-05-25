package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// releaseAPIURL returns the GitHub "latest release" API endpoint for the repo.
func releaseAPIURL() string {
	repo := strings.TrimPrefix(Module, "github.com/")
	return "https://api.github.com/repos/" + repo + "/releases/latest"
}

// ghRelease is the subset of the GitHub release API we consume.
type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
}

// httpGet performs the release-API GET; it is a package variable so tests can
// stub it without touching the network.
var httpGet = func(url string) (*http.Response, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	return client.Do(req)
}

// latestRelease fetches and decodes the latest release metadata.
func latestRelease() (ghRelease, error) {
	resp, err := httpGet(releaseAPIURL())
	if err != nil {
		return ghRelease{}, fmt.Errorf("querying GitHub releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ghRelease{}, fmt.Errorf("GitHub releases API returned %s", resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return ghRelease{}, fmt.Errorf("decoding release JSON: %w", err)
	}
	if rel.TagName == "" {
		return ghRelease{}, fmt.Errorf("release JSON had empty tag_name")
	}
	return rel, nil
}

// debArch maps a Go GOARCH value to the Debian package architecture used in
// the release asset names.
func debArch(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "amd64", nil
	case "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("no .deb asset for architecture %q", goarch)
	}
}

// pickDebAsset selects the .deb asset matching the host architecture. It
// matches by prefix and suffix to avoid depending on the version segment (the
// tag is "v0.0.4" while the asset embeds "0.0.4").
func pickDebAsset(rel ghRelease, goarch string) (ghAsset, error) {
	arch, err := debArch(goarch)
	if err != nil {
		return ghAsset{}, err
	}
	prefix := binaryName + "_"
	suffix := "_linux_" + arch + ".deb"
	for _, a := range rel.Assets {
		if strings.HasPrefix(a.Name, prefix) && strings.HasSuffix(a.Name, suffix) {
			return a, nil
		}
	}
	return ghAsset{}, fmt.Errorf("no asset matching %s*%s in release %s", prefix, suffix, rel.TagName)
}

// downloadFile fetches url into a new temp file using curl or wget and returns
// its path. The caller is responsible for removing the file.
func downloadFile(w io.Writer, url string) (string, error) {
	f, err := os.CreateTemp("", binaryName+"-*.deb")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	path := f.Name()
	_ = f.Close()

	var cmd *exec.Cmd
	switch {
	case lookPath("curl") != "":
		cmd = exec.CommandContext(context.Background(), "curl", "-fsSL", "-o", path, url)
	case lookPath("wget") != "":
		cmd = exec.CommandContext(context.Background(), "wget", "-q", "-O", path, url)
	default:
		_ = os.Remove(path)
		return "", fmt.Errorf("neither curl nor wget found; run --check-dependencies")
	}
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Run(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("downloading %s: %w", url, err)
	}
	return path, nil
}

func lookPath(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

// UpdateService updates the exporter to the latest GitHub release: it
// downloads the matching .deb, installs it with dpkg (replacing the
// dpkg-managed binary in /usr/bin), and restarts the systemd service so the
// new binary takes effect. Requires Linux and root.
func UpdateService(w io.Writer, cfg ServiceConfig) error {
	cfg.setDefaults()
	logf := func(format string, a ...any) { fmt.Fprintf(w, "==> "+format+"\n", a...) }

	if runtime.GOOS != "linux" {
		return fmt.Errorf("update is only supported on Linux (this host is %s)", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (try: sudo %s --update)", binaryName)
	}
	dpkg, err := exec.LookPath("dpkg")
	if err != nil {
		return fmt.Errorf("dpkg not found; this targets Debian-based systems: %w", err)
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return fmt.Errorf("systemctl not found; this targets systemd-based systems: %w", err)
	}

	logf("querying latest release")
	rel, err := latestRelease()
	if err != nil {
		return err
	}
	asset, err := pickDebAsset(rel, runtime.GOARCH)
	if err != nil {
		return err
	}
	logf("latest release is %s; asset %s", rel.TagName, asset.Name)

	deb, err := downloadFile(w, asset.DownloadURL)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(deb) }()

	logf("installing %s with dpkg", asset.Name)
	if err := runCmd(w, dpkg, "-i", deb); err != nil {
		return fmt.Errorf("dpkg -i failed: %w", err)
	}

	logf("restarting %s", cfg.ServiceName)
	if err := runCmd(w, systemctl, "restart", cfg.ServiceName+".service"); err != nil {
		return fmt.Errorf("systemctl restart: %w", err)
	}
	_ = runCmd(w, systemctl, "--no-pager", "--full", "status", cfg.ServiceName+".service")

	fmt.Fprintf(w, "\nDone. %s updated to %s (%s) and restarted.\n",
		cfg.ServiceName, rel.TagName, binaryVersion("/usr/bin/"+binaryName))
	return nil
}

// dpkgInstalled reports whether pkg is currently installed (Status "install
// ok installed"), as opposed to absent or in the config-files state left
// behind by a prior removal.
func dpkgInstalled(dpkg, pkg string) bool {
	out, err := exec.CommandContext(context.Background(), dpkg, "-s", pkg).Output()
	if err != nil {
		return false
	}
	return parseDpkgInstalled(string(out))
}

// parseDpkgInstalled checks `dpkg -s` output for the installed status line.
func parseDpkgInstalled(statusOutput string) bool {
	return strings.Contains(statusOutput, "Status: install ok installed")
}

// binaryVersion reports the --version output of the binary at path, or
// "unknown" if it cannot be run.
func binaryVersion(path string) string {
	out, err := exec.CommandContext(context.Background(), path, "--version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}
