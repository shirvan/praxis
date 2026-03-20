package ec2

#AMI: {
    apiVersion: "praxis.io/v1"
    kind:       "AMI"

    metadata: {
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._()/-]{0,127}$"
        labels: [string]: string
    }

    spec: {
        region: string
        description?: string
        source: {
            fromSnapshot?: {
                snapshotId:         string
                architecture:       "x86_64" | "arm64" | *"x86_64"
                virtualizationType: "hvm" | *"hvm"
                rootDeviceName:     string | *"/dev/xvda"
                volumeType:         "gp2" | "gp3" | "io1" | "io2" | *"gp3"
                volumeSize?:        int & >0
                enaSupport?:        bool | *true
            }
            fromAMI?: {
                sourceImageId: string
                sourceRegion?: string
                encrypted?:    bool
                kmsKeyId?:     string
            }
        }
        launchPermissions?: {
            accountIds?: [...string]
            public?:     bool
        }
        deprecation?: {
            deprecateAt: string
        }
        tags: [string]: string
    }

    outputs?: {
        imageId:            string
        name:               string
        state:              string
        architecture:       string
        virtualizationType: string
        rootDeviceName:     string
        ownerId:            string
        creationDate:       string
    }
}