package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// deployLoop wakes a sleeping VM when a new build of the hub waits to be installed, if the
// owner asked for that (`deploy.wake_to_deploy`). The hub starts its updater when the VM
// wakes; the VM goes back to sleep by itself when it is idle again.
func (g *Gateway) deployLoop(ctx context.Context) {
	ticker := time.NewTicker(g.t.DeployPoll)
	defer ticker.Stop()
	client := &http.Client{Timeout: 20 * time.Second}
	var wokeFor int64 // a deployment gets one wake: one that cannot be installed is not retried all night
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s := g.settings()
		if s == nil || !s.host.Deploy.WakeToDeploy || s.host.Deploy.Repo == nil {
			continue
		}
		if v := g.view.Load(); v.running || g.vmState() != "stopped" {
			continue
		}
		id, err := g.queuedDeployment(ctx, client, *s.host.Deploy.Repo, s.host.Deploy.HubEnvironment)
		if err != nil {
			g.log.Warn("deployments not checked", "error", err)
			continue
		}
		if id != 0 && id != wokeFor {
			wokeFor = id
			g.log.Info("waking the VM to install a new build of the hub", "deployment", id)
			g.RequestWake("deploy")
		}
	}
}

// vmState is the state of the VM as last seen.
func (g *Gateway) vmState() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return string(g.vm)
}

// queuedDeployment returns the id of the latest deployment of the hub when it still waits to
// be installed, and 0 when it does not (or there is none).
func (g *Gateway) queuedDeployment(ctx context.Context, client *http.Client, repo, environment string) (int64, error) {
	var deployments []struct {
		ID int64 `json:"id"`
	}
	query := url.Values{"environment": {environment}, "per_page": {"1"}}
	if err := g.github(ctx, client, "/repos/"+repo+"/deployments?"+query.Encode(), &deployments); err != nil || len(deployments) == 0 {
		return 0, err
	}
	var statuses []struct {
		State string `json:"state"`
	}
	path := fmt.Sprintf("/repos/%s/deployments/%d/statuses?per_page=1", repo, deployments[0].ID)
	if err := g.github(ctx, client, path, &statuses); err != nil {
		return 0, err
	}
	// The deploy workflow leaves a deployment queued; the updater moves it on.
	if len(statuses) == 0 || statuses[0].State == "queued" {
		return deployments[0].ID, nil
	}
	return 0, nil
}

func (g *Gateway) github(ctx context.Context, client *http.Client, path string, out any) error {
	api := g.opt.GitHubAPI
	if api == "" {
		api = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.opt.GitHubToken != "" {
		req.Header.Set("Authorization", "Bearer "+g.opt.GitHubToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub answers HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(data, out)
}
