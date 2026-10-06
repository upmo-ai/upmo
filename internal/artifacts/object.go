package artifacts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Object reads artifact bytes from an S3-compatible bucket.
type Object struct {
	Bucket string
	get    func(ctx context.Context, key string) (io.ReadCloser, error)
}

// OpenObject builds an Object client from the AWS SDK default credential chain.
// endpoint, when set, is a path-style S3-compatible base URL (for example MinIO).
func OpenObject(ctx context.Context, bucket, region, endpoint string) (Object, error) {
	bucket = strings.TrimSpace(bucket)
	if bucket == "" {
		return Object{}, fmt.Errorf("artifacts: object: bucket is required")
	}
	region = strings.TrimSpace(region)
	if region == "" {
		region = "us-east-1"
	}
	endpoint = strings.TrimSpace(endpoint)
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return Object{}, fmt.Errorf("artifacts: object: aws config: %w", err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		}
	})
	return Object{
		Bucket: bucket,
		get: func(ctx context.Context, key string) (io.ReadCloser, error) {
			out, err := client.GetObject(ctx, &s3.GetObjectInput{
				Bucket: aws.String(bucket),
				Key:    aws.String(key),
			})
			if err != nil {
				var nsk *types.NoSuchKey
				var nsb *types.NoSuchBucket
				if errors.As(err, &nsk) || errors.As(err, &nsb) {
					return nil, ErrNoBlob
				}
				// Some S3-compatible servers return NotFound without typed errors.
				if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "NoSuchKey") {
					return nil, ErrNoBlob
				}
				return nil, fmt.Errorf("artifacts: object: get: %w", err)
			}
			return out.Body, nil
		},
	}, nil
}

// Open returns a reader for an object key in Bucket.
func (o Object) Open(ctx context.Context, store, key string) (io.ReadCloser, error) {
	if store != StoreObject {
		return nil, ErrBlob
	}
	if strings.TrimSpace(o.Bucket) == "" || o.get == nil {
		return nil, ErrNoBlob
	}
	key = strings.TrimSpace(key)
	if !validKey(key) {
		return nil, ErrBlob
	}
	return o.get(ctx, key)
}
