package cloudwatch

#MetricAlarm: {
    apiVersion: "praxis.io/v1"
    kind:       "MetricAlarm"

    metadata: {
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9_\\-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        region: string
        namespace: string
        metricName: string
        dimensions?: [string]: string
        statistic?: "SampleCount" | "Average" | "Sum" | "Minimum" | "Maximum"
        extendedStatistic?: string & =~"^p[0-9]+(\\.[0-9]+)?$"
        period: int & >0
        evaluationPeriods: int & >=1
        datapointsToAlarm?: int & >=1
        threshold: number
        comparisonOperator: "GreaterThanThreshold" |
            "GreaterThanOrEqualToThreshold" |
            "LessThanThreshold" |
            "LessThanOrEqualToThreshold"
        treatMissingData: "breaching" | "notBreaching" | "ignore" | "missing" | *"missing"
        alarmDescription?: string
        actionsEnabled: bool | *true
        alarmActions?: [...string]
        okActions?: [...string]
        insufficientDataActions?: [...string]
        unit?: string
        tags: [string]: string
    }

    outputs?: {
        alarmArn: string
        alarmName: string
        stateValue: string
        stateReason?: string
    }
}