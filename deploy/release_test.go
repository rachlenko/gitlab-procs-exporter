package deploy

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDebArch(t *testing.T) {
	cases := map[string]struct {
		want    string
		wantErr bool
	}{
		"amd64": {"amd64", false},
		"arm64": {"arm64", false},
		"386":   {"", true},
		"ppc64": {"", true},
	}
	for in, c := range cases {
		got, err := debArch(in)
		if c.wantErr {
			if err == nil {
				t.Errorf("debArch(%q): expected error", in)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("debArch(%q) = %q,%v; want %q,nil", in, got, err, c.want)
		}
	}
}

func TestPickDebAsset(t *testing.T) {
	rel := ghRelease{
		TagName: "v0.0.4",
		Assets: []ghAsset{
			{Name: "gitlab-procs-exporter_0.0.4_linux_amd64.deb", DownloadURL: "https://x/amd64.deb"},
			{Name: "gitlab-procs-exporter_0.0.4_linux_arm64.deb", DownloadURL: "https://x/arm64.deb"},
			{Name: "gitlab-procs-exporter_0.0.4_linux_amd64.rpm", DownloadURL: "https://x/amd64.rpm"},
			{Name: "gitlab-procs-exporter_0.0.4_checksums.txt", DownloadURL: "https://x/sums"},
		},
	}
	a, err := pickDebAsset(rel, "amd64")
	if err != nil {
		t.Fatalf("pickDebAsset amd64: %v", err)
	}
	if a.DownloadURL != "https://x/amd64.deb" {
		t.Errorf("amd64 picked %q", a.DownloadURL)
	}
	if _, err := pickDebAsset(rel, "386"); err == nil {
		t.Error("expected error for unsupported arch")
	}
	empty := ghRelease{TagName: "v9", Assets: []ghAsset{{Name: "other_linux_amd64.deb"}}}
	if _, err := pickDebAsset(empty, "amd64"); err == nil {
		t.Error("expected error when no matching asset prefix")
	}
}

func TestLatestRelease(t *testing.T) {
	orig := httpGet
	t.Cleanup(func() { httpGet = orig })
	const body = `{"tag_name":"v1.2.3","assets":[{"name":"gitlab-procs-exporter_1.2.3_linux_amd64.deb","browser_download_url":"https://x/a.deb"}]}`
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}
	rel, err := latestRelease()
	if err != nil {
		t.Fatalf("latestRelease: %v", err)
	}
	if rel.TagName != "v1.2.3" || len(rel.Assets) != 1 {
		t.Errorf("decoded unexpectedly: %+v", rel)
	}
}
