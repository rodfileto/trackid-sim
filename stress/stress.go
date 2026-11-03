// Package stress measures a trackid stack under a burst (docs/stress-test-plan.md in
// ../trackid): it samples the machine while the burst runs -- GPU, the stack's containers,
// Postgres connections -- and afterwards reads from the database when each stage of the
// pipeline finished, how large the tables grew and which statements cost the most. Everything
// lands in one Record, written as JSON to out/stress/; nothing leaves the machine.
package stress

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rodfileto/trackid-sim/load"
	"github.com/rodfileto/trackid/embedding"
	"github.com/rodfileto/trackid/fingerprint"
)

// Record is one stress run.
type Record struct {
	Label   string    `json:"label"`
	TakenAt time.Time `json:"takenAt"`
	Config  Config    `json:"config"`
	Machine Machine   `json:"machine"`
	World   World     `json:"world"`

	Burst      BurstSummary      `json:"burst"`
	Drain      Drain             `json:"drain"`
	Final      load.Progress     `json:"final"`
	Progress   []ProgressSample  `json:"progress"`
	Samples    []Sample          `json:"samples"`
	Tables     []Table           `json:"tables"`
	Statements []Statement       `json:"statements,omitempty"`
	Counts     map[string]int64  `json:"counts"`
	Ops        []load.Op         `json:"ops"`
	Notes      map[string]string `json:"notes,omitempty"`
}

// Config is what the run was asked to do.
type Config struct {
	Seed       uint64 `json:"seed"`
	Humans     int    `json:"humans"`
	FaceReuse  bool   `json:"faceReuse"`
	Senders    int    `json:"senders"`
	Window     string `json:"window"`
	Workers    string `json:"workerConcurrency"` // WORKER_CONCURRENCY as the worker was started with
	TimeCap    string `json:"timeCap"`
	Poll       string `json:"poll"`
	Thresholds string `json:"thresholds,omitempty"`
}

// World is the size of what was sent.
type World struct {
	Humans, Enrollments, Occurrences, Cases, FacialCases, FingerprintCases, Traces int
}

// BurstSummary condenses load.BurstResult; the operations themselves are in Record.Ops.
type BurstSummary struct {
	Prepare     float64   `json:"prepareSeconds"`
	Cached      bool      `json:"preparedFromCache"`
	Occurrences float64   `json:"occurrencesSeconds"`
	Start       time.Time `json:"start"`
	Send        float64   `json:"sendSeconds"`
	Ops         int       `json:"ops"`
	Errors      int       `json:"errors"`
	Files       int       `json:"files"`
	MiB         float64   `json:"mib"`
	// Seconds an operation took, by kind, and the longest wait for a free sender.
	Took        map[string]Quantiles `json:"tookSeconds"`
	MaxWait     float64              `json:"maxWaitSeconds"`
	FirstErrors []string             `json:"firstErrors,omitempty"`
}

type Quantiles struct {
	N                  int
	P50, P95, P99, Max float64
}

// Summarize condenses a burst.
func Summarize(r load.BurstResult) BurstSummary {
	s := BurstSummary{
		Prepare: r.Prepare.Seconds(), Cached: r.Cached, Occurrences: r.Occurrences.Seconds(), Start: r.Start, Send: r.Send.Seconds(),
		Ops: len(r.Ops), Errors: r.Errors(), Took: map[string]Quantiles{},
	}
	took := map[string][]float64{}
	bytes := 0
	for _, op := range r.Ops {
		took[op.Kind] = append(took[op.Kind], op.Took.Seconds())
		took["all"] = append(took["all"], op.Took.Seconds())
		s.Files += op.Files
		bytes += op.Bytes
		s.MaxWait = max(s.MaxWait, (op.Started - op.Released).Seconds())
		if op.Err != "" && len(s.FirstErrors) < 10 {
			s.FirstErrors = append(s.FirstErrors, op.Kind+" "+op.ID+": "+op.Err)
		}
	}
	s.MiB = float64(bytes) / (1 << 20)
	for k, v := range took {
		s.Took[k] = quantiles(v)
	}
	return s
}

func quantiles(v []float64) Quantiles {
	q := Quantiles{N: len(v)}
	if len(v) == 0 {
		return q
	}
	sorted := append([]float64(nil), v...)
	slices.Sort(sorted)
	at := func(p float64) float64 { return sorted[min(len(sorted)-1, int(p*float64(len(sorted))))] }
	q.P50, q.P95, q.P99, q.Max = at(0.50), at(0.95), at(0.99), sorted[len(sorted)-1]
	return q
}

// Drain is when each stage of the pipeline finished, in seconds since the burst started, read
// from the rows' own timestamps. Zero means the stage wrote nothing.
type Drain struct {
	Cleared        float64 `json:"clearedSeconds"` // load.Wait returned: queue empty, everything matched
	TimedOut       bool    `json:"timedOut"`
	LastEmbedding  float64 `json:"lastEmbeddingSeconds"`
	LastTemplate   float64 `json:"lastTemplateSeconds"`
	LastFaceMatch  float64 `json:"lastFaceMatchSeconds"`
	LastPrintMatch float64 `json:"lastPrintMatchSeconds"`
	FirstDecision  float64 `json:"firstDecisionSeconds"`
	LastDecision   float64 `json:"lastDecisionSeconds"`
	LastCluster    float64 `json:"lastClusterSeconds"`
}

// ReadDrain reads the stage timestamps relative to start.
func ReadDrain(ctx context.Context, sqlDB *sql.DB, start time.Time) (Drain, error) {
	var d Drain
	since := func(q string, args ...any) (float64, error) {
		var t sql.NullTime
		if err := sqlDB.QueryRowContext(ctx, q, args...).Scan(&t); err != nil {
			return 0, err
		}
		if !t.Valid {
			return 0, nil
		}
		return t.Time.Sub(start).Seconds(), nil
	}
	var err error
	for _, s := range []struct {
		dst  *float64
		q    string
		args []any
	}{
		{&d.LastEmbedding, `SELECT max(created_at) FROM feature_embeddings WHERE embedding_type = $1`, []any{embedding.EmbeddingType}},
		{&d.LastTemplate, `SELECT max(created_at) FROM biometric_templates WHERE template_type = $1`, []any{fingerprint.TemplateType}},
		{&d.LastFaceMatch, `SELECT max(matched_at) FROM feature_embeddings WHERE embedding_type = $1`, []any{embedding.EmbeddingType}},
		{&d.LastPrintMatch, `SELECT max(matched_at) FROM biometric_templates WHERE template_type = $1`, []any{fingerprint.TemplateType}},
		{&d.FirstDecision, `SELECT min(created_at) FROM biometric_decisions WHERE role = 'SYSTEM'`, nil},
		{&d.LastDecision, `SELECT max(created_at) FROM biometric_decisions WHERE role = 'SYSTEM'`, nil},
		{&d.LastCluster, `SELECT max(updated_at) FROM clusters`, nil},
	} {
		if *s.dst, err = since(s.q, s.args...); err != nil {
			return d, err
		}
	}
	return d, nil
}

// Counts reads how many rows the pipeline produced.
func Counts(ctx context.Context, sqlDB *sql.DB) (map[string]int64, error) {
	out := map[string]int64{}
	for name, q := range map[string]string{
		"faceEmbeddings":      `SELECT count(*) FROM feature_embeddings`,
		"templates":           `SELECT count(*) FROM biometric_templates`,
		"systemPositive":      `SELECT count(*) FROM biometric_decisions WHERE role = 'SYSTEM' AND decision = 'POSITIVE'`,
		"systemInconclusive":  `SELECT count(*) FROM biometric_decisions WHERE role = 'SYSTEM' AND decision = 'INCONCLUSIVE'`,
		"clustersAutomatic":   `SELECT count(*) FROM clusters WHERE basis = 'AUTOMATIC'`,
		"clusterMembers":      `SELECT count(*) FROM cluster_members`,
		"cases":               `SELECT count(*) FROM biometric_cases`,
		"requests":            `SELECT count(*) FROM case_requests`,
		"analyses":            `SELECT count(*) FROM case_analyses`,
		"identityRegisters":   `SELECT count(*) FROM identity_registers`,
		"biometricFeatures":   `SELECT count(*) FROM biometric_features`,
		"evidenceWithoutFile": `SELECT count(*) FROM case_evidences WHERE case_file_id IS NULL`,
	} {
		var n int64
		if err := sqlDB.QueryRowContext(ctx, q).Scan(&n); err != nil {
			return out, err
		}
		out[name] = n
	}
	return out, nil
}

// Table is one table's size on disk, indexes and TOAST included.
type Table struct {
	Name  string  `json:"name"`
	Rows  int64   `json:"rows"` // planner's estimate after ANALYZE
	MiB   float64 `json:"mib"`
	Index float64 `json:"indexMib"`
}

// Tables lists the twenty largest tables.
func Tables(ctx context.Context, sqlDB *sql.DB) ([]Table, error) {
	if _, err := sqlDB.ExecContext(ctx, `ANALYZE`); err != nil {
		return nil, err
	}
	rows, err := sqlDB.QueryContext(ctx, `
		SELECT c.relname, c.reltuples::bigint,
		       pg_total_relation_size(c.oid) / 1048576.0, pg_indexes_size(c.oid) / 1048576.0
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r' AND n.nspname = 'public'
		ORDER BY pg_total_relation_size(c.oid) DESC LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Table
	for rows.Next() {
		var t Table
		if err := rows.Scan(&t.Name, &t.Rows, &t.MiB, &t.Index); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Statement is one normalised statement from pg_stat_statements.
type Statement struct {
	Query   string  `json:"query"`
	Calls   int64   `json:"calls"`
	TotalMs float64 `json:"totalMs"`
	MeanMs  float64 `json:"meanMs"`
	MaxMs   float64 `json:"maxMs"`
	Rows    int64   `json:"rows"`
}

// ResetStatements clears pg_stat_statements, so Statements covers the run only. It reports
// false when the extension is not loaded (the stress stack's Postgres preloads it).
func ResetStatements(ctx context.Context, sqlDB *sql.DB) bool {
	if _, err := sqlDB.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pg_stat_statements`); err != nil {
		return false
	}
	_, err := sqlDB.ExecContext(ctx, `SELECT pg_stat_statements_reset()`)
	return err == nil
}

// Statements lists the twenty statements that took the most time in all.
func Statements(ctx context.Context, sqlDB *sql.DB) ([]Statement, error) {
	rows, err := sqlDB.QueryContext(ctx, `
		SELECT left(regexp_replace(query, '\s+', ' ', 'g'), 300), calls, total_exec_time, mean_exec_time, max_exec_time, rows
		FROM pg_stat_statements WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
		ORDER BY total_exec_time DESC LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Statement
	for rows.Next() {
		var s Statement
		if err := rows.Scan(&s.Query, &s.Calls, &s.TotalMs, &s.MeanMs, &s.MaxMs, &s.Rows); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ProgressSample is one look at the pipeline, seconds since the burst started.
type ProgressSample struct {
	At float64 `json:"at"`
	load.Progress
}

// Sample is one look at the machine, seconds since the burst started.
type Sample struct {
	At            float64              `json:"at"`
	GPUUtil       int                  `json:"gpuUtil"` // percent
	GPUMemMiB     int                  `json:"gpuMemMiB"`
	Load1         float64              `json:"load1"`
	PGConnections int                  `json:"pgConnections"`
	Containers    map[string]Container `json:"containers,omitempty"`
}

type Container struct {
	CPU    float64 `json:"cpu"` // percent of one core
	MemMiB float64 `json:"memMiB"`
}

// Sampler samples the machine every interval until Stop.
type Sampler struct {
	mu      sync.Mutex
	samples []Sample
	stop    chan struct{}
	done    chan struct{}
}

// StartSampler samples every interval from start: the GPU (nvidia-smi), the load average,
// Postgres connections and the containers of the compose project.
func StartSampler(ctx context.Context, sqlDB *sql.DB, project string, start time.Time, interval time.Duration) *Sampler {
	s := &Sampler{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		for {
			sample := Sample{At: time.Since(start).Seconds()}
			sample.GPUUtil, sample.GPUMemMiB = gpu(ctx)
			sample.Load1 = load1()
			sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()`).Scan(&sample.PGConnections)
			sample.Containers = containers(ctx, project)
			s.mu.Lock()
			s.samples = append(s.samples, sample)
			s.mu.Unlock()
			select {
			case <-s.stop:
				return
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
		}
	}()
	return s
}

// Stop ends sampling and returns the samples.
func (s *Sampler) Stop() []Sample {
	close(s.stop)
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.samples
}

func gpu(ctx context.Context) (util, memMiB int) {
	out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=utilization.gpu,memory.used", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return -1, -1
	}
	f := strings.Split(strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]), ",")
	if len(f) != 2 {
		return -1, -1
	}
	util, _ = strconv.Atoi(strings.TrimSpace(f[0]))
	memMiB, _ = strconv.Atoi(strings.TrimSpace(f[1]))
	return util, memMiB
}

func load1() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return -1
	}
	v, _ := strconv.ParseFloat(strings.Fields(string(b))[0], 64)
	return v
}

func containers(ctx context.Context, project string) map[string]Container {
	if project == "" {
		return nil
	}
	ids, err := exec.CommandContext(ctx, "docker", "ps", "-q", "--filter", "label=com.docker.compose.project="+project).Output()
	if err != nil || len(bytes.TrimSpace(ids)) == 0 {
		return nil
	}
	args := append([]string{"stats", "--no-stream", "--format", "{{json .}}"}, strings.Fields(string(ids))...)
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		return nil
	}
	res := map[string]Container{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		var row struct{ Name, CPUPerc, MemUsage string }
		if json.Unmarshal(sc.Bytes(), &row) != nil {
			continue
		}
		cpu, _ := strconv.ParseFloat(strings.TrimSuffix(row.CPUPerc, "%"), 64)
		name := strings.TrimPrefix(row.Name, project+"-")
		res[name] = Container{CPU: cpu, MemMiB: mib(strings.TrimSpace(strings.SplitN(row.MemUsage, "/", 2)[0]))}
	}
	return res
}

// mib reads a docker size such as "512.3MiB" or "1.2GiB".
func mib(s string) float64 {
	for _, u := range []struct {
		suffix string
		f      float64
	}{{"GiB", 1024}, {"MiB", 1}, {"KiB", 1.0 / 1024}, {"kB", 1.0 / 1024}, {"MB", 1}, {"GB", 1024}, {"B", 1.0 / (1 << 20)}} {
		if strings.HasSuffix(s, u.suffix) {
			v, _ := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
			return v * u.f
		}
	}
	return 0
}

// Machine is where the run happened.
type Machine struct {
	CPU      string `json:"cpu"`
	Cores    int    `json:"cores"`
	MemGiB   int    `json:"memGiB"`
	GPU      string `json:"gpu"`
	SimSHA   string `json:"simSha"`
	TrackSHA string `json:"trackidSha"`
	Dirty    string `json:"dirty,omitempty"` // repos with uncommitted changes
}

// ReadMachine describes this machine and the two repos' commits.
func ReadMachine(ctx context.Context, simDir, trackidDir string) Machine {
	m := Machine{Cores: runtime.NumCPU()}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "model name") {
				m.CPU = strings.TrimSpace(strings.SplitN(l, ":", 2)[1])
				break
			}
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		var kb int
		for _, l := range strings.Split(string(b), "\n") {
			if f := strings.Fields(l); len(f) >= 2 && f[0] == "MemTotal:" {
				kb, _ = strconv.Atoi(f[1])
			}
		}
		m.MemGiB = (kb + (1 << 19)) >> 20
	}
	if out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader").Output(); err == nil {
		m.GPU = strings.TrimSpace(string(out))
	}
	var dirty []string
	sha := func(dir, name string) string {
		out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--short", "HEAD").Output()
		if err != nil {
			return ""
		}
		if st, _ := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain", "--untracked-files=no").Output(); len(bytes.TrimSpace(st)) > 0 {
			dirty = append(dirty, name)
		}
		return strings.TrimSpace(string(out))
	}
	m.SimSHA, m.TrackSHA = sha(simDir, "trackid-sim"), sha(trackidDir, "trackid")
	m.Dirty = strings.Join(dirty, ",")
	return m
}
