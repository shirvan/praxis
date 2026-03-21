package ebs

#EBSVolume: {
    apiVersion: "praxis.io/v1"
    kind:       "EBSVolume"

    metadata: {
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        region: string
        availabilityZone: string
        volumeType: "gp2" | "gp3" | "io1" | "io2" | "st1" | "sc1" | *"gp3"
        sizeGiB: int & >=1 & <=16384 | *20
        iops?: int & >=100
        throughput?: int & >=125 & <=1000
        encrypted: bool | *true
        kmsKeyId?: string
        snapshotId?: string & =~"^snap-[a-f0-9]{8,17}$"
        tags: [string]: string
    }

    outputs?: {
        volumeId:         string
        arn?:             string
        availabilityZone: string
        state:            string
        sizeGiB:          int
        volumeType:       string
        encrypted:        bool
    }
}