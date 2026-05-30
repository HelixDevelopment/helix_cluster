-- ============================================================
-- 008_create_health_scores.down.sql
-- Drop Health Scores
-- ============================================================

DROP INDEX IF EXISTS idx_health_score;
DROP INDEX IF EXISTS idx_health_time;
DROP INDEX IF EXISTS idx_health_node;
DROP TABLE IF EXISTS health_scores;
