package cluster

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// ProvisionLocal is explicitly limited to development endpoints. Production
// physical buckets/policies must be provisioned by the storage administrator.
func ProvisionLocal(ctx context.Context, c Config) error {
	if !c.AllowInsecure || c.Endpoint == "" {
		return errors.New("provision-local requires an explicit insecure development endpoint")
	}
	s, e := connect(ctx, c)
	if e != nil {
		return e
	}
	defer s.Close()
	_, e = s.S3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(c.Bucket)})
	if e != nil {
		var api smithy.APIError
		if !errors.As(e, &api) || api.ErrorCode() != "BucketAlreadyOwnedByYou" {
			return e
		}
	}
	_, e = s.S3.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{Bucket: aws.String(c.Bucket), LifecycleConfiguration: &types.BucketLifecycleConfiguration{Rules: []types.LifecycleRule{{ID: aws.String("abort-incomplete-uploads"), Status: types.ExpirationStatusEnabled, Filter: &types.LifecycleRuleFilter{Prefix: aws.String("")}, AbortIncompleteMultipartUpload: &types.AbortIncompleteMultipartUpload{DaysAfterInitiation: aws.Int32(1)}}}}})
	return e
}
