package adminui

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/appconfig"
	cli "github.com/0xivanov/self-hosted-deployer-admin-panel/internal/client"
)

// Demo is an in-memory simulation. It never opens a remote connection.
type Demo struct {
	mu       sync.Mutex
	apps     map[string]cli.AppInfo
	history  map[string][]cli.DeploymentInfo
	sequence int
	nodes    []cli.NodeInfo
}

func NewDemo() *Demo {
	d := &Demo{apps: map[string]cli.AppInfo{}, history: map[string][]cli.DeploymentInfo{}}
	_, _ = d.DeployApp(context.Background(), "name: hello-world\nimage: nginx:stable\nservice:\n  port: 80\n  health:\n    path: /\ndeploy:\n  replicas: 2\n")
	return d
}
func (d *Demo) Status(context.Context) (cli.ServerStatus, error) {
	return cli.ServerStatus{Ready: true, Version: "demo", ServerIdentity: "local-demo"}, nil
}
func (d *Demo) ListApps(context.Context) ([]cli.AppInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	apps := []cli.AppInfo{}
	for _, app := range d.apps {
		apps = append(apps, app)
	}
	return apps, nil
}
func (d *Demo) ListNodes(context.Context) ([]cli.NodeInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.nodes == nil {
		d.nodes = []cli.NodeInfo{{ID: "demo-control", Name: "demo-control", Status: "online", Arch: "linux/amd64", KubernetesStatus: "ready", VPNStatus: "connected", Schedulable: true}, {ID: "demo-worker", Name: "demo-worker", Status: "online", Arch: "linux/arm64", KubernetesStatus: "ready", VPNStatus: "connected", Schedulable: true}}
	}
	return append([]cli.NodeInfo{}, d.nodes...), nil
}
func (d *Demo) InspectApp(_ context.Context, name string) (cli.AppInspectResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	app, ok := d.apps[name]
	if !ok {
		return cli.AppInspectResult{}, fmt.Errorf("app %q not found", name)
	}
	return cli.AppInspectResult{App: app, Deployments: append([]cli.DeploymentInfo(nil), d.history[name]...)}, nil
}
func (d *Demo) GetAppStatus(ctx context.Context, name string) (cli.AppStatusResult, error) {
	info, err := d.InspectApp(ctx, name)
	if err != nil {
		return cli.AppStatusResult{}, err
	}
	return cli.AppStatusResult{App: info.App, LatestDeployment: info.Deployments[0], RuntimeStatus: "ready", DesiredReplicas: info.App.Replicas, AvailableReplicas: info.App.Replicas, RunningNodes: []string{"demo-control", "demo-worker"}}, nil
}
func (d *Demo) StreamLogs(ctx context.Context, name string, _ int32, _ bool, receive func(string) error) error {
	if _, err := d.InspectApp(ctx, name); err != nil {
		return err
	}
	return receive("[DEMO] Simulated application log\n[DEMO] Server started; readiness checks passed\n[DEMO] GET / 200\n")
}
func (d *Demo) DeployApp(_ context.Context, data string) (cli.DeployResult, error) {
	cfg, err := appconfig.Parse([]byte(data))
	if err != nil {
		return cli.DeployResult{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sequence++
	now := time.Now().UTC().Format(time.RFC3339)
	app := cli.AppInfo{ID: "demo-" + cfg.Name(), Name: cfg.Name(), Image: cfg.Image(), Replicas: cfg.Replicas(), Domain: cfg.Domain(), DesiredState: cfg, StateMode: cfg.StateMode(), CreatedAt: now, UpdatedAt: now}
	if old, ok := d.apps[cfg.Name()]; ok {
		app.CreatedAt = old.CreatedAt
	}
	deployment := cli.DeploymentInfo{ID: fmt.Sprintf("demo-deployment-%d", d.sequence), AppID: app.ID, Status: "applied", CreatedAt: now, UpdatedAt: now}
	d.apps[cfg.Name()] = app
	d.history[cfg.Name()] = append([]cli.DeploymentInfo{deployment}, d.history[cfg.Name()]...)
	if len(d.history[cfg.Name()]) > 20 {
		d.history[cfg.Name()] = d.history[cfg.Name()][:20]
	}
	return cli.DeployResult{App: app, Deployment: deployment}, nil
}

func (d *Demo) ChangeNode(_ context.Context, id, action string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.nodes {
		if d.nodes[i].ID == id {
			if action == "remove" {
				d.nodes[i].Status = "removed"
				d.nodes[i].Schedulable = false
			} else if action == "purge" {
				d.nodes = append(d.nodes[:i], d.nodes[i+1:]...)
			} else {
				return fmt.Errorf("unsupported action")
			}
			return nil
		}
	}
	return fmt.Errorf("node not found")
}
