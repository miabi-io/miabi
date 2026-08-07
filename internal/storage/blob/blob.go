// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package blob is a minimal S3/MinIO object client for the few places Miabi must
// touch an object store directly, from Go, without a helper container.
//
// Everything else that talks to S3 does so through the jkaninda/*-bkup one-shot
// images, which is the right tool for a data artifact: the helper streams a dump
// or an archive straight to the bucket, and Miabi never handles the bytes. That
// only works while there is a running platform to schedule containers with
// credentials from the database.
//
// Two paths need more than that. A portable workspace bundle's dumps and volume
// archives are still the helpers' work, but its control objects — the info file
// that indexes it and the sealed state file that holds its configuration — are
// written and read by the control plane itself, and listing what a bucket holds
// is a query no helper answers. Disaster recovery has no platform at all:
// `miabi restore` must read the sealed identity envelope off a bucket on a bare
// host, before a control plane, a database or any credentials exist. Hence this
// package.
package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// ErrNotFound is returned when an object (or the bucket) does not exist.
var ErrNotFound = errors.New("object not found")

// Config describes an S3-compatible target. It mirrors the fields of the backup
// S3 settings so callers can hand the same values to either path.
type Config struct {
	Endpoint       string // empty for AWS S3; host or URL for MinIO and friends
	Bucket         string
	Region         string
	AccessKey      string
	SecretKey      string
	UseSSL         bool
	ForcePathStyle bool
}

// Store is a bucket-scoped object client.
type Store struct {
	client *s3.Client
	bucket string
}

// Object is one listed object.
type Object struct {
	Key       string
	Size      int64
	UpdatedAt time.Time
}

// New builds a client for cfg. It does not contact the network; the first
// Get/Put/List reports connectivity or credential problems.
func New(cfg Config) (*Store, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("blob: a bucket is required")
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		// S3 requires *a* region even when the endpoint is a MinIO that ignores it.
		// Must match the default the *-bkup helpers are given (see
		// wsbackup.defaultS3Region and platformbackup.defaultS3Region): a client and
		// a helper signing for different regions is a harder failure to read than
		// either alone.
		region = "us-east-1"
	}
	opts := []func(*s3.Options){
		func(o *s3.Options) {
			o.Region = region
			o.Credentials = credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")
			o.HTTPClient = &http.Client{Timeout: 10 * time.Minute}
			// MinIO and most self-hosted gateways serve bucket-in-path, not
			// bucket-as-subdomain — and a virtual-host request against them fails as
			// a DNS error far from here.
			o.UsePathStyle = cfg.ForcePathStyle
		},
	}
	if ep := normalizeEndpoint(cfg.Endpoint, cfg.UseSSL); ep != "" {
		opts = append(opts, func(o *s3.Options) { o.BaseEndpoint = aws.String(ep) })
	}
	return &Store{client: s3.New(s3.Options{}, opts...), bucket: cfg.Bucket}, nil
}

// normalizeEndpoint turns the bare "host:port" form the backup settings accept
// into the absolute URL the SDK requires, choosing the scheme from UseSSL. An
// endpoint that already carries a scheme is left alone, so an operator who typed
// a full URL gets what they typed.
func normalizeEndpoint(endpoint string, useSSL bool) string {
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		return ""
	}
	if strings.Contains(ep, "://") {
		return strings.TrimRight(ep, "/")
	}
	scheme := "https://"
	if !useSSL {
		scheme = "http://"
	}
	return scheme + strings.TrimRight(ep, "/")
}

// Get opens an object for reading. The caller closes the returned reader.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, 0, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, 0, fmt.Errorf("blob: get %s: %w", key, err)
	}
	var size int64
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	return out.Body, size, nil
}

// GetBytes reads a whole object. Intended for small control objects (the identity
// envelope, manifests) — not for artifacts.
func (s *Store) GetBytes(ctx context.Context, key string) ([]byte, error) {
	body, _, err := s.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	return io.ReadAll(body)
}

// Put writes an object.
func (s *Store) Put(ctx context.Context, key string, body []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   strings.NewReader(string(body)),
	})
	if err != nil {
		return fmt.Errorf("blob: put %s: %w", key, err)
	}
	return nil
}

// Exists reports whether an object is present.
func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("blob: head %s: %w", key, err)
	}
	return true, nil
}

// Delete removes an object. Deleting an absent object is not an error, so
// retention pruning is idempotent.
func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("blob: delete %s: %w", key, err)
	}
	return nil
}

// List returns every object under prefix, following pagination.
func (s *Store) List(ctx context.Context, prefix string) ([]Object, error) {
	var (
		out   []Object
		token *string
	)
	for {
		page, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			if isNotFound(err) {
				return nil, fmt.Errorf("%w: bucket %s", ErrNotFound, s.bucket)
			}
			return nil, fmt.Errorf("blob: list %s: %w", prefix, err)
		}
		for _, o := range page.Contents {
			obj := Object{Key: aws.ToString(o.Key)}
			if o.Size != nil {
				obj.Size = *o.Size
			}
			if o.LastModified != nil {
				obj.UpdatedAt = *o.LastModified
			}
			out = append(out, obj)
		}
		if page.IsTruncated == nil || !*page.IsTruncated {
			return out, nil
		}
		token = page.NextContinuationToken
	}
}

// isNotFound recognizes the several shapes S3 uses to say "no such thing":
// typed NoSuchKey/NotFound/NoSuchBucket, and the bare 404 MinIO returns from
// HeadObject (which carries no body to decode an error code from).
func isNotFound(err error) bool {
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return true
	}
	var nsb *types.NoSuchBucket
	if errors.As(err, &nsb) {
		return true
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NoSuchKey", "NotFound", "NoSuchBucket", "404":
			return true
		}
	}
	return false
}
