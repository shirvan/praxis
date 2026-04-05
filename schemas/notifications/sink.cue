#SinkFilter: {
	types?: [...string]
	categories?: [...("lifecycle" | "drift" | "policy" | "command" | "system")]
	severities?: [...("info" | "warn" | "error")]
	workspaces?: [...string]
	deployments?: [...string]
}

#RetryPolicy: {
	maxAttempts: int & >=1 & <=10 | *3
	backoffMs:   int & >=100 & <=60000 | *1000
}

#WebhookSink: {
	name: string & =~"^[a-zA-Z0-9_-]{1,64}$"
	type: "webhook"
	url:  string & =~"^(https://.+|http://(localhost|127\\.0\\.0\\.1)(:[0-9]+)?(/.*)?)$"
	filter: #SinkFilter | *{}
	headers?: [string]: string
	retry: #RetryPolicy | *{maxAttempts: 3, backoffMs: 1000}
	circuitOpenDurationSec?: int & >=10 & <=3600
}

#StructuredLogSink: {
	name: string & =~"^[a-zA-Z0-9_-]{1,64}$"
	type: "structured_log"
	filter: #SinkFilter | *{}
}

#CloudEventsHTTPSink: {
	name: string & =~"^[a-zA-Z0-9_-]{1,64}$"
	type: "cloudevents_http"
	url:  string & =~"^(https://.+|http://(localhost|127\\.0\\.0\\.1)(:[0-9]+)?(/.*)?)$"
	filter: #SinkFilter | *{}
	contentMode: "structured" | "binary" | *"structured"
	retry: #RetryPolicy | *{maxAttempts: 3, backoffMs: 1000}
	circuitOpenDurationSec?: int & >=10 & <=3600
}

#RestateRPCSink: {
	name:    string & =~"^[a-zA-Z0-9_-]{1,64}$"
	type:    "restate_rpc"
	target:  string & =~"^[a-zA-Z0-9_-]+$"
	handler: string & =~"^[a-zA-Z0-9_-]+$"
	filter: #SinkFilter | *{}
	circuitOpenDurationSec?: int & >=10 & <=3600
}

#NotificationSink: #WebhookSink | #StructuredLogSink | #CloudEventsHTTPSink | #RestateRPCSink
