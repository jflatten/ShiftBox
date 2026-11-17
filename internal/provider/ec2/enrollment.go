package ec2

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	api "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/jflatten/ShiftBox/internal/provider"
)

const enrollmentSeedLimit = 16 << 10

// DeliverEnrollment writes the short-lived enrollment seed to the verified stopped instance.
func (p *Provider) DeliverEnrollment(ctx context.Context, req provider.EnrollmentRequest) (provider.DeliveryResult, error) {
	if !req.Eligible || req.OperationID == "" || req.Ref.InstanceID == "" || req.MediaPath != "" ||
		len(req.Seed) == 0 || len(req.Seed) > enrollmentSeedLimit {
		return provider.DeliveryResult{}, provider.ErrInvalidState
	}
	observed, err := p.Inspect(ctx, req.Ref)
	if err != nil {
		return provider.DeliveryResult{}, err
	}
	if observed.Power != provider.Stopped || !observed.EC2Enrollment {
		return provider.DeliveryResult{}, provider.ErrInvalidState
	}
	// Identify the attribute explicitly so AWS can enforce the scoped IAM rule.
	_, err = p.client.ModifyInstanceAttribute(ctx, &api.ModifyInstanceAttributeInput{
		InstanceId: aws.String(req.Ref.InstanceID),
		Attribute:  types.InstanceAttributeNameUserData,
		Value:      aws.String(base64.StdEncoding.EncodeToString(req.Seed)),
	})
	if err != nil && !errors.Is(apiError(err), provider.ErrOutcomeUnknown) {
		return provider.DeliveryResult{}, fmt.Errorf("write EC2 enrollment: %w", apiError(err))
	}
	receipt, err := p.enrollmentReceipt(ctx, req.Ref.InstanceID)
	if err != nil {
		return provider.DeliveryResult{}, err
	}
	if receipt != seedReceipt(req.Seed) {
		return provider.DeliveryResult{}, provider.ErrOutcomeUnknown
	}
	return provider.DeliveryResult{Inventory: observed, MediaID: receipt}, nil
}

func (p *Provider) enrollmentReceipt(ctx context.Context, instanceID string) (string, error) {
	readback, err := p.client.DescribeInstanceAttribute(ctx, &api.DescribeInstanceAttributeInput{
		InstanceId: aws.String(instanceID),
		Attribute:  types.InstanceAttributeNameUserData,
	})
	if err != nil {
		return "", fmt.Errorf("verify EC2 enrollment data: %w", apiError(err))
	}
	if id := aws.ToString(readback.InstanceId); id != "" && id != instanceID {
		return "", provider.ErrOwnershipMismatch
	}
	if readback.UserData == nil || len(aws.ToString(readback.UserData.Value)) > base64.StdEncoding.EncodedLen(enrollmentSeedLimit) {
		return "", provider.ErrOutcomeUnknown
	}
	decoded, err := base64.StdEncoding.DecodeString(aws.ToString(readback.UserData.Value))
	defer clear(decoded)
	if err != nil || len(decoded) == 0 || len(decoded) > enrollmentSeedLimit {
		return "", provider.ErrOutcomeUnknown
	}
	return seedReceipt(decoded), nil
}

func seedReceipt(seed []byte) string {
	digest := sha256.Sum256(seed)
	return "ec2-user-data:" + hex.EncodeToString(digest[:])
}
