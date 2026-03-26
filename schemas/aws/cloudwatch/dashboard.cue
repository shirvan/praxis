package cloudwatch

#Dashboard: {
    apiVersion: "praxis.io/v1"
    kind:       "Dashboard"

    metadata: {
        name: string & =~"^[a-zA-Z0-9_\\-]{1,255}$"
        labels: [string]: string
    }

    spec: {
        region: string
        dashboardBody: string
    }

    outputs?: {
        dashboardArn: string
        dashboardName: string
    }
}