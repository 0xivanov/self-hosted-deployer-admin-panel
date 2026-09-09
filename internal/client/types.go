package client

import "github.com/0xivanov/self-hosted-deployer-admin-panel/internal/appconfig"

type ServerStatus struct {
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	BuildDate      string `json:"build_date"`
	Ready          bool   `json:"ready"`
	ServerIdentity string `json:"server_identity,omitempty"`
}

type NodeInfo struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Status             string            `json:"status"`
	Labels             map[string]string `json:"labels"`
	CreatedAt          string            `json:"created_at"`
	UpdatedAt          string            `json:"updated_at"`
	LastSeenAt         string            `json:"last_seen_at,omitempty"`
	Hostname           string            `json:"hostname,omitempty"`
	Arch               string            `json:"arch,omitempty"`
	OS                 string            `json:"os,omitempty"`
	Kernel             string            `json:"kernel,omitempty"`
	WireGuardIP        string            `json:"wireguard_ip,omitempty"`
	WireGuardPublicKey string            `json:"wireguard_public_key,omitempty"`
	KubernetesStatus   string            `json:"kubernetes_status,omitempty"`
	KubernetesMessage  string            `json:"kubernetes_message,omitempty"`
	Schedulable        bool              `json:"schedulable"`
	VPNStatus          string            `json:"vpn_status,omitempty"`
}

type AppInfo struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Image        string           `json:"image"`
	Replicas     int              `json:"replicas"`
	Domain       string           `json:"domain"`
	StateMode    string           `json:"state_mode"`
	DesiredState appconfig.Config `json:"desired_state"`
	CreatedAt    string           `json:"created_at"`
	UpdatedAt    string           `json:"updated_at"`
}

type DeploymentInfo struct {
	ID            string `json:"id"`
	AppID         string `json:"app_id"`
	Status        string `json:"status"`
	FailureReason string `json:"failure_reason,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type RouteInfo struct {
	ID         string `json:"id"`
	AppID      string `json:"app_id"`
	Domain     string `json:"domain"`
	TargetPort int    `json:"target_port"`
	Status     string `json:"status"`
	TLSEnabled bool   `json:"tls_enabled"`
}

type DeployResult struct {
	App        AppInfo        `json:"app"`
	Deployment DeploymentInfo `json:"deployment"`
}

type PreflightResult struct {
	DesiredState string   `json:"desired_state"`
	Warnings     []string `json:"warnings"`
}

type AppInspectResult struct {
	App         AppInfo          `json:"app"`
	Deployments []DeploymentInfo `json:"deployments"`
	Routes      []RouteInfo      `json:"routes"`
}

type AppStatusResult struct {
	App               AppInfo             `json:"app"`
	LatestDeployment  DeploymentInfo      `json:"latest_deployment"`
	Routes            []RouteInfo         `json:"routes"`
	RuntimeStatus     string              `json:"runtime_status"`
	DesiredReplicas   int                 `json:"desired_replicas"`
	AvailableReplicas int                 `json:"available_replicas"`
	RunningNodes      []string            `json:"running_nodes"`
	Database          *DatabaseStatusInfo `json:"database,omitempty"`
	Warnings          []string            `json:"warnings"`
}

type DatabaseStatusInfo struct {
	State            string   `json:"state"`
	Phase            string   `json:"phase"`
	DesiredInstances int      `json:"desired_instances"`
	ReadyInstances   int      `json:"ready_instances"`
	Primary          string   `json:"primary"`
	RunningNodes     []string `json:"running_nodes"`
}
