package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// R2Config holds credentials and bucket info for a Cloudflare R2 publisher.
// All fields are required; NewR2 returns an error if any are empty.
type R2Config struct {
	AccountID       string
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
}

// R2 publishes snapshots to a private Cloudflare R2 bucket via S3-compatible API.
// The client Worker (not ingest) serves objects publicly via an internal R2 binding.
type R2 struct {
	client *s3.Client
	bucket string
}

// NewR2 constructs an R2 publisher. The account-scoped endpoint is derived
// from the account ID; region is "auto" per Cloudflare's S3 compatibility docs.
func NewR2(ctx context.Context, cfg R2Config) (*R2, error) {
	if cfg.AccountID == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" || cfg.Bucket == "" {
		return nil, fmt.Errorf("r2 config: all fields (account_id, access_key_id, secret_access_key, bucket) are required")
	}

	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.AccountID)

	awsCfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("auto"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID,
			cfg.SecretAccessKey,
			"",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("loading aws config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})

	return &R2{client: client, bucket: cfg.Bucket}, nil
}

// Publish writes the snapshot to R2 twice: the canonical live key (overwritten
// every cycle, short cache) and a versioned history key (append-only, immutable,
// long cache). The Worker serves `/v1/active-events.json` from the first and
// `/v1/history/{ts}` from the second, enabling client-side scrubbing.
//
// Keys are alphabetically sortable thanks to the compact RFC3339-like format
// `history/YYYYMMDDTHHMMSSZ.json`, which matters because R2's list API returns
// objects in ascending lexicographic order.
func (p *R2) Publish(ctx context.Context, snapshot Snapshot) error {
	start := time.Now()

	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshaling snapshot: %w", err)
	}

	historyKey := fmt.Sprintf(
		"history/%s.json",
		snapshot.GeneratedAt.UTC().Format("20060102T150405Z"),
	)

	targets := []struct {
		key          string
		cacheControl string
	}{
		// Live snapshot — short cache so edge absorbs burst load but clients
		// see fresh data within one poll interval.
		{SnapshotKey, "public, max-age=10, s-maxage=10"},
		// Historical snapshot — immutable once written, cache for a year.
		{historyKey, "public, max-age=31536000, immutable"},
	}

	for _, t := range targets {
		_, err := p.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:       aws.String(p.bucket),
			Key:          aws.String(t.key),
			Body:         bytes.NewReader(data),
			ContentType:  aws.String("application/json; charset=utf-8"),
			CacheControl: aws.String(t.cacheControl),
		})
		if err != nil {
			return fmt.Errorf("r2 put %s/%s: %w", p.bucket, t.key, err)
		}
	}

	slog.InfoContext(ctx, "snapshot published",
		"destination", "r2",
		"bucket", p.bucket,
		"current_key", SnapshotKey,
		"history_key", historyKey,
		"size_bytes", len(data),
		"alert_count", snapshot.AlertCount,
		"duration", time.Since(start),
	)

	return nil
}
