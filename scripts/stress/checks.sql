-- Consistency after a concurrent run (scripts/stress/class.sh): each row is a check and the
-- number of rows breaking it. Every count should be 0.

-- The same examiner holds two steps of one analysis, so verification was not blind.
SELECT 'examiner holds two steps of one analysis' AS check, count(*) AS broken FROM (
  SELECT case_analysis_id, user_id FROM current_analysis_assignments
  WHERE user_id IS NOT NULL GROUP BY case_analysis_id, user_id HAVING count(*) > 1) x
UNION ALL
-- Distribution handed one step of one analysis out twice at once (two automatic rows).
SELECT 'step distributed twice', count(*) FROM (
  SELECT case_analysis_id, step FROM analysis_assignments
  WHERE method = 'AUTOMATIC' GROUP BY case_analysis_id, step HAVING count(*) > 1) x
UNION ALL
-- A submitted decision by someone who was never given that step of the analysis.
SELECT 'decision by an examiner never given the step', count(*)
FROM biometric_decisions d
JOIN users u ON u.username = d.username
LEFT JOIN analysis_assignments a
  ON a.case_analysis_id = d.case_analysis_id AND a.step = d.role AND a.user_id = u.id
WHERE d.role <> 'SYSTEM' AND a.case_analysis_id IS NULL
UNION ALL
-- An analysis in verification without the examination's decisions, or in inconsistence without
-- the verification's: a stage moved on without the step before it. (A review with nothing to
-- decide goes straight to REPORT with no decisions, by design, so REPORT is not checked.)
SELECT 'stage moved on without the previous step''s decisions', count(*)
FROM case_analyses ca
WHERE (ca.stage = 'VERIFICATION'
       AND NOT EXISTS (SELECT 1 FROM biometric_decisions d WHERE d.case_analysis_id = ca.id AND d.role = 'VERIFICATOR'))
   OR (ca.stage = 'INCONSISTENCE'
       AND NOT EXISTS (SELECT 1 FROM biometric_decisions d WHERE d.case_analysis_id = ca.id AND d.role = 'REVIEWER'))
UNION ALL
-- A cluster left with no members by a rebuild racing another.
SELECT 'cluster with no members', count(*)
FROM clusters c WHERE NOT EXISTS (SELECT 1 FROM cluster_members m WHERE m.cluster_id = c.id)
UNION ALL
-- A feature embedded or extracted twice.
SELECT 'feature embedded twice', count(*) FROM (
  SELECT biometric_feature_id, embedding_type FROM feature_embeddings GROUP BY 1, 2 HAVING count(*) > 1) x
UNION ALL
SELECT 'feature extracted twice', count(*) FROM (
  SELECT biometric_feature_id, template_type FROM biometric_templates GROUP BY 1, 2 HAVING count(*) > 1) x;
