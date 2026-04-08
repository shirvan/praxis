package keypair

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// KeyPairAPI abstracts AWS EC2 Key Pair operations for testability.
type KeyPairAPI interface {
	// CreateKeyPair asks AWS to generate a new key pair; returns the private key material exactly once.
	CreateKeyPair(ctx context.Context, name, keyType string, tags map[string]string) (keyPairID, fingerprint, privateKey string, err error)
	// ImportKeyPair uploads a user-provided public key; no private key is returned.
	ImportKeyPair(ctx context.Context, name, publicKeyMaterial string, tags map[string]string) (keyPairID, fingerprint string, err error)
	// DescribeKeyPair fetches the current state of a key pair by name.
	DescribeKeyPair(ctx context.Context, keyName string) (ObservedState, error)
	// DeleteKeyPair removes the key pair from EC2.
	DeleteKeyPair(ctx context.Context, keyName string) error
	// UpdateTags performs a delete-then-create sync of user tags on the key pair.
	UpdateTags(ctx context.Context, keyPairID string, tags map[string]string) error
}

// realKeyPairAPI is the production implementation backed by the EC2 SDK client.
type realKeyPairAPI struct {
	client  *ec2sdk.Client
	limiter *ratelimit.Limiter
}

// NewKeyPairAPI creates a production KeyPairAPI with rate limiting (20 tokens/s, burst 10).
func NewKeyPairAPI(client *ec2sdk.Client) KeyPairAPI {
	return &realKeyPairAPI{
		client:  client,
		limiter: ratelimit.New("key-pair", 20, 10),
	}
}

// CreateKeyPair calls EC2 CreateKeyPair to generate a new key pair.
// AWS generates both halves; the private key material is returned exactly once.
// Tags are applied atomically via TagSpecifications.
func (r *realKeyPairAPI) CreateKeyPair(ctx context.Context, name, keyType string, tags map[string]string) (string, string, string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", "", "", err
	}

	input := &ec2sdk.CreateKeyPairInput{
		KeyName: aws.String(name),
		KeyType: ec2types.KeyType(keyType),
	}
	if ec2Tags := toEC2Tags(tags); len(ec2Tags) > 0 {
		input.TagSpecifications = []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeKeyPair,
			Tags:         ec2Tags,
		}}
	}

	out, err := r.client.CreateKeyPair(ctx, input)
	if err != nil {
		return "", "", "", err
	}
	return aws.ToString(out.KeyPairId), aws.ToString(out.KeyFingerprint), aws.ToString(out.KeyMaterial), nil
}

// ImportKeyPair uploads a user-provided public key to AWS.
// No private key is returned since the user already possesses it.
func (r *realKeyPairAPI) ImportKeyPair(ctx context.Context, name, publicKeyMaterial string, tags map[string]string) (string, string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", "", err
	}

	input := &ec2sdk.ImportKeyPairInput{
		KeyName:           aws.String(name),
		PublicKeyMaterial: []byte(publicKeyMaterial),
	}
	if ec2Tags := toEC2Tags(tags); len(ec2Tags) > 0 {
		input.TagSpecifications = []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeKeyPair,
			Tags:         ec2Tags,
		}}
	}

	out, err := r.client.ImportKeyPair(ctx, input)
	if err != nil {
		return "", "", err
	}
	return aws.ToString(out.KeyPairId), aws.ToString(out.KeyFingerprint), nil
}

// DescribeKeyPair fetches key pair metadata by name via EC2 DescribeKeyPairs.
// Returns ObservedState with all tags converted from EC2 format.
func (r *realKeyPairAPI) DescribeKeyPair(ctx context.Context, keyName string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}

	out, err := r.client.DescribeKeyPairs(ctx, &ec2sdk.DescribeKeyPairsInput{
		KeyNames: []string{keyName},
	})
	if err != nil {
		return ObservedState{}, err
	}
	if len(out.KeyPairs) == 0 {
		return ObservedState{}, awserr.NotFound(fmt.Sprintf("key pair %q not found", keyName))
	}
	kp := out.KeyPairs[0]

	obs := ObservedState{
		KeyName:        aws.ToString(kp.KeyName),
		KeyPairId:      aws.ToString(kp.KeyPairId),
		KeyFingerprint: aws.ToString(kp.KeyFingerprint),
		KeyType:        string(kp.KeyType),
		Tags:           make(map[string]string, len(kp.Tags)),
	}
	for _, tag := range kp.Tags {
		obs.Tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return obs, nil
}

// DeleteKeyPair removes the key pair by name from EC2.
func (r *realKeyPairAPI) DeleteKeyPair(ctx context.Context, keyName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteKeyPair(ctx, &ec2sdk.DeleteKeyPairInput{KeyName: aws.String(keyName)})
	return err
}

// UpdateTags synchronizes tags on an existing key pair using delete-then-create.
// Steps:
//  1. Describe the current tags on the key pair.
//  2. Delete all non-praxis: tags (praxis: tags are system-managed).
//  3. Create the desired user tags.
func (r *realKeyPairAPI) UpdateTags(ctx context.Context, keyPairID string, tags map[string]string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	out, err := r.client.DescribeKeyPairs(ctx, &ec2sdk.DescribeKeyPairsInput{KeyPairIds: []string{keyPairID}})
	if err != nil {
		return err
	}
	if len(out.KeyPairs) > 0 {
		var oldTags []ec2types.Tag
		for _, tag := range out.KeyPairs[0].Tags {
			key := aws.ToString(tag.Key)
			if strings.HasPrefix(key, "praxis:") {
				continue
			}
			oldTags = append(oldTags, ec2types.Tag{Key: tag.Key})
		}
		if len(oldTags) > 0 {
			if err := r.limiter.Wait(ctx); err != nil {
				return err
			}
			_, err = r.client.DeleteTags(ctx, &ec2sdk.DeleteTagsInput{
				Resources: []string{keyPairID},
				Tags:      oldTags,
			})
			if err != nil {
				return err
			}
		}
	}

	ec2Tags := toEC2Tags(tags)
	if len(ec2Tags) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err = r.client.CreateTags(ctx, &ec2sdk.CreateTagsInput{Resources: []string{keyPairID}, Tags: ec2Tags})
	return err
}

// toEC2Tags converts a map to EC2 Tag slice, filtering out praxis: namespace tags.
func toEC2Tags(tags map[string]string) []ec2types.Tag {
	if len(tags) == 0 {
		return nil
	}
	ec2Tags := make([]ec2types.Tag, 0, len(tags))
	for key, value := range tags {
		if strings.HasPrefix(key, "praxis:") {
			continue
		}
		ec2Tags = append(ec2Tags, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	return ec2Tags
}

// IsNotFound returns true if the error is an InvalidKeyPair.NotFound API error.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "InvalidKeyPair.NotFound") || awserr.IsNotFoundErr(err)
}

// IsDuplicate returns true if a key pair with the same name already exists.
func IsDuplicate(err error) bool {
	return awserr.HasCode(err, "InvalidKeyPair.Duplicate")
}

// IsInvalidKeyFormat returns true if the public key material is malformed.
func IsInvalidKeyFormat(err error) bool {
	return awserr.HasCode(err, "InvalidKey.Format", "InvalidKeyPair.Format")
}
