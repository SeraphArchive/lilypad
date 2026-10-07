// Command fixtool is a developer CLI for the captured-traffic oracle: inspect
// route distribution and replay captured requests through the live handler stack
// to structurally diff responses. The capture lives outside the repo and
// contains real data; fixtool reads its path from -path or LILYPAD_FIXTURES and
// exits cleanly (status 0) when it is absent.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	"lilypad/internal/config"
	"lilypad/internal/fixtures"
	"lilypad/internal/httpapi"
	"lilypad/internal/master"
	"lilypad/internal/model"
	"lilypad/internal/replay"
	"lilypad/internal/sign"
	"lilypad/internal/store/postgres"
)

func main() {
	path := flag.String("path", os.Getenv("LILYPAD_FIXTURES"), "capture .jsonl path")
	cfgPath := flag.String("config", "config.local.yaml", "config (for replay)")
	player := flag.String("player", "", "override x-player-id for replay (fresh seeded account)")
	flag.Parse()
	cmd := flag.Arg(0)

	if *path == "" {
		fmt.Println("no fixtures path (set -path or LILYPAD_FIXTURES); nothing to do")
		return
	}
	recs, err := fixtures.Load(*path)
	if err != nil {
		fmt.Println("fixtures unavailable:", err, "- skipping")
		return
	}

	switch cmd {
	case "", "routes":
		printRoutes(recs)
	case "replay":
		if err := doReplay(*cfgPath, *player, recs); err != nil {
			fmt.Fprintln(os.Stderr, "replay:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", cmd)
		os.Exit(2)
	}
}

func printRoutes(recs []fixtures.Record) {
	g := fixtures.GroupByRoute(recs)
	names := make([]string, 0, len(g))
	for k := range g {
		names = append(names, k)
	}
	sort.Strings(names)
	total := 0
	for _, n := range names {
		fmt.Printf("%4d %s\n", len(g[n]), n)
		total += len(g[n])
	}
	fmt.Printf("---- %d pairs across %d routes\n", total, len(names))
}

func doReplay(cfgPath, player string, recs []fixtures.Record) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	ctx := context.Background()
	pg, err := postgres.New(ctx, cfg.DB.DSN, model.NewPlayerSeed())
	if err != nil {
		return err
	}
	defer pg.Close()
	var md *master.Data
	if cfg.DataDir != "" {
		if md, err = master.Load(cfg.DataDir); err != nil {
			return err
		}
	}
	srv := httpapi.New(cfg, sign.NoopSigner{}, pg, pg, md, nil)
	results := replay.ReplayPairs(srv, recs, replay.Options{OverridePlayerID: player, Strict: true})
	fmt.Print(replay.Summary(results))
	for _, result := range results {
		if !result.OK() {
			return fmt.Errorf("capture mismatches; inspect summary above")
		}
	}
	return nil
}
