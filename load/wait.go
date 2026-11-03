package load

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
	"github.com/rodfileto/trackid/embedding"
	"github.com/rodfileto/trackid/fingerprint"
)

// Progress is one look at trackid's pipeline for the simulated data.
type Progress struct {
	Faces     int // simulated face features that have an image to embed
	Embedded  int // of those, with a face embedding
	Unmatched int // face embeddings not yet run through matching

	Prints          int // simulated fingerprint features that have an image
	Extracted       int // of those, with a template
	PrintsUnmatched int // templates not yet run through matching

	Queued int // tasks pending, active, scheduled or awaiting retry
	// Of Queued: waiting for a worker slot, running, and awaiting a retry after failing.
	Pending, Active, Retry int
	Failed                 int // tasks archived after exhausting their retries
}

func (p Progress) String() string {
	return fmt.Sprintf("faces embedded %d/%d (unmatched %d), prints extracted %d/%d (unmatched %d), queued %d, failed %d",
		p.Embedded, p.Faces, p.Unmatched, p.Extracted, p.Prints, p.PrintsUnmatched, p.Queued, p.Failed)
}

// Wait blocks until trackid's worker has finished with the loaded faces and
// prints: nothing left in the queue and every face embedding and fingerprint
// template matched, which means the last syncs (match, then cluster) have run.
// It checks every poll and gives up at timeout. When the queue is empty but
// embeddings or templates are still unmatched -- a sync request that was folded
// into one already running -- it enqueues trackid's own sync task for that
// modality, as an operator re-running the pipeline would.
//
// A face the detector cannot find, or a print the extractor cannot use, fails
// its task and is archived after its retries; Wait counts it as done, not
// pending, and reports it in Failed.
func Wait(ctx context.Context, sqlDB *sql.DB, redisURL string, queue embedding.Enqueuer, poll, timeout time.Duration, logf func(string, ...any)) (Progress, error) {
	return WaitObserved(ctx, sqlDB, redisURL, queue, poll, timeout, logf, nil)
}

// WaitObserved is Wait, handing every look at the pipeline to observe as well (the stress
// test records them as a time series).
func WaitObserved(ctx context.Context, sqlDB *sql.DB, redisURL string, queue embedding.Enqueuer, poll, timeout time.Duration, logf func(string, ...any), observe func(time.Time, Progress)) (Progress, error) {
	opt, err := asynq.ParseRedisURI(redisURL)
	if err != nil {
		return Progress{}, err
	}
	inspector := asynq.NewInspector(opt)
	defer inspector.Close()

	deadline := time.Now().Add(timeout)
	idle := 0
	for {
		p, err := progress(ctx, sqlDB, inspector)
		if err != nil {
			return p, err
		}
		if logf != nil {
			logf("  pipeline: %s", p)
		}
		if observe != nil {
			observe(time.Now(), p)
		}
		if p.Queued == 0 {
			if p.Unmatched == 0 && p.PrintsUnmatched == 0 {
				// Twice in a row: a task can be between dequeue and its handler.
				if idle++; idle >= 2 {
					return p, nil
				}
			}
			if p.Unmatched > 0 {
				if _, err := queue.Enqueue(embedding.NewSyncFaceTask()); err != nil && logf != nil {
					logf("  pipeline: nudge face sync: %v", err)
				}
			}
			if p.PrintsUnmatched > 0 {
				if _, err := queue.Enqueue(fingerprint.NewSyncTask()); err != nil && logf != nil {
					logf("  pipeline: nudge fingerprint sync: %v", err)
				}
			}
		} else {
			idle = 0
		}
		if time.Now().After(deadline) {
			return p, fmt.Errorf("load: trackid's pipeline did not finish within %s (%s); is cmd/worker running with the vision models and FINGERPRINT_SIDECAR_URL?", timeout, p)
		}
		select {
		case <-ctx.Done():
			return p, ctx.Err()
		case <-time.After(poll):
		}
	}
}

func progress(ctx context.Context, sqlDB *sql.DB, inspector *asynq.Inspector) (Progress, error) {
	var p Progress
	err := sqlDB.QueryRowContext(ctx, `
		WITH faces AS (
			SELECT b.id FROM biometric_features b
			JOIN identity_files f ON f.id = b.identity_file_id
			JOIN identity_registers r ON r.id = f.register_id
			WHERE r.register_number LIKE 'SIM-%' AND f.storage_ref NOT LIKE 'sim://%' AND f.file_type = 'photo'
			UNION ALL
			SELECT b.id FROM biometric_features b
			JOIN case_traces t ON t.id = b.case_trace_id
			JOIN case_evidences e ON e.id = t.evidence_id
			JOIN biometric_cases c ON c.id = e.biometric_case_id
			WHERE c.case_id LIKE 'SIM-%' AND c.modality = 'FACIAL' AND e.case_file_id IS NOT NULL AND t.box_x1 IS NOT NULL
		)
		SELECT count(*),
		       count(fe.biometric_feature_id),
		       count(fe.biometric_feature_id) FILTER (WHERE fe.matched_at IS NULL)
		FROM faces LEFT JOIN feature_embeddings fe
		  ON fe.biometric_feature_id = faces.id AND fe.embedding_type = $1`,
		embedding.EmbeddingType).Scan(&p.Faces, &p.Embedded, &p.Unmatched)
	if err != nil {
		return p, fmt.Errorf("load: pipeline progress: %w", err)
	}
	err = sqlDB.QueryRowContext(ctx, `
		WITH prints AS (
			SELECT b.id FROM biometric_features b
			JOIN identity_files f ON f.id = b.identity_file_id
			JOIN identity_registers r ON r.id = f.register_id
			WHERE r.register_number LIKE 'SIM-%' AND f.storage_ref NOT LIKE 'sim://%' AND f.file_type = 'nist'
			UNION ALL
			SELECT b.id FROM biometric_features b
			JOIN case_traces t ON t.id = b.case_trace_id
			JOIN case_evidences e ON e.id = t.evidence_id
			JOIN biometric_cases c ON c.id = e.biometric_case_id
			WHERE c.case_id LIKE 'SIM-%' AND c.modality = 'FINGERPRINT' AND e.case_file_id IS NOT NULL AND t.box_x1 IS NOT NULL
		)
		SELECT count(*),
		       count(bt.biometric_feature_id),
		       count(bt.biometric_feature_id) FILTER (WHERE bt.matched_at IS NULL)
		FROM prints LEFT JOIN biometric_templates bt
		  ON bt.biometric_feature_id = prints.id AND bt.template_type = $1`,
		fingerprint.TemplateType).Scan(&p.Prints, &p.Extracted, &p.PrintsUnmatched)
	if err != nil {
		return p, fmt.Errorf("load: pipeline progress (prints): %w", err)
	}
	info, err := inspector.GetQueueInfo("default")
	if err != nil {
		return p, fmt.Errorf("load: queue info: %w", err)
	}
	p.Pending, p.Active, p.Retry = info.Pending, info.Active, info.Retry
	p.Queued = info.Pending + info.Active + info.Scheduled + info.Retry
	p.Failed = info.Archived
	return p, nil
}
