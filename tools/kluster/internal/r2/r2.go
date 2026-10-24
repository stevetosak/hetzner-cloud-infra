// Package r2 reads and clears Cloudflare R2 buckets for `kluster db create`
// and `kluster down` (ADR 0009, Amendment 2026-10-24): the object listing
// over R2's S3 API, and the bucket lock rules over the Cloudflare API.
//
// Every token comes from Bitwarden and reaches no output. R2 tokens scope to
// buckets, not prefixes, so what a token can reach is the bucket it was made
// for; a prefix in this package is a filter, never a boundary.
package r2

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Credentials are one R2 token's S3 keys.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
}

// Object is one object in a bucket.
type Object struct {
	Key  string
	Size int64
}

// Bucket is the part of a bucket kluster uses.
type Bucket interface {
	Name() string
	// Prefixes returns the top-level prefixes ("<name>/"), each of which
	// holds at least one object: S3 has no empty directory.
	Prefixes(ctx context.Context) ([]string, error)
	// List returns every object whose key starts with prefix.
	List(ctx context.Context, prefix string) ([]Object, error)
	// Delete removes the objects with keys.
	Delete(ctx context.Context, keys []string) error
}

// S3 is a Bucket over R2's S3 API.
type S3 struct {
	bucket string
	c      *minio.Client
}

// Open returns the bucket at endpoint, an https URL such as
// https://<account>.r2.cloudflarestorage.com.
func Open(endpoint, bucket string, cred Credentials) (*S3, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("R2 endpoint %q: want https://<host>", endpoint)
	}
	c, err := minio.New(u.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(cred.AccessKeyID, cred.SecretAccessKey, ""),
		Secure: true,
		// R2 takes "auto"; a region also stops minio asking for one.
		Region:       "auto",
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, err
	}
	return &S3{bucket: bucket, c: c}, nil
}

func (b *S3) Name() string { return b.bucket }

func (b *S3) Prefixes(ctx context.Context) ([]string, error) {
	var out []string
	for o := range b.c.ListObjects(ctx, b.bucket, minio.ListObjectsOptions{}) {
		if o.Err != nil {
			return nil, fmt.Errorf("listing %s: %w", b.bucket, o.Err)
		}
		if strings.HasSuffix(o.Key, "/") {
			out = append(out, o.Key)
		}
	}
	return out, nil
}

func (b *S3) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	for o := range b.c.ListObjects(ctx, b.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if o.Err != nil {
			return nil, fmt.Errorf("listing %s/%s: %w", b.bucket, prefix, o.Err)
		}
		out = append(out, Object{Key: o.Key, Size: o.Size})
	}
	return out, nil
}

func (b *S3) Delete(ctx context.Context, keys []string) error {
	ch := make(chan minio.ObjectInfo, len(keys))
	for _, k := range keys {
		ch <- minio.ObjectInfo{Key: k}
	}
	close(ch)
	var errs []error
	for e := range b.c.RemoveObjects(ctx, b.bucket, ch, minio.RemoveObjectsOptions{}) {
		errs = append(errs, fmt.Errorf("deleting %s/%s: %w", b.bucket, e.ObjectName, e.Err))
	}
	return errors.Join(errs...)
}

// Empty deletes every object in b and reads the listing back. It refuses a
// bucket whose name is not want: the name check is the guard against a token
// that reaches a bucket it was not meant for.
func Empty(ctx context.Context, b Bucket, want string) (int, error) {
	if b.Name() != want {
		return 0, fmt.Errorf("refusing to empty %s: only %s may be emptied", b.Name(), want)
	}
	objs, err := b.List(ctx, "")
	if err != nil || len(objs) == 0 {
		return 0, err
	}
	keys := make([]string, len(objs))
	for i, o := range objs {
		keys[i] = o.Key
	}
	if err := b.Delete(ctx, keys); err != nil {
		return 0, err
	}
	after, err := b.List(ctx, "")
	if err != nil {
		return 0, err
	}
	if len(after) > 0 {
		return 0, fmt.Errorf("%s still holds %d object(s) after the delete", b.Name(), len(after))
	}
	return len(keys), nil
}
