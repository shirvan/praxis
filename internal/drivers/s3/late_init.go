package s3

// LateInitS3Bucket fills in server-defaulted values from the observed bucket
// state without overwriting explicit user input.
func LateInitS3Bucket(spec S3BucketSpec, observed ObservedState) (S3BucketSpec, bool) {
	changed := false

	if spec.Encryption.Enabled && spec.Encryption.Algorithm == "" && observed.EncryptionAlgo != "" {
		spec.Encryption.Algorithm = observed.EncryptionAlgo
		changed = true
	}

	return spec, changed
}
