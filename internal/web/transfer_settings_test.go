package web

import (
	"context"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/transfercfg"
)

func TestAPITransferSettingsSetAndClear(t *testing.T) {
	e := newTestEnvOpts(t, func(cfg *config.Config, opt *Options) {
		cfg.DownloadThreads, cfg.UploadThreads = 4, 4
		cfg.DownloadConnections, cfg.UploadConnections = 4, 4
		r, err := transfercfg.New(context.Background(), opt.Store, transfercfg.Snapshot{
			DownloadThreads: 4, UploadThreads: 4, DownloadConnections: 4, UploadConnections: 4,
		})
		if err != nil {
			t.Fatal(err)
		}
		opt.Transfer = r
	})
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	resp := e.apiPost(j, "/api/v1/settings", csrf, `{"download_threads":8,"upload_connections":16}`)
	if resp.StatusCode != 200 {
		t.Fatalf("set transfer settings: %d %s", resp.StatusCode, bodyOf(t, resp))
	}
	var out struct {
		Settings apiSettingsView `json:"settings"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if out.Settings.DownloadThreads != 8 || out.Settings.UploadConnections != 16 ||
		!out.Settings.DownloadThreadsOverridden || !out.Settings.UploadConnectionsOverridden {
		t.Fatalf("set response: %+v", out.Settings)
	}
	if !e.containsAction("settings.transfer") {
		t.Fatal("transfer change should be audited")
	}

	resp = e.apiPost(j, "/api/v1/settings", csrf, `{"clear_transfer_overrides":["download_threads"]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("clear transfer setting: %d %s", resp.StatusCode, bodyOf(t, resp))
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if out.Settings.DownloadThreads != 4 || out.Settings.DownloadThreadsOverridden {
		t.Fatalf("clear response: %+v", out.Settings)
	}

	resp = e.apiPost(j, "/api/v1/settings", csrf, `{"download_threads":3,"clear_transfer_overrides":["download_threads"]}`)
	if resp.StatusCode != 400 {
		t.Fatalf("set/clear conflict should be 400, got %d", resp.StatusCode)
	}
	bodyOf(t, resp)
	resp = e.apiPost(j, "/api/v1/settings", csrf, `{"clear_transfer_overrides":["unknown"]}`)
	if resp.StatusCode != 400 {
		t.Fatalf("unknown clear key should be 400, got %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}
