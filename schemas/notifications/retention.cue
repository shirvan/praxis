#RetentionPolicy: {
    maxAge:                 string & =~"^[0-9]+(d)$" | *"90d"
    maxEventsPerDeployment: int & >=100 & <=1000000 | *10000
    maxIndexEntries:        int & >=1000 & <=10000000 | *100000
    sweepInterval:          string & =~"^[0-9]+(h|d)$" | *"24h"
    shipBeforeDelete:       bool | *false
    drainSink?:             string

    if shipBeforeDelete == true {
        drainSink: string & !=""
    }
}