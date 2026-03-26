package dashboard

import "github.com/shirvan/praxis/pkg/types"

const ServiceName = "Dashboard"

type DashboardSpec struct {
	Account       string `json:"account,omitempty"`
	Region        string `json:"region"`
	DashboardName string `json:"dashboardName"`
	DashboardBody string `json:"dashboardBody"`
	ManagedKey    string `json:"managedKey,omitempty"`
}

type DashboardOutputs struct {
	DashboardArn  string `json:"dashboardArn"`
	DashboardName string `json:"dashboardName"`
}

type ObservedState struct {
	DashboardArn  string `json:"dashboardArn"`
	DashboardName string `json:"dashboardName"`
	DashboardBody string `json:"dashboardBody"`
}

type DashboardState struct {
	Desired            DashboardSpec        `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            DashboardOutputs     `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}

type ValidationMessage struct {
	DataPath string `json:"dataPath,omitempty"`
	Message  string `json:"message"`
}
