package providers

import (
	"context"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"golang.org/x/oauth2"
)

// BenchmarkBedrock_LoadDefaultConfig measures standalone cost of reading env vars,
// parsing AWS config/credentials, and building aws.Config.
func BenchmarkBedrock_LoadDefaultConfig(b *testing.B) {
	ctx := context.Background()
	os.Setenv("AWS_REGION", "us-east-1")
	defer os.Unsetenv("AWS_REGION")

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"))
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBedrock_ClientConstruction measures standalone cost of bedrockruntime.NewFromConfig.
func BenchmarkBedrock_ClientConstruction(b *testing.B) {
	ctx := context.Background()
	os.Setenv("AWS_REGION", "us-east-1")
	defer os.Unsetenv("AWS_REGION")

	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"))
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		client := bedrockruntime.NewFromConfig(cfg)
		if client == nil {
			b.Fatal("nil client")
		}
	}
}

// BenchmarkBedrock_CachedClientRetrieval measures steady-state getOrInitClient hit path.
func BenchmarkBedrock_CachedClientRetrieval(b *testing.B) {
	p := NewBedrockProvider(Spec{ID: "b", Type: "bedrock", Region: "us-east-1", Model: "m"})
	p.newClientWithHTTP = func(ctx context.Context, spec Spec, httpClient aws.HTTPClient) (bedrockConverseClient, error) {
		return &mockBedrockClient{id: 1}, nil
	}

	// Warm cache
	ctx := context.Background()
	_, err := p.getCache().getOrInitClient(ctx, p)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		client, err := p.getCache().getOrInitClient(ctx, p)
		if err != nil || client == nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBedrock_RequestPath_Recreated_vs_Cached compares per-turn config reloading against cached client reuse.
func BenchmarkBedrock_RequestPath_Cached(b *testing.B) {
	p := NewBedrockProvider(Spec{ID: "b", Type: "bedrock", Region: "us-east-1", Model: "m"})
	p.newClientWithHTTP = func(ctx context.Context, spec Spec, httpClient aws.HTTPClient) (bedrockConverseClient, error) {
		return &mockBedrockClient{id: 1}, nil
	}

	ctx := context.Background()
	_, _ = p.getCache().getOrInitClient(ctx, p)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		client, err := p.getCache().getOrInitClient(ctx, p)
		if err != nil {
			b.Fatal(err)
		}
		_, err = client.Converse(ctx, &bedrockruntime.ConverseInput{})
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBedrock_ConcurrentInitialization measures contention and throughput when 10 goroutines
// simultaneously request client initialization.
func BenchmarkBedrock_ConcurrentInitialization_10(b *testing.B) {
	b.ReportAllocs()
	ctx := context.Background()

	for i := 0; i < b.N; i++ {
		p := NewBedrockProvider(Spec{ID: "b", Type: "bedrock", Region: "us-east-1", Model: "m"})
		p.newClientWithHTTP = func(ctx context.Context, spec Spec, httpClient aws.HTTPClient) (bedrockConverseClient, error) {
			return &mockBedrockClient{id: 1}, nil
		}

		var wg sync.WaitGroup
		wg.Add(10)
		for j := 0; j < 10; j++ {
			go func() {
				defer wg.Done()
				_, _ = p.getCache().getOrInitClient(ctx, p)
			}()
		}
		wg.Wait()
	}
}

// BenchmarkBedrock_ConcurrentInitialization_50 measures contention and throughput when 50 goroutines
// simultaneously request client initialization.
func BenchmarkBedrock_ConcurrentInitialization_50(b *testing.B) {
	b.ReportAllocs()
	ctx := context.Background()

	for i := 0; i < b.N; i++ {
		p := NewBedrockProvider(Spec{ID: "b", Type: "bedrock", Region: "us-east-1", Model: "m"})
		p.newClientWithHTTP = func(ctx context.Context, spec Spec, httpClient aws.HTTPClient) (bedrockConverseClient, error) {
			return &mockBedrockClient{id: 1}, nil
		}

		var wg sync.WaitGroup
		wg.Add(50)
		for j := 0; j < 50; j++ {
			go func() {
				defer wg.Done()
				_, _ = p.getCache().getOrInitClient(ctx, p)
			}()
		}
		wg.Wait()
	}
}

// BenchmarkVertex_TokenResolution_vs_Cached measures ADC resolution overhead versus cached TokenSource.Token() retrieval.
func BenchmarkVertex_TokenResolution_Cached(b *testing.B) {
	p := NewVertexProvider(Spec{ID: "v", Type: "vertex", Project: "proj", Location: "us-central1", Model: "gemini-2.5-flash"})
	tsMock := &mockTokenSource{token: "cached-token"}
	p.ts = tsMock

	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tok, err := p.token(ctx)
		if err != nil || tok == "" {
			b.Fatal(err)
		}
	}
}

var _ = http.StatusOK
var _ = oauth2.Token{}
var _ = time.Second
