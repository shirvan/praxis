package keypair

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"github.com/praxiscloud/praxis/internal/infra/ratelimit"
)

type KeyPairAPI interface {
	CreateKeyPair(ctx context.Context, name, keyType string, tags map[string]string) (keyPairID, fingerprint, privateKey string, err error)
	ImportKeyPair(ctx context.Context, name, publicKeyMaterial string, tags map[string]string) (keyPairID, fingerprint string, err error)
	DescribeKeyPair(ctx context.Context, keyName string) (ObservedState, error)
	DeleteKeyPair(ctx context.Context, keyName string) error
	UpdateTags(ctx context.Context, keyPairID string, tags map[string]string) error
}

type realKeyPairAPI struct {
	client  *ec2sdk.Client
	limiter *ratelimit.Limiter
}

func NewKeyPairAPI(client *ec2sdk.Client) KeyPairAPI {
	return &realKeyPairAPI{
		client:  client,
		limiter: ratelimit.New("key-pair", 20, 10),
	}
}

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
		return ObservedState{}, fmt.Errorf("key pair %q not found", keyName)
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

func (r *realKeyPairAPI) DeleteKeyPair(ctx context.Context, keyName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteKeyPair(ctx, &ec2sdk.DeleteKeyPairInput{KeyName: aws.String(keyName)})
	return err
}

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

func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "InvalidKeyPair.NotFound"
	}
	errText := err.Error()
	return strings.Contains(errText, "InvalidKeyPair.NotFound")
}

func IsDuplicate(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "InvalidKeyPair.Duplicate"
	}
	errText := err.Error()
	return strings.Contains(errText, "InvalidKeyPair.Duplicate")
}

func IsInvalidKeyFormat(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		return code == "InvalidKey.Format" || code == "InvalidKeyPair.Format"
	}
	errText := err.Error()
	return strings.Contains(errText, "InvalidKey.Format") || strings.Contains(errText, "InvalidKeyPair.Format")
}
