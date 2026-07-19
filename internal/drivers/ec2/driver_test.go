package ec2

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServiceName(t *testing.T) {
	drv := NewGenericEC2InstanceDriver(nil)
	assert.Equal(t, "EC2Instance", drv.ServiceName())
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		ImageId:            "ami-0123456789abcdef0",
		InstanceType:       "t3.micro",
		KeyName:            "default",
		SubnetId:           "subnet-1",
		SecurityGroupIds:   []string{"sg-1", "sg-2"},
		IamInstanceProfile: "profile",
		Monitoring:         true,
		Tags:               map[string]string{"Name": "web", "praxis:managed-key": "us-east-1~web"},
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.ImageId, spec.ImageId)
	assert.Equal(t, obs.InstanceType, spec.InstanceType)
	assert.Equal(t, obs.KeyName, spec.KeyName)
	assert.Equal(t, obs.SubnetId, spec.SubnetId)
	assert.Equal(t, obs.SecurityGroupIds, spec.SecurityGroupIds)
	assert.Equal(t, obs.IamInstanceProfile, spec.IamInstanceProfile)
	assert.Equal(t, obs.Monitoring, spec.Monitoring)
	assert.Equal(t, map[string]string{"Name": "web"}, spec.Tags)
	assert.Nil(t, spec.RootVolume)
}
