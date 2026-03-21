package ec2

#KeyPair: {
    apiVersion: "praxis.io/v1"
    kind:       "KeyPair"

    metadata: {
        name: string & =~"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$"
        labels: [string]: string
    }

    spec: {
        region: string
        keyType: "rsa" | "ed25519" | *"ed25519"
        publicKeyMaterial?: string
        tags: [string]: string
    }

    outputs?: {
        keyName:            string
        keyPairId:          string
        keyFingerprint:     string
        keyType:            string
        privateKeyMaterial?: string
    }
}