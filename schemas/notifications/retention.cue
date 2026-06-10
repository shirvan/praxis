package notifications

#RetentionPolicy: {
	maxAge:                 string & =~"^[0-9]+(d)$" | *"90d"
	maxEventsPerDeployment: int & >=100 & <=1000000 | *10000
	sweepInterval:          string & =~"^[0-9]+(h|d)$" | *"24h"
}
