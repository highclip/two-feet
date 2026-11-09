// Package demo loads the sample people, posts and spaces the demo path needs. It runs just before
// presenting, because the spaces' start times are relative to now. Everything goes through the
// store and the indexer, and every sample person's name says "(sample)".
package demo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/highclip/two-feet/app/internal/search"
	"github.com/highclip/two-feet/app/internal/store"
)

type samplePerson struct {
	key  string
	name string
	tags []string
}

var people = []samplePerson{
	{"priya", "Priya (sample)", []string{"otel-collector", "tail-sampling"}},
	{"marcus", "Marcus (sample)", []string{"kubernetes", "helm"}},
	{"lena", "Lena (sample)", []string{"postgres", "go"}},
	{"tomas", "Tomas (sample)", []string{"ebpf", "kubernetes"}},
	{"aiko", "Aiko (sample)", []string{"elasticsearch", "vector-search"}},
	{"dev", "Dev (sample)", []string{"temporal", "go"}},
	{"nora", "Nora (sample)", []string{"gitops", "argocd"}},
	{"sam", "Sam (sample)", []string{"tailscale", "networking"}},
	{"ines", "Ines (sample)", []string{"prometheus", "grafana"}},
	{"kofi", "Kofi (sample)", []string{"kafka", "go"}},
	{"mei", "Mei (sample)", []string{"terraform", "docker"}},
	{"jo", "Jo (sample)", []string{"github-actions", "docker"}},
}

type samplePost struct {
	author string
	kind   string
	text   string
}

var posts = []samplePost{
	{"priya", store.PostOffer, "Rebuilding our OTel collector pipelines with tail sampling, happy to compare notes"},
	{"marcus", store.PostStuck, "Helm upgrades on k8s keep timing out during the pre-upgrade job"},
	{"marcus", store.PostCurious, "How are people sizing Kubernetes node pools for bursty workloads?"},
	{"lena", store.PostOffer, "I can help with Postgres connection pooling and pgx in Go"},
	{"lena", store.PostStuck, "Long-running Postgres migrations lock the table during deploys"},
	{"tomas", store.PostCurious, "Anyone tracing network calls with eBPF in production?"},
	{"aiko", store.PostOffer, "Happy to show hybrid search with Elasticsearch and ELSER"},
	{"aiko", store.PostCurious, "What does reciprocal rank fusion do with very short documents?"},
	{"dev", store.PostStuck, "Our Temporal workers lose activity heartbeats during rolling restarts"},
	{"dev", store.PostOffer, "Can walk through durable retries for flaky third-party APIs"},
	{"nora", store.PostCurious, "GitOps with ArgoCD across three clusters, what broke for you?"},
	{"nora", store.PostStuck, "ArgoCD sync loops forever on a Helm chart with random secrets"},
	{"sam", store.PostOffer, "Can help you get a private network between clusters with Tailscale"},
	{"sam", store.PostCurious, "Who is running subnet routers inside Kubernetes?"},
	{"ines", store.PostStuck, "Prometheus memory keeps climbing after we added more labels"},
	{"ines", store.PostOffer, "Happy to review your Grafana dashboards and alert rules"},
	{"kofi", store.PostStuck, "Kafka consumers rebalance constantly when pods restart"},
	{"mei", store.PostCurious, "Terraform drift detection, do you run it on a schedule?"},
	{"mei", store.PostOffer, "I can pair on slimming down Docker images for Go services"},
	{"jo", store.PostStuck, "GitHub Actions cache misses make our builds take forever"},
}

// collectorTitle is the space the demo path walks to (demo_seed.must_include, moved by D1).
const collectorTitle = "Collector tuning: dropping spans under load"

type sampleSpace struct {
	host     string
	title    string
	blurb    string
	spot     string
	startsIn time.Duration
	minutes  int
	capacity int
	going    []string // beyond the host
}

var spaces = []sampleSpace{
	{
		"marcus", collectorTitle, "Bring your collector config and a graph of what you're losing.",
		"Long table", 6 * time.Minute, 30, 8, []string{"priya", "ines", "tomas", "kofi"},
	},
	{"nora", "ArgoCD across many clusters", "What's working, what isn't.", "Window table", 10 * time.Minute, 30, 6, []string{"sam"}},
	{"lena", "Postgres migrations without downtime", "Locks, batches and backfills.", "Kitchen island", 20 * time.Minute, 30, 6, []string{"dev"}},
	{"aiko", "Hybrid search show and tell", "ELSER and rank fusion, live.", "Couches", 35 * time.Minute, 30, 8, nil},
	{"dev", "Durable workflows: what really survives a restart", "", "Long table", 45 * time.Minute, 30, 10, []string{"kofi"}},
	{"mei", "Smaller Docker images", "Bring a Dockerfile.", "Picnic table", 50 * time.Minute, 15, 8, nil},
}

// Load replaces the sample data, then rebuilds the index so it holds this run's rows and none of
// the last run's. Real people, their posts and their spaces are left alone. A space that a real
// space already occupies the spot for is skipped, except the collector space, which the demo needs.
func Load(ctx context.Context, st *store.Store, index search.Indexer, log *slog.Logger, eventSlug string, now time.Time) error {
	if err := st.ClearSamples(ctx); err != nil {
		return err
	}
	byKey := make(map[string]store.Person, len(people))
	for _, p := range people {
		person, err := st.SamplePerson(ctx, p.key, p.name, p.tags)
		if err != nil {
			return fmt.Errorf("sample person %s: %w", p.key, err)
		}
		byKey[p.key] = person
	}
	for _, p := range posts {
		if _, err := st.CreatePost(ctx, byKey[p.author].ID, p.kind, p.text, search.ExtractTags(p.text)); err != nil {
			return fmt.Errorf("sample post: %w", err)
		}
	}
	event, err := st.EventBySlug(ctx, eventSlug)
	if err != nil {
		return err
	}
	spots, err := st.Spots(ctx, event.ID)
	if err != nil {
		return err
	}
	spotIDs := make(map[string]string, len(spots))
	for _, sp := range spots {
		spotIDs[sp.Name] = sp.ID
	}
	for _, sp := range spaces {
		if err := createSpace(ctx, st, sp, byKey, spotIDs, now); err != nil {
			var taken *store.SpotTakenError
			if errors.As(err, &taken) && sp.title != collectorTitle {
				log.Warn("sample space skipped: its spot is taken", "spot", sp.spot)
				continue
			}
			return fmt.Errorf("sample space at %s: %w", sp.spot, err)
		}
	}
	if err := search.NewSync(st, index, log).All(ctx); err != nil {
		return fmt.Errorf("index the sample data: %w", err)
	}
	return nil
}

func createSpace(ctx context.Context, st *store.Store, sp sampleSpace, byKey map[string]store.Person, spots map[string]string, now time.Time) error {
	spotID, ok := spots[sp.spot]
	if !ok {
		return fmt.Errorf("no spot named %s", sp.spot)
	}
	created, err := st.CreateSpace(ctx, byKey[sp.host].ID, store.SpaceInput{
		Title: sp.title, Blurb: sp.blurb, SpotID: spotID, StartsAt: now.Add(sp.startsIn), Minutes: sp.minutes, Capacity: sp.capacity,
	})
	if err != nil {
		return err
	}
	going := make([]string, 0, len(sp.going))
	for _, key := range sp.going {
		going = append(going, byKey[key].ID)
	}
	return st.AddGoing(ctx, created.ID, going)
}
