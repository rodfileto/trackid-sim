package main

// snapshot saves both cluster layers' feature -> cluster ids before the review exercise, so eval
// -before can report afterwards which clusters the students' decisions merged or split, and any
// cluster id that changed for no recorded reason (see docs/ROADMAP.md, block D).

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/rodfileto/trackid-sim/eval"
	"github.com/rodfileto/trackid/cluster"
)

// clusterSnapshot is the file snapshot writes: each layer's assignment, keyed by
// cluster_members.basis.
type clusterSnapshot struct {
	TakenAt time.Time                   `json:"taken_at"`
	Layers  map[string]map[string]int64 `json:"layers"`
}

func cmdSnapshot(args []string) {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	envFile := fs.String("env", "", "dotenv file with DATABASE_URL")
	out := fs.String("out", "out/snapshot.json", "file to write")
	fs.Parse(args)

	ctx := context.Background()
	sqlDB := connect(ctx, *envFile)
	defer sqlDB.Close()

	s := clusterSnapshot{TakenAt: time.Now().UTC(), Layers: map[string]map[string]int64{}}
	for _, basis := range cluster.Bases {
		assign, err := eval.Snapshot(ctx, sqlDB, string(basis))
		must(err, "snapshot")
		s.Layers[string(basis)] = assign
		fmt.Printf("%-9s %d features\n", basis, len(assign))
	}
	writeJSON(*out, s)
	fmt.Printf("wrote %s\n", *out)
}

func readSnapshot(path string) clusterSnapshot {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	var s clusterSnapshot
	if err := json.Unmarshal(data, &s); err != nil {
		log.Fatalf("%s: %v", path, err)
	}
	return s
}
